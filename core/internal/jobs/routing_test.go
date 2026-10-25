package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/provider"
	"forja/internal/store"
)

// engine is a fake provider besides this computer: another computer, a rented GPU or a paid
// API. It runs stages like fakeProvider and can be told to fail.
type engine struct {
	*fakeProvider
	id   string
	kind provider.Kind
	paid bool

	mu sync.Mutex
	// refuse fails runs as if the machine went off; oom runs stages of these types out of
	// memory; slow holds runs until they are cancelled; billed reports a failed stage billed.
	refuse bool
	oom    map[string]bool
	slow   bool
	billed bool
	ran    []string
}

func newEngine(st *store.Store, id string, kind provider.Kind, paid bool) *engine {
	builtins := map[string]string{}
	for _, use := range []string{"framing", "mesh.cleanup", "mesh.decimate", "mesh.uv", "texture.match", "bake", "mesh.lods",
		"export", "views.detect", "views.split"} {
		builtins[use] = "1"
	}
	return &engine{fakeProvider: &fakeProvider{st: st, stages: builtins, shape: model("stub-shape", "shape", 1), drop: map[string]bool{}},
		id: id, kind: kind, paid: paid, oom: map[string]bool{}}
}

func (e *engine) ID() string          { return e.id }
func (e *engine) Kind() provider.Kind { return e.kind }
func (e *engine) Paid() bool          { return e.paid }

func (e *engine) with(fn func(e *engine)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e)
}

func (e *engine) stagesRun() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.ran, ",")
}

func (e *engine) Run(ctx context.Context, req engineapi.RunRequest, evs chan<- engineapi.Event) (*engineapi.Run, error) {
	e.mu.Lock()
	refuse, slow, billed := e.refuse, e.slow, e.billed
	oom := -1
	for i, s := range req.Stages {
		if oom < 0 && e.oom[s.Use] {
			oom = i
		}
	}
	e.mu.Unlock()
	if refuse {
		return nil, errors.New("dial tcp 192.0.2.10:7861: connectex: No connection could be made because the target machine actively refused it")
	}
	if slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if oom < 0 {
		e.mu.Lock()
		for _, s := range req.Stages {
			e.ran = append(e.ran, s.ID)
		}
		e.mu.Unlock()
		return e.fakeProvider.Run(ctx, req, evs)
	}
	run := &engineapi.Run{ID: "r", Stages: map[string]engineapi.StageState{}}
	if oom > 0 {
		prefix := req
		prefix.Stages = req.Stages[:oom]
		done, err := e.fakeProvider.Run(ctx, prefix, evs)
		if err != nil {
			return nil, err
		}
		run = done
	}
	e.mu.Lock()
	for _, s := range req.Stages[:oom] {
		e.ran = append(e.ran, s.ID)
	}
	e.mu.Unlock()
	failed := req.Stages[oom]
	if billed {
		evs <- engineapi.Event{Type: "stage.billed", Data: map[string]any{"stage": failed.ID}}
	}
	msg := "CUDA out of memory. Tried to allocate 2.00 GiB"
	secs := 1.0
	run.Status, run.Error = "failed", &msg
	run.Stages[failed.ID] = engineapi.StageState{Status: "failed", Error: &msg, Seconds: &secs}
	return run, nil
}

// withProject writes a forja.toml and returns its folder.
func withProject(t *testing.T, toml string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forja.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func providers(j *library.Job) map[string]string {
	out := map[string]string{}
	for _, s := range j.Stages {
		out[s.ID] = s.Provider
	}
	return out
}

const studio = `
[[providers]]
id = "lan"
kind = "engine"
label = "Studio PC"
`

func TestAPinnedModelRunsWhereItIsReady(t *testing.T) {
	r, _, _, st, img := setup(t)
	lan := newEngine(st, "lan", provider.KindEngine, false)
	lan.set(func(f *fakeProvider) { f.extra = []engineapi.Model{model("paint-cuda", "texture", 9)} })
	r.providers.Add(lan)
	project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n[models.stages]\ntexture = \"paint-cuda\"\n"+studio)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if got := providers(j); got["paint"] != "lan" || got["shape"] != LocalProvider || got["bake"] != LocalProvider {
		t.Fatalf("routes %v", got)
	}
	if got := lan.stagesRun(); got != "paint" {
		t.Fatalf("the studio PC ran %q", got)
	}
}

