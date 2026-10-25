package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"forja/internal/catalog"
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
	// extra models it lists besides background, shape and paint; drop hides listed ones.
	extra []engineapi.Model
	drop  map[string]bool
	// sheet is what views.detect reports; specs are the stages it was sent, by id.
	sheet     string
	specs     map[string]engineapi.StageSpec
	clientIDs map[string]bool
	// hold, when set, keeps runs that build a shape going until it is closed; held counts them.
	hold chan struct{}
	held int
	// pngs makes the images it prepares start like PNG files, for a hosted model that checks.
	pngs bool
	// unavailable are the built-ins it says it cannot run here, with why.
	unavailable map[string]string
	// notes are what a stage type says about its first output, such as a stand-in it made.
	notes map[string]string
}

var cpu = "cpu"

// model is a fake model that is installed and runnable.
func model(id, task string, rank int) engineapi.Model {
	return engineapi.Model{ID: id, Name: id, Tasks: []string{task}, Status: "ready", Device: &cpu, AdapterVersion: "1", Rank: rank}
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
	return &engineapi.Info{Stages: stages, Unavailable: f.unavailable}, nil
}

func (f *fakeProvider) Models(context.Context) ([]engineapi.Model, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	var out []engineapi.Model
	for _, m := range append([]engineapi.Model{
		model("stub-background", "background-removal", 1), f.shape, model("stub-paint", "texture", 1),
	}, f.extra...) {
		if !f.drop[m.ID] {
			out = append(out, m)
		}
	}
	return out, nil
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
	// A real engine answers a repeated client id with the run it already has.
	f.mu.Lock()
	if f.clientIDs == nil {
		f.clientIDs = map[string]bool{}
	}
	reused := f.clientIDs[req.ClientRunID]
	f.clientIDs[req.ClientRunID] = true
	f.mu.Unlock()
	if reused {
		return nil, fmt.Errorf("client run id %s was sent twice; the engine would replay the first run", req.ClientRunID)
	}
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	for _, s := range req.Stages {
		if s.Use == "shape" && hold != nil {
			f.mu.Lock()
			f.held++
			f.mu.Unlock()
			<-hold
			break
		}
	}
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
		blob := s.ID + s.Model + strings.Join(inputs, ",") + strings.Join(s.Views, ",") + fmtParams(s.Params)
		if f.pngs && (s.Use == "background-removal" || s.Use == "framing") {
			blob = "\x89PNG" + blob
		}
		outs := []engineapi.Output{{Kind: "mesh/glb"}}
		switch s.Use {
		case "views.detect":
			f.mu.Lock()
			blob = f.sheet
			f.mu.Unlock()
			if blob == "" {
				blob = `{"sheet": false, "size": [100, 100], "parts": [{"box": [10, 10, 90, 90], "kind": "figure", "view": ""}]}`
			}
			outs[0].Kind = "application/json"
		case "views.split":
			views, _ := s.Params["views"].([]map[string]any)
			outs = nil
			for _, v := range views {
				ref, _ := f.st.PutBytes([]byte(blob + fmt.Sprint(v["view"])))
				outs = append(outs, engineapi.Output{Hash: ref, Kind: "image/png"})
			}
		}
		ref, _ := f.st.PutBytes([]byte(blob))
		if outs[0].Hash == "" {
			outs[0].Hash = ref
		}
		f.mu.Lock()
		outs[0].Note = f.notes[s.Use]
		f.mu.Unlock()
		produced[s.ID] = outs[0].Hash
		secs := 0.01
		evs <- engineapi.Event{Type: "stage.started", Data: map[string]any{"stage": s.ID}}
		run.Stages[s.ID] = engineapi.StageState{Status: "succeeded", Seconds: &secs, Outputs: outs}
		f.mu.Lock()
		if f.specs == nil {
			f.specs = map[string]engineapi.StageSpec{}
		}
		f.specs[s.ID] = s
		f.mu.Unlock()
	}
	f.mu.Lock()
	f.runs = append(f.runs, ids)
	f.mu.Unlock()
	return run, nil
}

