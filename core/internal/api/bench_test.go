package api

import (
	"context"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/bench"
	"forja/internal/engineapi"
	"forja/internal/store"
)

// benchEngine runs stages for the bench tests: every stage succeeds at once, measured, unless
// hold is set, which keeps a run going until it is closed or the run is cancelled.
type benchEngine struct {
	*fakeEngine
	st   *store.Store
	hold chan struct{}
	// metrics are what a stage type measured about its output.
	metrics map[string]map[string]float64
}

func (e *benchEngine) SharesStore() bool { return true }
func (e *benchEngine) HasArtifact(_ context.Context, ref string) (bool, error) {
	return e.st.Has(ref), nil
}
func (e *benchEngine) Upload(context.Context, string) error   { return nil }
func (e *benchEngine) Download(context.Context, string) error { return nil }

func (e *benchEngine) Run(ctx context.Context, req engineapi.RunRequest, _ chan<- engineapi.Event) (*engineapi.Run, error) {
	if e.hold != nil {
		select {
		case <-e.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	run := &engineapi.Run{ID: "r", Status: "succeeded", Stages: map[string]engineapi.StageState{}}
	for _, s := range req.Stages {
		ref, _ := e.st.PutBytes([]byte(s.ID + strings.Join(s.Inputs, ",")))
		secs, ram := 0.02, int64(1<<20)
		kind := "mesh/glb"
		if s.Use == "views.detect" {
			ref, _ = e.st.PutBytes([]byte(`{"sheet": false, "size": [1, 1], "parts": []}`))
			kind = "application/json"
		}
		st := engineapi.StageState{Status: "succeeded", Seconds: &secs, Outputs: []engineapi.Output{{Hash: ref, Kind: kind, Metrics: e.metrics[s.Use]}}}
		if req.Measure {
			st.Metrics = &engineapi.StageMetrics{Device: "cpu-pool", PeakRAMBytes: &ram}
		}
		run.Stages[s.ID] = st
	}
	return run, nil
}

func benchServer(t *testing.T) (*httptest.Server, *Server, *benchEngine, func(body string) string) {
	t.Helper()
	ts, s, eng := newServer(t)
	s.DataDir = t.TempDir()
	eng.installed["bg"], eng.installed["shape"] = true, true
	be := &benchEngine{fakeEngine: eng, st: s.Store}
	s.Providers.Add(be)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chest.png"), []byte("\x89PNG\r\n\x1a\nnot really a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	suite := func(body string) string {
		path := filepath.Join(dir, "suite.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return ts, s, be, suite
}

const apiSuite = "schema: 1\nname: api\nversion: 1\npresets: [draft]\nimages:\n  - path: chest.png\n"

func TestBenchRunsASuiteAndKeepsItsResult(t *testing.T) {
	ts, s, _, suite := benchServer(t)
	var started bench.Result
	if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite)}, &started); code != 202 ||
		started.Status != bench.Running || started.Total != 1 {
		t.Fatalf("status %d: %+v", code, started)
	}
	s.benches().Wait()
	var res bench.Result
	if code := call(t, ts, "GET", "/v1/bench/"+started.ID, nil, &res); code != 200 || res.Status != bench.Succeeded ||
		len(res.Jobs) != 1 || res.Jobs[0].Stages[0].PeakRAMBytes == nil || res.Summary == nil {
		t.Fatalf("status %d: %+v", code, res)
	}
	var list bench.List
	if code := call(t, ts, "GET", "/v1/bench", nil, &list); code != 200 || list.Running != nil || len(list.Results) != 1 ||
		list.Results[0].ID != res.ID || list.Results[0].Presets[0].Preset != "draft" {
		t.Fatalf("status %d: %+v", code, list)
	}
	resp, err := http.Get(ts.URL + "/v1/bench/" + res.ID + "/report")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") ||
		!strings.Contains(string(page), "api <span") {
		t.Fatalf("report %d %s", resp.StatusCode, page)
	}
	var cmp bench.Comparison
	if code := call(t, ts, "POST", "/v1/bench/compare", CompareRequest{Baseline: res.ID,
		Current: filepath.Join(s.DataDir, "bench", res.ID+".json")}, &cmp); code != 200 || cmp.Regressions != 0 ||
		cmp.Thresholds.Pct != 10 || len(cmp.Deltas) == 0 {
		t.Fatalf("compare %d: %+v", code, cmp)
	}

	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"DELETE", "/v1/bench/" + res.ID, nil, 409},
		{"GET", "/v1/bench/01J00000000000000000000000", nil, 404},
		{"GET", "/v1/bench/..%2f..%2fsecrets", nil, 404},
		{"POST", "/v1/bench", bench.Request{Suite: "suite.yaml"}, 422},
		{"POST", "/v1/bench", "not an object", 400},
		{"POST", "/v1/bench/compare", CompareRequest{Baseline: res.ID, Current: "01J00000000000000000000000"}, 404},
		{"POST", "/v1/bench/compare", CompareRequest{Baseline: res.ID, Current: res.ID,
			Thresholds: &bench.Thresholds{Pct: -1}}, 422},
	} {
		if code := call(t, ts, c.method, c.path, c.body, nil); code != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, code, c.want)
		}
	}
}