// The M5 gate: a Mac whose engine lacks the CUDA-only painter routes texturing to a PC that has
// one, by itself, and says where it runs.
func TestAStageThisComputerCannotRunGoesToOneThatCan(t *testing.T) {
	r, fp, _, st, img := setup(t)
	fp.set(func(f *fakeProvider) { f.drop["stub-paint"] = true })
	lan := newEngine(st, "lan", provider.KindEngine, false)
	r.providers.Add(lan)
	project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n"+studio)
	all, _ := r.Presets(project)
	plan, err := r.Resolve(context.Background(), project, all["mobile-prop"], nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan {
		want := "This computer"
		if p.Stage == "paint" {
			want = "Studio PC"
		}
		if p.Stage != "match" && p.ProviderLabel != want {
			t.Fatalf("%s runs on %q, want %q", p.Stage, p.ProviderLabel, want)
		}
	}
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded || providers(got)["paint"] != "lan" {
		t.Fatalf("%s %s %v", got.Status, got.Error, providers(got))
	}
	if s := stage(got, "paint"); s.ProviderLabel != "Studio PC" {
		t.Fatalf("paint runs on %q", s.ProviderLabel)
	}
}

func TestBuiltInStagesGoWhereTheyAreBuiltIn(t *testing.T) {
	r, fp, _, _, img := setup(t)
	withHub(t, r, fp, 0.4)
	// The hub first: it has the shape model, but no built-in stages to frame or bake with.
	project := withProject(t, "[routing]\npriority = [\"hub\", \"local\"]\nallow_paid_fallback = true\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := providers(j)
	if got["shape"] != "hub" || got["frame"] != LocalProvider || got["bake"] != LocalProvider {
		t.Fatalf("routes %v", got)
	}
	if done := wait(t, r, j.ID); done.Status != library.Succeeded {
		t.Fatalf("%s: %s", done.Status, done.Error)
	}
}

func TestPoliciesOrderTheProvidersThatCanRunAStage(t *testing.T) {
	r, fp, _, st, img := setup(t)
	lan := newEngine(st, "lan", provider.KindEngine, false)
	r.providers.Add(lan)
	withHub(t, r, fp, 0.4)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = r.db.RecordTiming(ctx, "shape", "stub-shape", LocalProvider, 40)
		_ = r.db.RecordTiming(ctx, "shape", "stub-shape", "lan", 12)
	}
	shapeOn := func(routing string) string {
		t.Helper()
		project := withProject(t, "[routing]\n"+routing+"\n")
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Project: project})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, r, j.ID)
		return providers(j)["shape"]
	}
	if got := shapeOn(`priority = ["local", "lan"]`); got != LocalProvider {
		t.Fatalf("prefer-local put the shape on %s", got)
	}
	if got := shapeOn("priority = [\"local\", \"lan\"]\npolicy = \"fastest\""); got != "lan" {
		t.Fatalf("fastest put the shape on %s", got)
	}
	// With paid help allowed and the hub first in line, prefer-local follows the line; cheapest
	// takes the free computer.
	if got := shapeOn("priority = [\"hub\", \"local\"]\nallow_paid_fallback = true"); got != "hub" {
		t.Fatalf("prefer-local put the shape on %s", got)
	}
	if got := shapeOn("priority = [\"hub\", \"local\"]\nallow_paid_fallback = true\npolicy = \"cheapest\""); got != LocalProvider {
		t.Fatalf("cheapest put the shape on %s", got)
	}
}

func TestOfflineOnlyKeepsStagesOffARentedPod(t *testing.T) {
	r, _, _, st, img := setup(t)
	r.providers.Add(newEngine(st, "pod", provider.KindEngine, true))
	r.providers.Add(newEngine(st, "lan", provider.KindEngine, false))
	ctx := context.Background()
	project := withProject(t, "[routing]\npriority = [\"pod\", \"lan\", \"local\"]\npolicy = \"offline-only\"\nallow_paid_fallback = true\n")
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	wait(t, r, j.ID)
	// The pod is first in line and paid help is allowed, but it is someone else's cloud.
	if got := providers(j)["shape"]; got != "lan" {
		t.Fatalf("offline-only put the shape on %s", got)
	}
	routed := withProject(t, "[routing]\npolicy = \"offline-only\"\n[routing.stages]\nshape = \"pod\"\n")
	if _, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Project: routed}); err == nil {
		t.Fatal("offline-only accepted a stage routed to a rented pod")
	}
}

func fallbacks(j *library.Job, id string) []library.Fallback {
	if s := stage(j, id); s != nil {
		return s.Fallbacks
	}
	return nil
}