func (f *fakeProvider) spec(id string) engineapi.StageSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[id]
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
	builtins := map[string]string{}
	for _, use := range []string{"framing", "mesh.cleanup", "mesh.decimate", "mesh.uv", "texture.match", "bake", "mesh.lods",
		"export", "views.detect", "views.split"} {
		builtins[use] = "1"
	}
	shape := model("stub-shape", "shape", 1)
	shape.License = engineapi.License{SPDX: "Apache-2.0"}
	fp := &fakeProvider{st: st, stages: builtins, shape: shape, drop: map[string]bool{}}
	reg := provider.NewRegistry()
	reg.Add(fp)
	hub := events.NewHub()
	cfg := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	cat := catalog.New(reg, db, nil, nil)
	r := NewRunner(db, st, hub, reg, cat, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", 4)
	// Jobs a test did not wait for still write to the temp folder; let them end first.
	t.Cleanup(func() {
		for deadline := time.Now().Add(5 * time.Second); r.Busy() && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
	})
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

// Nothing carries over from one job to the next: the same image again runs every stage again.
func TestTheSameJobAgainRunsEveryStage(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	var ran []string
	for range 2 {
		n := fp.count()
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop", Name: "chest.png"})
		if err != nil {
			t.Fatal(err)
		}
		if got := wait(t, r, j.ID); got.Status != library.Succeeded || got.AssetID == "" {
			t.Fatalf("job: %s %s", got.Status, got.Error)
		}
		ran = append(ran, fp.runsSince(n))
	}
	if ran[0] != "bg,sheet|frame,shape,cleanup,lowpoly,paint,bake,lods,export" || ran[1] != ran[0] {
		t.Fatalf("the provider ran %q", ran)
	}
}

// A stage that made a stand-in says so on its output, and the job keeps that note.
func TestAnOutputsNoteStaysWithIt(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) { f.notes = map[string]string{"mesh.decimate": "simplified instead"} })
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range wait(t, r, j.ID).Stages {
		want := []string(nil)
		if s.Use == "mesh.decimate" {
			want = []string{"simplified instead"}
		}
		if !slices.Equal(s.OutputNotes(), want) {
			t.Fatalf("stage %s: notes %q", s.ID, s.OutputNotes())
		}
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

// stageModels maps stage id to the model a submitted job uses.
func stageModels(j *library.Job) map[string]string {
	out := map[string]string{}
	for _, s := range j.Stages {
		out[s.ID] = s.Model
	}
	return out
}

func TestOpenStagesGetTheBestReadyModel(t *testing.T) {
	r, fp, _, _, img := setup(t)
	notReady := model("shape-big", "shape", 30)
	notReady.Status = "not-installed"
	stub := model("shape-stub", "shape", 40)
	stub.Stub = true
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{model("shape-a", "shape", 5), model("shape-b", "shape", 10), notReady, stub}
	})
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageModels(j); got["shape"] != "shape-b" || got["bg"] != "stub-background" || got["frame"] != "" {
		t.Fatalf("picked %v", got)
	}

	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[models]\nstubs = true\n"), 0o644)
	j, _ = r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if got := stageModels(j)["shape"]; got != "shape-stub" {
		t.Fatalf("with stubs allowed the shape stage got %q", got)
	}
}

func TestConfigAndOverridesPinModels(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{model("shape-a", "shape", 5), model("shape-b", "shape", 10)}
	})
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[models.stages]\nshape = \"shape-a\"\n"), 0o644)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageModels(j)["shape"]; got != "shape-a" {
		t.Fatalf("config pin ignored: %q", got)
	}
	j, _ = r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project,
		Overrides: map[string]any{"shape.model": "stub-shape"}})
	if got := stageModels(j)["shape"]; got != "stub-shape" {
		t.Fatalf("job override ignored: %q", got)
	}
	if _, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft",
		Overrides: map[string]any{"shape.model": "not-here"}}); err == nil {
		t.Fatal("a pinned model the provider does not have was accepted")
	}
}

func TestAPresetWithoutAModelForAStageIsRefused(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) { f.drop["stub-paint"] = true })
	_, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop"})
	var missing *MissingError
	if !errors.As(err, &missing) || strings.Join(missing.Uses, ",") != "texture" {
		t.Fatalf("got %v", err)
	}
	if _, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft"}); err != nil {
		t.Fatalf("draft needs no texture model: %v", err)
	}
	all, _ := r.Presets("")
	plan, err := r.Resolve(context.Background(), "", all["mobile-prop"], nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan {
		if p.Missing != (p.Use == "texture") {
			t.Fatalf("stage %s missing = %v", p.Stage, p.Missing)
		}
	}
}

func TestOptionalStagesRunOnlyWhenSwitchedOn(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
	wait(t, r, j.ID)
	if _, ok := stageModels(j)["match"]; ok {
		t.Fatal("match ran while off by default")
	}
	n := fp.count()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop", Overrides: map[string]any{"match.enabled": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if got := fp.executed(n); got != "frame,shape,cleanup,lowpoly,paint,match,bake,lods,export" {
		t.Fatalf("switching match on ran %q", got)
	}
}

