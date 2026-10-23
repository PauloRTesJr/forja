package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/provider"
	"forja/internal/store"
)

// fakeProvider "runs" every stage by storing a deterministic blob derived from its inputs.
type fakeProvider struct {
	st   *store.Store
	mu   sync.Mutex
	runs [][]string // stage ids per run
	// What it says it runs; tests bump these between jobs. down makes Info and Models fail.
	stages map[string]string
	shape  engineapi.Model
	down   bool
}

func (f *fakeProvider) ID() string                   { return LocalProvider }
func (f *fakeProvider) Kind() provider.Kind          { return provider.KindLocal }
func (f *fakeProvider) Paid() bool                   { return false }
func (f *fakeProvider) SharesStore() bool            { return true }
func (f *fakeProvider) Health(context.Context) error { return nil }
func (f *fakeProvider) Info(context.Context) (*engineapi.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	stages := map[string]string{}
	for k, v := range f.stages {
		stages[k] = v
	}
	return &engineapi.Info{Stages: stages}, nil
}

func (f *fakeProvider) Models(context.Context) ([]engineapi.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	return []engineapi.Model{
		{ID: "stub-background", AdapterVersion: "1"},
		f.shape,
		{ID: "stub-paint", AdapterVersion: "1"},
	}, nil
}

func (f *fakeProvider) set(fn func(f *fakeProvider)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}
func (f *fakeProvider) HasArtifact(_ context.Context, ref string) (bool, error) {
	return f.st.Has(ref), nil
}
func (f *fakeProvider) Upload(context.Context, string) error   { return nil }
func (f *fakeProvider) Download(context.Context, string) error { return nil }
func (f *fakeProvider) Cancel(context.Context, string) error   { return nil }

func (f *fakeProvider) Run(_ context.Context, req engineapi.RunRequest, evs chan<- engineapi.Event) (*engineapi.Run, error) {
	ids := []string{}
	run := &engineapi.Run{ID: "r", Status: "succeeded", Stages: map[string]engineapi.StageState{}}
	produced := map[string]string{}
	for _, s := range req.Stages {
		ids = append(ids, s.ID)
		var inputs []string
		for _, in := range s.Inputs {
			if id, ok := strings.CutPrefix(in, "stage:"); ok {
				in = produced[id]
			}
			inputs = append(inputs, in)
		}
		ref, _ := f.st.PutBytes([]byte(s.ID + s.Model + strings.Join(inputs, ",") + fmtParams(s.Params)))
		produced[s.ID] = ref
		secs := 0.01
		evs <- engineapi.Event{Type: "stage.started", Data: map[string]any{"stage": s.ID}}
		run.Stages[s.ID] = engineapi.StageState{Status: "succeeded", Seconds: &secs,
			Outputs: []engineapi.Output{{Hash: ref, Kind: "mesh/glb"}}}
	}
	f.mu.Lock()
	f.runs = append(f.runs, ids)
	f.mu.Unlock()
	return run, nil
}

func fmtParams(p map[string]any) string {
	raw, _ := json.Marshal(p) // sorted keys: deterministic
	return string(raw)
}

func setup(t *testing.T) (*Runner, *fakeProvider, *events.Hub, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "config"))
	st, _ := store.Open(filepath.Join(dir, "store"))
	db, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	fp := &fakeProvider{st: st, stages: map[string]string{"bake": "1"}, shape: engineapi.Model{
		ID: "stub-shape", License: engineapi.License{SPDX: "Apache-2.0"}, AdapterVersion: "1",
	}}
	reg := provider.NewRegistry()
	reg.Add(fp)
	hub := events.NewHub()
	cfg := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	r := NewRunner(db, st, hub, reg, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", 4)
	img, _ := st.PutBytes([]byte("fake image"))
	return r, fp, hub, st, img
}

func wait(t *testing.T, r *Runner, id string) *library.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, err := r.Job(context.Background(), id)
		if err == nil && j.Status.Done() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return nil
}

func TestTextureRerunHitsTheShapeCache(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	j1, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop", Name: "chest.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j1.ID); got.Status != library.Succeeded || got.AssetID == "" {
		t.Fatalf("job 1: %s %s", got.Status, got.Error)
	}
	j2, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop",
		Overrides: map[string]any{"paint.resolution": 2048}})
	got := wait(t, r, j2.ID)
	cached := 0
	for _, s := range got.Stages {
		if s.Cached {
			cached++
		}
	}
	if cached != 6 {
		t.Fatalf("expected the 6 pre-texture stages cached, got %d", cached)
	}
	if last := fp.runs[len(fp.runs)-1]; strings.Join(last, ",") != "paint,bake,lods,export" {
		t.Fatalf("second run executed %v", last)
	}
}

func TestExportWritesGLBAndSidecar(t *testing.T) {
	r, _, _, _, img := setup(t)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[project]\nexport_dir = \"Assets/Gen\"\n"), 0o644)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Name: "My Chest!.png", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	for _, f := range []string{"my-chest.glb", "my-chest.forja.json"} {
		st, err := os.Stat(filepath.Join(project, "Assets", "Gen", "my-chest", f))
		if err != nil {
			t.Fatal(err)
		}
		// Windows has no Unix permission bits: every writable file reports 0666.
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o644 {
			t.Errorf("%s has mode %v", f, st.Mode().Perm())
		}
	}
}

func TestPaidProvidersAreNeverAnAutomaticChoice(t *testing.T) {
	r, _, _, _, img := setup(t)
	if _, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft",
		Overrides: map[string]any{"shape.provider": "runpod"}}); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

// executed returns the stage ids the provider ran in its latest run, or "" when it ran nothing.
func (f *fakeProvider) executed(before int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == before {
		return ""
	}
	return strings.Join(f.runs[len(f.runs)-1], ",")
}

func TestNewVersionsOnTheProviderRecomputeFromTheStageThatChanged(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	submit := func() *library.Job {
		t.Helper()
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
		if err != nil {
			t.Fatal(err)
		}
		got := wait(t, r, j.ID)
		if got.Status != library.Succeeded {
			t.Fatalf("%s: %s", got.Status, got.Error)
		}
		return got
	}
	first := submit()
	fp.set(func(f *fakeProvider) { f.stages["bake"] = "2" })
	n := fp.count()
	submit()
	if got := fp.executed(n); got != "bake,lods,export" {
		t.Fatalf("after a new bake version the provider ran %q", got)
	}
	fp.set(func(f *fakeProvider) { f.shape.Weights.Revision = "next" })
	n = fp.count()
	submit()
	if got := fp.executed(n); got != "shape,cleanup,lowpoly,uv,paint,bake,lods,export" {
		t.Fatalf("after new shape weights the provider ran %q", got)
	}
	if first.Stages[0].Key == "" {
		t.Fatal("keys are not recorded on the job")
	}
}

func TestAnUnreachableProviderKeysWithItsLastAnswer(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	fp.set(func(f *fakeProvider) { f.down = true })
	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
	if got := wait(t, r, j.ID); got.Status != library.Failed || !strings.Contains(got.Error, "did not say what it runs") {
		t.Fatalf("with no answer ever: %s %q", got.Status, got.Error)
	}
	fp.set(func(f *fakeProvider) { f.down = false })
	j, _ = r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("first answered job: %s %s", got.Status, got.Error)
	}
	fp.set(func(f *fakeProvider) { f.down = true })
	n := fp.count()
	j, _ = r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("fully cached job while the provider is down: %s %s", got.Status, got.Error)
	}
	if got := fp.executed(n); got != "" {
		t.Fatalf("a fully cached job ran %q", got)
	}
}