func TestAProviderThatCannotBeReachedHandsItsStagesOn(t *testing.T) {
	r, _, hub, st, img := setup(t)
	lan := newEngine(st, "lan", provider.KindEngine, false)
	lan.with(func(e *engine) { e.refuse = true })
	r.providers.Add(lan)
	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	project := withProject(t, "[routing]\npriority = [\"lan\", \"local\"]\n"+studio)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if providers(j)["shape"] != "lan" {
		t.Fatalf("routes %v", providers(j))
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	for id, p := range providers(got) {
		if p != LocalProvider {
			t.Fatalf("%s still on %s", id, p)
		}
	}
	if fb := fallbacks(got, "shape"); len(fb) != 1 || fb[0].From != "lan" || fb[0].To != LocalProvider || fb[0].Reason != "could not be reached" {
		t.Fatalf("fallbacks %+v", fb)
	}
	if !heard(evs, EvJobFallback) {
		t.Fatal("no job.fallback event")
	}
}

func heard(evs <-chan events.Event, typ string) bool {
	for {
		select {
		case ev := <-evs:
			if ev.Type == typ {
				return true
			}
		default:
			return false
		}
	}
}

func TestAStageThatRunsOutOfMemoryMovesAlone(t *testing.T) {
	r, _, _, st, img := setup(t)
	// This computer as an engine that can run out of memory, the studio PC beside it.
	local := newEngine(st, LocalProvider, provider.KindLocal, false)
	r.providers.Add(local)
	local.with(func(e *engine) { e.oom["texture"] = true })
	lan := newEngine(st, "lan", provider.KindEngine, false)
	r.providers.Add(lan)
	project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n"+studio)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if routes := providers(got); routes["paint"] != "lan" || routes["shape"] != LocalProvider || routes["bake"] != LocalProvider {
		t.Fatalf("routes %v", routes)
	}
	if fb := fallbacks(got, "paint"); len(fb) != 1 || fb[0].Reason != "ran out of memory" {
		t.Fatalf("fallbacks %+v", fb)
	}
	if got := lan.stagesRun(); got != "paint" {
		t.Fatalf("the studio PC ran %q", got)
	}
	// What ran before the painter is reused, not run again.
	if got := local.stagesRun(); got != "bg,sheet,frame,shape,cleanup,lowpoly,bake,lods,export" {
		t.Fatalf("this computer ran %q", got)
	}
	for _, s := range got.Stages {
		if s.Use == "shape" && s.Cached {
			t.Fatal("the shape this job made shows as cached")
		}
	}
}

func TestAFailureNeverEscalatesToAPaidProviderUnlessAllowed(t *testing.T) {
	r, _, _, st, img := setup(t)
	local := newEngine(st, LocalProvider, provider.KindLocal, false)
	local.with(func(e *engine) { e.oom["texture"] = true })
	r.providers.Add(local)
	cloud := newEngine(st, "cloud", provider.KindServerless, true)
	r.providers.Add(cloud)
	routing := "[routing]\npriority = [\"local\", \"cloud\"]\n"
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: withProject(t, routing)})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Failed || !strings.Contains(got.Error, "out of memory") || cloud.stagesRun() != "" {
		t.Fatalf("%s %q; the cloud ran %q", got.Status, got.Error, cloud.stagesRun())
	}
	j, err = r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop",
		Project: withProject(t, routing+"allow_paid_fallback = true\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Succeeded || providers(got)["paint"] != "cloud" {
		t.Fatalf("%s %q %v", got.Status, got.Error, providers(got))
	}
}

func TestAStageRoutedToOneProviderDoesNotFallBack(t *testing.T) {
	r, _, _, st, img := setup(t)
	lan := newEngine(st, "lan", provider.KindEngine, false)
	lan.with(func(e *engine) { e.refuse = true })
	r.providers.Add(lan)
	project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n[routing.stages]\nshape = \"lan\"\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Failed || !strings.Contains(got.Error, "refused") {
		t.Fatalf("%s %q", got.Status, got.Error)
	}
}

// rented is a paid provider billed by the second, as RunPod serverless is.
const rented = `
[[providers]]
id = "cloud"
kind = "serverless"
vendor = "runpod"
`