// blenderBox fakes another computer's engine, one that has Blender.
type blenderBox struct{ *fakeProvider }

func (b *blenderBox) ID() string          { return "box" }
func (b *blenderBox) Kind() provider.Kind { return provider.KindEngine }
func (b *blenderBox) Info(context.Context) (*engineapi.Info, error) {
	return &engineapi.Info{Stages: map[string]string{"mesh.remesh": "1+blender-5.2.1", "mesh.uv.smart": "1+blender-5.2.1"}}, nil
}
func (b *blenderBox) Models(context.Context) ([]engineapi.Model, error) { return nil, nil }

func TestBlenderStagesRunWhereBlenderIsAndAreRefusedWhereNoneIs(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	why := "Blender 4.2 or newer was not found on the computer that runs the engine"
	fp.set(func(f *fakeProvider) {
		f.unavailable = map[string]string{"mesh.remesh": why, "mesh.uv.smart": why}
	})
	on := map[string]any{"remesh.enabled": true, "uv.enabled": true}
	_, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "environment-piece", Overrides: on})
	if err == nil || !strings.Contains(err.Error(), "no provider can run mesh.remesh: "+why) {
		t.Fatalf("got %v", err)
	}
	all, _ := r.Presets("")
	off, err := r.Unavailable(ctx, "", all["environment-piece"])
	if err != nil || off["remesh"].Reason != why || off["uv"].Reason != why || !off["remesh"].Software {
		t.Fatalf("unavailable %v, %v", off, err)
	}
	if _, ok := off["match"]; ok {
		t.Fatalf("match runs anywhere, yet: %v", off["match"])
	}

	r.providers.Add(&blenderBox{fp})
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[routing]\npriority = [\"local\", \"box\"]\n"), 0o644)
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "environment-piece", Project: project, Overrides: on})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageIDs(j); got != "bg,frame,shape,cleanup,remesh,uv,paint,bake,lods,export" {
		t.Fatalf("stages %s", got)
	}
	for _, s := range j.Stages {
		if want := map[bool]string{true: "box", false: LocalProvider}[s.Use == "mesh.remesh" || s.Use == "mesh.uv.smart"]; s.Provider != want {
			t.Fatalf("stage %s runs on %q, want %q", s.ID, s.Provider, want)
		}
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if got := fp.spec("remesh").Params["target_tris"]; fmt.Sprint(got) != "15000" {
		t.Fatalf("remesh budget %v, want the decimation's 15000", got)
	}
}

// hub fakes a paid model hub: one model that does shape and texture in one pass.
type hub struct {
	*fakeProvider
	price float64
}

func (h *hub) ID() string          { return "hub" }
func (h *hub) Kind() provider.Kind { return provider.KindAPI }
func (h *hub) Paid() bool          { return true }
func (h *hub) Info(context.Context) (*engineapi.Info, error) {
	return &engineapi.Info{Stages: map[string]string{}}, nil
}
func (h *hub) Models(context.Context) ([]engineapi.Model, error) {
	m := model("oneshot", "shape", 50)
	m.Tasks = []string{"shape", "texture"}
	m.Price = &engineapi.Price{Amount: h.price, Unit: "generation", Currency: "USD"}
	return []engineapi.Model{m}, nil
}

func withHub(t *testing.T, r *Runner, fp *fakeProvider, price float64) {
	t.Helper()
	r.providers.Add(&hub{fakeProvider: fp, price: price})
}

func stageIDs(j *library.Job) string {
	var ids []string
	for _, s := range j.Stages {
		ids = append(ids, s.ID)
	}
	return strings.Join(ids, ",")
}

