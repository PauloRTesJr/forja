package bench

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/store"
)

func write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestASuiteNamesImagesByPathOrFolderAndSkipsWhatIsNotThere(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "refs", "b.png"), "png")
	write(t, filepath.Join(dir, "refs", "a.JPG"), "jpeg")
	write(t, filepath.Join(dir, "refs", "notes.txt"), "not an image")
	write(t, filepath.Join(dir, "props", "a.png"), "another a")
	path := write(t, filepath.Join(dir, "suites", "s.yaml"), `schema: 1
name: s
version: 2
presets: [draft, mobile-prop]
project: ../m0
images:
  - dir: ../refs
    category: mixed
  - path: ../props/a.png
    category: prop
  - path: ../missing.png
  - dir: ../empty
`)
	s, err := LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Project != filepath.Join(dir, "m0") || s.Version != 2 {
		t.Fatalf("suite %+v", s)
	}
	images, skipped := s.Resolve()
	var names []string
	for _, im := range images {
		names = append(names, im.Name+":"+im.Category)
	}
	if got := strings.Join(names, ","); got != "a:mixed,b:mixed,a-2:prop" {
		t.Fatalf("images %s", got)
	}
	if len(skipped) != 2 || !strings.Contains(skipped[0].Reason, "not there") || skipped[1].Reason != "no such folder" {
		t.Fatalf("skipped %+v", skipped)
	}

	for body, want := range map[string]string{
		"schema: 2\nname: s\npresets: [draft]\nimages: [{path: a.png}]\n":            "suite schema 1",
		"schema: 1\nname: s\npresets: [draft]\nimages: [{path: a.png, dir: refs}]\n": "either path or dir",
		"schema: 1\nname: s\nimages: [{path: a.png}]\n":                              "no presets",
		"schema: 1\nname: s\npresets: [draft]\nimagez: []\n":                         "imagez",
	} {
		if _, err := LoadSuite(write(t, filepath.Join(dir, "bad.yaml"), body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestShippedSuitesLoad(t *testing.T) {
	for _, name := range []string{"default.yaml", "ci.yaml"} {
		s, err := LoadSuite(filepath.Join("..", "..", "..", "bench", "suites", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(s.Project, "forja.toml")); err != nil {
			t.Errorf("%s pins no models: %v", name, err)
		}
	}
	// The CI suite's image is in the repository, so it always has something to run.
	s, _ := LoadSuite(filepath.Join("..", "..", "..", "bench", "suites", "ci.yaml"))
	if images, skipped := s.Resolve(); len(images) != 1 || len(skipped) != 0 {
		t.Fatalf("ci suite: %v, skipped %v", images, skipped)
	}
}

func TestSpreadUsesTheNearestRank(t *testing.T) {
	if got := spread([]float64{5, 1, 3, 2, 4}); got != (Spread{N: 5, Median: 3, P90: 5, Max: 5}) {
		t.Fatalf("%+v", got)
	}
	if got := spread([]float64{1, 2}); got.Median != 1.5 {
		t.Fatalf("%+v", got)
	}
	if got := spread(nil); got != (Spread{}) {
		t.Fatalf("%+v", got)
	}
}

func sample(preset string, wall float64, stages map[string]float64) jobs.Sample {
	j := &library.Job{ID: "j-" + preset, Status: library.Succeeded}
	metrics := map[string]*engineapi.StageMetrics{}
	for id, secs := range stages {
		j.Stages = append(j.Stages, library.Stage{ID: id, Use: id, Status: library.Succeeded, Seconds: secs, PeakVRAM: 1 << 30})
		load, util := 0.5, 50.0
		metrics[id] = &engineapi.StageMetrics{Device: "cuda:0", LoadSeconds: &load, GPUUtil: &util, MeasureSeconds: 0.1,
			Outputs: []engineapi.OutputStats{{Triangles: 5000, Watertight: true}}}
	}
	return jobs.Sample{Job: j, Wall: time.Duration(wall * float64(time.Second)), Engine: time.Duration(0.9 * wall * float64(time.Second)),
		Metrics: metrics}
}

func TestASummaryWeighsSamplesAgainstTheBudgets(t *testing.T) {
	res := &Result{Options: Options{Presets: []string{"draft"}}}
	for _, wall := range []float64{4.1, 12.1} {
		res.Jobs = append(res.Jobs, jobResult(JobResult{Preset: "draft"}, sample("draft", wall, map[string]float64{"shape": wall - 1})))
	}
	res.Jobs = append(res.Jobs, JobResult{Preset: "draft", Status: library.Failed})
	sum := summarize(res, &Suite{Budgets: map[string]float64{"draft": 10}, CoreOverheadMS: 200}, 20*time.Second)
	p := sum.Presets[0]
	// The engine's 0.1 s of measuring is no part of an image's time.
	if p.Images != 3 || p.Failed != 1 || p.OverBudget != 1 || p.Seconds.Max != 12 || p.Seconds.N != 2 {
		t.Fatalf("preset %+v", p)
	}
	st := sum.Stages[0]
	if st.Stage != "shape" || st.Seconds.N != 2 || *st.PeakVRAMBytes != 1<<30 || *st.GPUUtil != 50 || *st.Triangles != 5000 ||
		*st.Watertight != 1 || st.LoadSeconds.Median != 0.5 {
		t.Fatalf("stage %+v", st)
	}
	d := sum.Devices[0]
	if d.Device != "cuda:0" || d.BusySeconds != 14.2 || d.Busy != 0.71 || *d.GPUUtil != 0.355 {
		t.Fatalf("device %+v", d)
	}
	if sum.CoreOverhead.N != 2 || sum.CoreOverhead.Max < 1.2 {
		t.Fatalf("overhead %+v", sum.CoreOverhead)
	}
}

func result(id string, stageSecs, vram, overhead float64, failed int) *Result {
	v := int64(vram * (1 << 30))
	return &Result{Schema: ResultSchema, ID: id, Device: Device{ID: "gpu"}, Suite: SuiteRef{Name: "s", Version: 1},
		Summary: &Summary{
			Presets:      []PresetSummary{{Preset: "draft", Failed: failed, Seconds: Spread{N: 1, Median: stageSecs + 1}}},
			Stages:       []StageSummary{{Preset: "draft", Stage: "shape", Seconds: Spread{N: 1, Median: stageSecs}, PeakVRAMBytes: &v}},
			CoreOverhead: Spread{N: 1, Median: overhead},
		}}
}

func TestCompareFailsOnlyPastTheThresholdAndTheNoiseFloor(t *testing.T) {
	th := Thresholds{Pct: 10, MinSeconds: 0.05, MinVRAMMB: 64}
	regressions := func(base, cur *Result) []string {
		t.Helper()
		c, err := Compare(base, cur, th)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range c.Deltas {
			if d.Regression {
				out = append(out, d.Stage+"/"+d.Metric)
			}
		}
		if len(out) != c.Regressions {
			t.Fatalf("%d regressions counted, %v marked", c.Regressions, out)
		}
		return out
	}
	base := result("a", 10, 6, 0.05, 0)
	if got := regressions(base, result("b", 10.9, 6.5, 0.054, 0)); len(got) != 0 {
		t.Fatalf("within 10%%: %v", got)
	}
	if got := strings.Join(regressions(base, result("b", 11.5, 7, 0.05, 0)), ","); got != "/seconds,shape/seconds,shape/peak_vram_bytes" {
		t.Fatalf("past 10%%: %s", got)
	}
	// 20 ms that became 30 ms is 50% slower, and noise.
	if got := regressions(result("a", 0.02, 1, 0.01, 0), result("b", 0.03, 1, 0.02, 0)); len(got) != 0 {
		t.Fatalf("noise: %v", got)
	}
	if got := strings.Join(regressions(base, result("b", 10, 6, 0.05, 1)), ","); got != "/failed" {
		t.Fatalf("a failure: %s", got)
	}
	// Faster is never a regression.
	if got := regressions(base, result("b", 5, 3, 0.01, 0)); len(got) != 0 {
		t.Fatalf("faster: %v", got)
	}

	other := result("b", 10, 6, 0.05, 0)
	other.Device.ID = "cpu"
	if c, _ := Compare(base, other, th); len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "ran on gpu") {
		t.Fatalf("warnings %v", c.Warnings)
	}
	if _, err := Compare(base, &Result{ID: "running"}, th); err == nil {
		t.Fatal("a run without a summary compared")
	}
	if _, err := Compare(base, base, Thresholds{}); err == nil {
		t.Fatal("a zero threshold compared")
	}
}

func TestTheReportCarriesItsThumbnails(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 300, 150))
	for x := range 300 {
		for y := range 150 {
			img.Set(x, y, color.RGBA{uint8(x), 100, 50, 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	ref, _ := st.PutBytes(buf.Bytes())
	path, _ := st.Path(ref)
	uri, err := thumbnail(path, 96)
	if err != nil || !strings.HasPrefix(string(uri), "data:image/png;base64,") {
		t.Fatalf("%v %.40s", err, uri)
	}

	res := result("01J00000000000000000000000", 1, 1, 0.01, 0)
	res.Suite.Name, res.Status = "<suite>", Succeeded
	res.Jobs = []JobResult{{Image: "chest", Preset: "draft", Input: ref, Status: library.Succeeded},
		{Image: "gone", Preset: "draft", Input: "blake3:" + strings.Repeat("0", 64), Status: library.Failed, Error: "boom"}}
	page, err := (&Bench{Store: st}).Report(res)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if !strings.Contains(html, "&lt;suite&gt;") || strings.Count(html, `<img class="thumb"`) != 1 || !strings.Contains(html, "boom") {
		t.Fatalf("report:\n%s", html)
	}
}

// The app opens a result's report with the OS's default program, so its path comes from the
// result's id inside the bench folder, never from what the result file says.
func TestAReportIsTheHTMLFileNamedAfterItsResult(t *testing.T) {
	dir := t.TempDir()
	b := &Bench{Dir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const id = "01J00000000000000000000000"
	res := result(id, 1, 1, 0.01, 0)
	res.Report = filepath.Join(t.TempDir(), "run-me.exe")
	raw, _ := json.Marshal(res)
	write(t, filepath.Join(dir, id+".json"), string(raw))
	got, err := b.Get(id)
	if err != nil || got.Report != "" {
		t.Fatalf("a result without its report: %q %v", got.Report, err)
	}
	write(t, filepath.Join(dir, id+".html"), "<!doctype html>")
	if got, _ := b.Get(id); got.Report != filepath.Join(dir, id+".html") {
		t.Fatalf("report %q", got.Report)
	}
	list, err := b.List()
	if err != nil || len(list.Results) != 1 || list.Results[0].Report != filepath.Join(dir, id+".html") {
		t.Fatalf("list %+v %v", list.Results, err)
	}
}

func TestDevicesAreNamedByTheirGPUElseTheirCPU(t *testing.T) {
	gpu := deviceOf("local", map[string]any{"os": "linux", "devices": []any{
		map[string]any{"id": "cuda:0", "backend": "cuda", "name": "NVIDIA GeForce RTX 4070 Ti", "vram_bytes": 12.0 * (1 << 30)},
		map[string]any{"id": "cpu", "backend": "cpu", "name": "AMD Ryzen 9"},
	}})
	if gpu.ID != "nvidia-geforce-rtx-4070-ti" || gpu.CPU != "AMD Ryzen 9" || gpu.VRAMBytes != 12<<30 {
		t.Fatalf("%+v", gpu)
	}
	if cpu := deviceOf("local", map[string]any{"devices": []any{map[string]any{"backend": "cpu", "name": "x86_64"}}}); cpu.ID != "cpu-x86-64" {
		t.Fatalf("%+v", cpu)
	}
}
