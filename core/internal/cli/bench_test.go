package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forja/internal/bench"
	"forja/internal/library"
)

func (h *harness) suite(name, body string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return path
}

const testSuite = `schema: 1
name: test
version: 3
presets: [draft]
budgets: {draft: 10}
core_overhead_ms: 200
images:
  - path: chest.png
    category: prop
  - path: refs/not-yet.png
`

func TestBenchMeasuresEveryStageOfASuiteThenComparesRuns(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	h.ok("generate", img, "--preset", "draft") // the stage cache now holds every draft stage
	h.eng.set(func(e *fakeEngine) { e.ran, e.measured = nil, nil })
	suite := h.suite("suite.yaml", testSuite)
	out := filepath.Join(h.dir, "results")

	r := h.cli("", "bench", "--suite", suite, "--json", "--out", out)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	var res bench.Result
	if err := json.Unmarshal([]byte(r.out), &res); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if res.Status != bench.Succeeded || res.Schema != bench.ResultSchema || res.Suite.Version != 3 || len(res.Jobs) != 1 ||
		res.Device.ID != "test-gpu" || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "not there") {
		t.Fatalf("result %+v", res)
	}
	// A warm-up job, then the measured one: each ran every stage, none from the cache.
	h.eng.set(func(e *fakeEngine) {
		if n := strings.Count(strings.Join(e.ran, ","), "shape"); n != 2 || slices.Contains(e.measured, false) {
			t.Errorf("the engine ran %v (measured %v)", e.ran, e.measured)
		}
	})
	job := res.Jobs[0]
	if job.Status != library.Succeeded || job.Category != "prop" || job.Asset["triangles"] != 100.0 || job.WallSeconds <= 0 ||
		job.CoreOverheadSeconds <= 0 || job.CoreOverheadSeconds > job.WallSeconds || job.LoadSeconds != 0.5 { // bg and shape load a model each
		t.Fatalf("sample %+v", job)
	}
	var shape *bench.StageResult
	for i := range job.Stages {
		if job.Stages[i].ID == "shape" {
			shape = &job.Stages[i]
		}
	}
	if shape == nil || *shape.LoadSeconds != 0.25 || *shape.PeakVRAMBytes != 2<<30 || *shape.GPUUtil != 80 ||
		shape.Outputs[0].Triangles != 100 || *shape.PeakRAMBytes != 512<<20 {
		t.Fatalf("shape %+v", shape)
	}
	sum := res.Summary
	if sum == nil || len(sum.Presets) != 1 || sum.Presets[0].BudgetSeconds != 10 || sum.Presets[0].OverBudget != 0 ||
		sum.CoreOverhead.N != 1 || sum.CoreOverheadBudgetMS != 200 || len(sum.Devices) != 1 || sum.Devices[0].Device != "cuda:0" {
		t.Fatalf("summary %+v", sum)
	}
	// Samples make no asset: the library holds only what generate made.
	var assets []*library.Asset
	h.okJSON(&assets, "assets", "ls")
	if len(assets) != 1 {
		t.Fatalf("%d assets", len(assets))
	}

	saved := filepath.Join(out, benchJSON)
	page, err := os.ReadFile(filepath.Join(out, benchHTML))
	if err != nil || !strings.Contains(string(page), "test <span class=\"muted\">v3</span>") ||
		!strings.Contains(string(page), "refs") {
		t.Fatalf("report: %v\n%.400s", err, page)
	}
	if _, err := os.Stat(res.Report); err != nil {
		t.Fatalf("the core kept no report: %v", err)
	}
	if out := h.ok("bench", "ls"); !strings.Contains(out, res.ID) || !strings.Contains(out, "test v3") ||
		!strings.Contains(out, "draft") {
		t.Fatalf("bench ls:\n%s", out)
	}
	if out := h.ok("bench", "show", res.ID); !strings.Contains(out, "core overhead per job") || !strings.Contains(out, "shape") {
		t.Fatalf("bench show:\n%s", out)
	}
	if out := h.ok("bench", "report", res.ID); !strings.HasPrefix(out, "<!doctype html>") {
		t.Fatalf("bench report:\n%.200s", out)
	}

	// Against itself nothing regressed; against a run whose shape stage took three times as
	// long, the threshold catches it, and a high enough threshold lets it pass.
	if out := h.ok("bench", "compare", saved, res.ID); !strings.Contains(out, "No regression past 10%") {
		t.Fatalf("compare:\n%s", out)
	}
	slower := res
	stages := slices.Clone(res.Summary.Stages)
	for i := range stages {
		if stages[i].Stage == "shape" {
			stages[i].Seconds.Median = 3 * stages[i].Seconds.Median
			stages[i].Seconds.Median += 0.1 // past the noise floor
		}
	}
	s := *res.Summary
	s.Stages = stages
	slower.Summary = &s
	raw, _ := json.Marshal(slower)
	slowerPath := filepath.Join(h.dir, "slower.json")
	if err := os.WriteFile(slowerPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r = h.cli("", "bench", "compare", saved, slowerPath)
	if r.code != exitRegression || !strings.Contains(r.out, "REGRESSION") || !strings.Contains(r.err, "1 regressions past 10%") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	var cmp bench.Comparison
	r = h.cli("", "--json", "bench", "compare", saved, slowerPath, "--threshold", "100000")
	if r.code != 0 || json.Unmarshal([]byte(r.out), &cmp) != nil || cmp.Regressions != 0 || cmp.Thresholds.Pct != 100000 {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
}

func TestBenchQueuesABatchColdAndRepeated(t *testing.T) {
	h := newHarness(t)
	h.image("chest.png")
	h.image("barrel.png")
	suite := h.suite("suite.yaml", "schema: 1\nname: b\nversion: 1\npresets: [draft]\nimages:\n  - path: chest.png\n  - path: barrel.png\n")
	var res bench.Result
	h.okJSON(&res, "bench", "--suite", suite, "--batch", "--cold", "--repeat", "2", "--label", "nightly")
	var names []string
	for _, j := range res.Jobs {
		names = append(names, fmt.Sprintf("%s#%d:%s", j.Image, j.Repeat, j.Status))
	}
	if got := strings.Join(names, ","); got != "chest#1:succeeded,barrel#1:succeeded,chest#2:succeeded,barrel#2:succeeded" ||
		!res.Options.Batch || !res.Options.Cold || res.Label != "nightly" || res.Summary.Presets[0].Seconds.N != 4 {
		t.Fatalf("%s %+v", got, res.Options)
	}
	// Cold runs load nothing first: only the four samples ran.
	h.eng.set(func(e *fakeEngine) {
		if n := strings.Count(strings.Join(e.ran, ","), "shape"); n != 4 {
			t.Errorf("shape ran %d times", n)
		}
	})
}

func TestBenchRefusesWhatCannotRun(t *testing.T) {
	h := newHarness(t)
	h.image("chest.png")
	empty := h.suite("empty.yaml", "schema: 1\nname: e\nversion: 1\npresets: [draft]\nimages:\n  - dir: refs\n")
	if r := h.cli("", "bench", "--suite", empty); r.code != 1 || !strings.Contains(r.err, "no images to run here") ||
		!strings.Contains(r.err, "no such folder") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if r := h.cli("", "bench", "--suite", h.suite("s.yaml", testSuite), "--preset", "no-such"); r.code != 1 ||
		!strings.Contains(r.err, `unknown preset "no-such"`) {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	h.eng.set(func(e *fakeEngine) { delete(e.installed, "shape") })
	if r := h.cli("", "bench", "--suite", h.suite("s.yaml", testSuite)); r.code != 1 || !strings.Contains(r.err, "no shape model") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if r := h.cli("", "bench", "cancel", "01J00000000000000000000000"); r.code != 1 || !strings.Contains(r.err, "not running") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if r := h.cli("", "bench", "compare", "nothing", "else"); r.code != 1 || !strings.Contains(r.err, "neither a result id") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}