func TestASpendingLimitStopsAStageBilledByTheSecond(t *testing.T) {
	r, _, _, st, img := setup(t)
	tick := meterTick
	meterTick = 5 * time.Millisecond
	t.Cleanup(func() { meterTick = tick })
	cloud := newEngine(st, "cloud", provider.KindServerless, true)
	cloud.with(func(e *engine) { e.slow = true })
	r.providers.Add(cloud)
	// A dollar a second, so a twentieth of a dollar goes in a moment.
	project := withProject(t, "[routing.stages]\nshape = \"cloud\"\n[routing.budget_usd]\nper_job = 0.05\n"+rented+
		"price_usd_per_second = 1.0\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Failed || !strings.Contains(got.Error, "spending limit of $0.05 per image") {
		t.Fatalf("%s %q", got.Status, got.Error)
	}
	spent, _ := r.db.SpentOn(context.Background(), j.ID)
	if spent < 0.05 || spent > 1 {
		t.Fatalf("recorded %v for the time it ran", spent)
	}
}

func TestABatchIsMeteredAtTheRateOfTheStageRunning(t *testing.T) {
	r, _, _, _, _ := setup(t)
	project := withProject(t, "[routing.budget_usd]\nper_job = 5.0\n")
	charges := []charge{{Rate: 1, Overhead: 1}, {Rate: 1, Overhead: 1}, {Rate: 1.5, Overhead: 1}}
	release, err := r.admit(context.Background(), project, "j", charges, func(error) {})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	r.mu.Lock()
	defer r.mu.Unlock()
	for m := range r.meters {
		if m.rate != 1.5 {
			t.Fatalf("metered at $%v a second", m.rate)
		}
	}
}

func TestAPaidStageDoesNotStartPastTheDailyLimit(t *testing.T) {
	r, _, _, st, img := setup(t)
	cloud := newEngine(st, "cloud", provider.KindServerless, true)
	r.providers.Add(cloud)
	// Nothing measured yet, so the job passes when it is queued; spending today does not.
	_ = r.db.RecordCost(context.Background(), library.Cost{Job: "earlier", Stage: "shape", Provider: "cloud", Amount: 0.6, Currency: "USD"})
	project := withProject(t, "[routing.stages]\nshape = \"cloud\"\n[routing.budget_usd]\nper_day = 0.5\n"+rented)
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Failed || !strings.Contains(got.Error, "daily spending limit") || cloud.stagesRun() != "" {
		t.Fatalf("%s %q; ran %q", got.Status, got.Error, cloud.stagesRun())
	}
}

func TestQueuedJobsCountAgainstTheLimits(t *testing.T) {
	r, _, _, st, img := setup(t)
	cloud := newEngine(st, "cloud", provider.KindAPI, true)
	cloud.set(func(f *fakeProvider) {
		oneshot := model("oneshot", "shape", 50)
		oneshot.Tasks = []string{"shape", "texture"}
		oneshot.Price = &engineapi.Price{Amount: 0.4, Unit: engineapi.PerCall, Currency: "USD"}
		f.extra = []engineapi.Model{oneshot}
	})
	hold := make(chan struct{})
	cloud.set(func(f *fakeProvider) { f.hold = hold })
	r.providers.Add(cloud)
	defer close(hold)
	project := withProject(t, "[models.stages]\nshape = \"oneshot\"\n[routing.stages]\nshape = \"cloud\"\n[routing.budget_usd]\nper_day = 1.0\n")
	for seed := 1; seed <= 2; seed++ {
		if _, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project, Seed: &seed}); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
	}
	seed := 3
	_, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project, Seed: &seed})
	if err == nil || !strings.Contains(err.Error(), "already queued") {
		t.Fatalf("a third job while two are queued: %v", err)
	}
}

func TestAFailedStageTheProviderBilledIsRecorded(t *testing.T) {
	r, _, _, st, img := setup(t)
	cloud := newEngine(st, "cloud", provider.KindAPI, true)
	cloud.set(func(f *fakeProvider) {
		f.shape.Price = &engineapi.Price{Amount: 0.25, Unit: engineapi.PerCall, Currency: "USD"}
	})
	cloud.with(func(e *engine) { e.oom["shape"], e.billed = true, true })
	r.providers.Add(cloud)
	project := withProject(t, "[models.stages]\nshape = \"stub-shape\"\n[routing.stages]\nshape = \"cloud\"\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, j.ID); got.Status != library.Failed {
		t.Fatalf("%s %q", got.Status, got.Error)
	}
	if spent, _ := r.db.SpentOn(context.Background(), j.ID); spent != 0.25 {
		t.Fatalf("recorded %v", spent)
	}
}