func TestAOneShotModelSkipsTheTextureStageAndWhatRefinesIt(t *testing.T) {
	r, fp, _, _, img := setup(t)
	withHub(t, r, fp, 0.4)
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "forja.toml"), []byte(
		"[models.stages]\nshape = \"oneshot\"\n[routing.stages]\nshape = \"hub\"\n"), 0o644)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project,
		Overrides: map[string]any{"match.enabled": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageIDs(j); got != "bg,frame,shape,cleanup,lowpoly,bake,lods,export" {
		t.Fatalf("stages %s", got)
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	all, _ := r.Presets(project)
	plan, _ := r.Resolve(context.Background(), project, all["mobile-prop"], nil)
	match := false
	for _, p := range plan {
		if p.Stage == "paint" && (!p.Skipped || p.By != "shape") {
			t.Fatalf("paint pick %+v", p)
		}
		// Off by default, but reported, so the app does not offer to turn it on.
		match = match || p.Stage == "match" && p.Skipped
		if p.Stage == "shape" && (!p.Paid || p.Price == nil || p.Price.Amount != 0.4) {
			t.Fatalf("shape pick %+v", p)
		}
	}
	if !match {
		t.Fatalf("match is not reported skipped: %+v", plan)
	}
	spent, _ := r.db.Spent(context.Background(), time.Now().Add(-time.Hour))
	if spent != 0.4 {
		t.Fatalf("recorded spending %v", spent)
	}
}

func TestSpendingLimitsRefuseJobsThatWouldPassThem(t *testing.T) {
	r, fp, _, _, img := setup(t)
	withHub(t, r, fp, 0.4)
	project := t.TempDir()
	pins := "[models.stages]\nshape = \"oneshot\"\n[routing.stages]\nshape = \"hub\"\n"
	limit := func(budget string) {
		os.WriteFile(filepath.Join(project, "forja.toml"), []byte(pins+"[routing.budget_usd]\n"+budget+"\n"), 0o644)
	}
	// Each job runs its hosted shape stage, so each job pays.
	submit := func(seed int) error {
		j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project, Seed: &seed})
		if err == nil {
			wait(t, r, j.ID)
		}
		return err
	}
	limit("per_job = 0.3")
	if err := submit(1); err == nil || !strings.Contains(err.Error(), "per image") {
		t.Fatalf("per image: %v", err)
	}
	limit("per_day = 1.0")
	for seed := 1; seed <= 2; seed++ {
		if err := submit(seed); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
	if err := submit(3); err == nil || !strings.Contains(err.Error(), "daily limit") {
		t.Fatalf("daily: %v", err)
	}
}

func TestSweepsWaitForRunningJobsAndHearWhenTheLastEnds(t *testing.T) {
	r, fp, _, _, img := setup(t)
	idle := make(chan struct{}, 4)
	r.OnIdle = func() { idle <- struct{}{} }
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.hold = hold })
	ctx := context.Background()
	j1, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "mobile-prop"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		fp.mu.Lock()
		held := fp.held
		fp.mu.Unlock()
		if held == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the job never reached its shape")
		}
	}
	if r.WhenIdle(func() { t.Error("a sweep ran while a job built its shape") }) {
		t.Fatal("WhenIdle ran during a job")
	}
	close(hold)
	if got := wait(t, r, j1.ID); got.Status != library.Succeeded {
		t.Fatalf("job: %s %s", got.Status, got.Error)
	}
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("no word that the runner went idle")
	}
	ran := false
	if !r.WhenIdle(func() { ran = true }) || !ran {
		t.Fatal("WhenIdle did not run once the job ended")
	}
}

// runsSince lists the provider's runs after the first before, "|" between runs.
func (f *fakeProvider) runsSince(before int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var runs []string
	for _, ids := range f.runs[before:] {
		runs = append(runs, strings.Join(ids, ","))
	}
	return strings.Join(runs, "|")
}

func TestASampleMeasuresEveryStage(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	measure := func() Sample {
		t.Helper()
		job, done, err := r.Measure(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetKeep})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-done:
			if s.Job.ID != job.ID || s.Job.Status != library.Succeeded {
				t.Fatalf("sample %s: %s %s", s.Job.ID, s.Job.Status, s.Job.Error)
			}
			return s
		case <-time.After(5 * time.Second):
			t.Fatal("the sample never ended")
		}
		return Sample{}
	}
	before := fp.count()
	s := measure()
	// Background removal ran once, while looking for a sheet of views, and was reused after.
	if got := fp.runsSince(before); got != "bg,sheet|frame,shape,cleanup,export" {
		t.Fatalf("the sample ran %s", got)
	}
	if s.Job.AssetID != "" || s.Engine <= 0 || s.Overhead() <= 0 || s.Overhead() >= s.Wall {
		t.Fatalf("sample %+v, overhead %s", s, s.Overhead())
	}

	for deadline := time.Now().Add(5 * time.Second); !r.WhenIdle(func() {}); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the runner never went idle")
		}
	}
	release := r.Hold()
	if r.WhenIdle(func() {}) {
		t.Fatal("a sweep ran while a benchmark held the runner")
	}
	release()
	release()
	if !r.WhenIdle(func() {}) {
		t.Fatal("sweeps stayed away after the benchmark let go")
	}
}