// What a stage measured about its output is kept per stage in the result, summed up, worded in the
// report and compared with a baseline that has none.
func TestBenchKeepsWhatStagesMeasuredAboutTheirOutputs(t *testing.T) {
	ts, s, eng, suite := benchServer(t)
	eng.metrics = map[string]map[string]float64{"shape": {"match_iou": 0.93, "lod_open_edges": 2}}
	var started bench.Result
	if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite)}, &started); code != 202 {
		t.Fatalf("status %d", code)
	}
	s.benches().Wait()
	var res bench.Result
	if code := call(t, ts, "GET", "/v1/bench/"+started.ID, nil, &res); code != 200 || res.Summary == nil {
		t.Fatalf("status %d: %+v", code, res)
	}
	for _, st := range res.Jobs[0].Stages {
		want := map[string]float64(nil)
		if st.Use == "shape" {
			want = map[string]float64{"match_iou": 0.93, "lod_open_edges": 2}
		}
		if !maps.Equal(st.Metrics, want) {
			t.Errorf("stage %s: metrics %v", st.ID, st.Metrics)
		}
	}
	for _, st := range res.Summary.Stages {
		if st.Use == "shape" && !maps.Equal(st.Metrics, map[string]float64{"match_iou": 0.93, "lod_open_edges": 2}) {
			t.Errorf("summary of %s: %v", st.Stage, st.Metrics)
		}
	}
	resp, err := http.Get(ts.URL + "/v1/bench/" + res.ID + "/report")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "reference fit 93%") || !strings.Contains(string(page), "LOD cracks: 2 open edges") {
		t.Fatalf("the report does not word the measures:\n%s", page)
	}
	// The same suite after a change that cracks the LODs further and blurs the reference fit.
	eng.metrics = map[string]map[string]float64{"shape": {"match_iou": 0.6, "lod_open_edges": 9}}
	var next bench.Result
	if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite)}, &next); code != 202 {
		t.Fatalf("status %d", code)
	}
	s.benches().Wait()
	var cmp bench.Comparison
	if code := call(t, ts, "POST", "/v1/bench/compare", CompareRequest{Baseline: res.ID, Current: next.ID}, &cmp); code != 200 ||
		cmp.Regressions != 2 {
		t.Fatalf("compare %d: %+v", code, cmp)
	}
}

func TestOneBenchmarkRunsAtATimeAndCanBeCancelled(t *testing.T) {
	ts, s, eng, suite := benchServer(t)
	eng.hold = make(chan struct{})
	var started bench.Result
	if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite), Cold: true}, &started); code != 202 {
		t.Fatalf("status %d", code)
	}
	if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite)}, nil); code != 409 {
		t.Fatalf("a second benchmark: %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/bench/"+started.ID, nil, nil); code != 202 {
		t.Fatalf("cancel: %d", code)
	}
	s.benches().Wait()
	var res bench.Result
	call(t, ts, "GET", "/v1/bench/"+started.ID, nil, &res)
	if res.Status != bench.Cancelled || !res.Options.Cold {
		t.Fatalf("%+v", res)
	}
}

// "Compare with the last run" picked a cancelled one, and then reported "nothing slower".
func TestCompareTakesTheNewestFinishedRunAndRefusesACancelledOne(t *testing.T) {
	ts, s, eng, suite := benchServer(t)
	run := func() bench.Result {
		var started bench.Result
		if code := call(t, ts, "POST", "/v1/bench", bench.Request{Suite: suite(apiSuite)}, &started); code != 202 {
			t.Fatalf("status %d", code)
		}
		return started
	}
	first := run()
	s.benches().Wait()
	time.Sleep(5 * time.Millisecond)
	second := run()
	s.benches().Wait()
	time.Sleep(5 * time.Millisecond)
	eng.hold = make(chan struct{})
	cancelled := run()
	call(t, ts, "DELETE", "/v1/bench/"+cancelled.ID, nil, nil)
	s.benches().Wait()

	var cmp bench.Comparison
	if code := call(t, ts, "POST", "/v1/bench/compare", CompareRequest{Baseline: first.ID}, &cmp); code != 200 || cmp.Current.ID != second.ID {
		t.Fatalf("no current: %d, compared with %s (the cancelled run is %s, want %s)", code, cmp.Current.ID, cancelled.ID, second.ID)
	}
	for _, req := range []CompareRequest{{Baseline: first.ID, Current: cancelled.ID}, {Baseline: cancelled.ID, Current: first.ID}} {
		var prob struct{ Detail string }
		if code := call(t, ts, "POST", "/v1/bench/compare", req, &prob); code != 422 || !strings.Contains(prob.Detail, "was cancelled") {
			t.Errorf("%+v: %d %q", req, code, prob.Detail)
		}
	}
	// With only one finished run there is nothing to compare it with.
	var prob struct{ Detail string }
	if code := call(t, ts, "POST", "/v1/bench/compare", CompareRequest{Baseline: second.ID}, &cmp); code != 200 || cmp.Current.ID != first.ID {
		t.Fatalf("baseline is the newest: %d, %s", code, cmp.Current.ID)
	}
	os.Remove(filepath.Join(s.DataDir, "bench", first.ID+".json"))
	if code := call(t, ts, "POST", "/v1/bench/compare", CompareRequest{Baseline: second.ID}, &prob); code != 422 || !strings.Contains(prob.Detail, "no other finished benchmark") {
		t.Fatalf("%d %q", code, prob.Detail)
	}
}
