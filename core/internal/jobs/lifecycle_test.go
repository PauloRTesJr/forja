package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/provider"
)

// restarted is the core that starts next on the same library, store and providers.
func restarted(r *Runner) *Runner {
	return NewRunner(r.db, r.store, events.NewHub(), r.providers, r.catalog, r.config,
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test", 4)
}

// looksForViews is the run a job makes first, to find out whether its image holds several views.
func looksForViews(req engineapi.RunRequest) bool {
	for _, s := range req.Stages {
		if s.Use == "views.detect" {
			return true
		}
	}
	return false
}

// stalled makes the provider take on every run and answer none, as an engine that has not begun
// anything yet; it counts the runs it took. The runs end only when their job is cancelled.
func stalled(fp *fakeProvider) *atomic.Int32 {
	var entered atomic.Int32
	fp.set(func(f *fakeProvider) {
		f.gate = func(ctx context.Context, req engineapi.RunRequest, _ chan<- engineapi.Event) error {
			if looksForViews(req) {
				return nil
			}
			entered.Add(1)
			<-ctx.Done()
			return ctx.Err()
		}
	})
	return &entered
}

// admitted queues a batch as the app does when it is dropped: the first job begins a stage
// and the others are given a slot (the test runner has 4) that the engine does not answer.
// It returns the first job's id, then the others'. The runner is closed when the test ends.
func admitted(t *testing.T, r *Runner, fp *fakeProvider, img string, others int) (string, []string) {
	t.Helper()
	ctx := context.Background()
	t.Cleanup(func() { r.Close(time.Second) })
	fp.set(func(f *fakeProvider) {
		f.gate = func(ctx context.Context, req engineapi.RunRequest, evs chan<- engineapi.Event) error {
			if looksForViews(req) {
				return nil
			}
			evs <- engineapi.Event{Type: "stage.started", Data: map[string]any{"stage": req.Stages[0].ID}}
			<-ctx.Done()
			return ctx.Err()
		}
	})
	seed := 1
	first, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if j, _ := r.db.Job(ctx, first.ID); j != nil && j.Status == library.Running && !j.NotStarted() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the library does not know that the first job began a stage")
		}
	}
	entered := stalled(fp)
	var ids []string
	for i := range others {
		seed := i + 2
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		ready := int(entered.Load()) == others
		for _, id := range ids {
			if j, _ := r.db.Job(ctx, id); j == nil || j.Status != library.Running {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the other jobs are not saved as running with no stage begun (entered %d)", entered.Load())
		}
	}
	return first.ID, ids
}

// holding waits until n of the provider's runs are held at their shape stage.
func holding(t *testing.T, fp *fakeProvider, n int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		fp.mu.Lock()
		held := fp.held
		fp.mu.Unlock()
		if held == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d runs are held at their shape, want %d", held, n)
		}
	}
}

func settled(t *testing.T, j *library.Job) {
	t.Helper()
	for _, s := range j.Stages {
		if !s.Status.Done() {
			t.Fatalf("stage %s is still %s on a job that ended as %s", s.ID, s.Status, j.Status)
		}
	}
}

func TestCancellingARunningJobEndsItAsCancelled(t *testing.T) {
	r, fp, hub, _, img := setup(t)
	hold := make(chan struct{})
	defer close(hold)
	fp.set(func(f *fakeProvider) { f.hold = hold })
	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	holding(t, fp, 1)
	if !r.Cancel(j.ID) {
		t.Fatal("cancelling a running job was refused")
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Cancelled || got.Error != "" || got.FinishedAt == nil || got.AssetID != "" {
		t.Fatalf("%s %q, finished %v, asset %q", got.Status, got.Error, got.FinishedAt, got.AssetID)
	}
	settled(t, got)
	// What the library keeps is what clients were told last.
	saved, err := r.db.Job(context.Background(), j.ID)
	if err != nil || saved.Status != library.Cancelled {
		t.Fatalf("saved as %v, %v", saved, err)
	}
	last := ""
	for len(evs) > 0 {
		last = (<-evs).Type
	}
	if last != EvJobFinished {
		t.Fatalf("the last event of a cancelled job was %q", last)
	}
	if r.Cancel(j.ID) {
		t.Fatal("a job that ended was cancelled again")
	}
}

func TestAJobCancelledWhileItWaitsForASlotNeverRuns(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.hold = hold })
	ctx := context.Background()
	var ids []string
	for seed := 1; seed <= 5; seed++ { // the test runner has 4 slots
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		if seed == 4 {
			holding(t, fp, 4)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if j, _ := r.Job(ctx, ids[4]); j != nil && j.Status == library.Queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fifth job does not show as queued")
		}
	}
	if !r.Cancel(ids[4]) {
		t.Fatal("cancelling a queued job was refused")
	}
	got := wait(t, r, ids[4])
	if got.Status != library.Cancelled {
		t.Fatalf("%s %q", got.Status, got.Error)
	}
	settled(t, got)
	close(hold)
	for _, id := range ids[:4] {
		if j := wait(t, r, id); j.Status != library.Succeeded {
			t.Fatalf("a job that held a slot ended %s: %s", j.Status, j.Error)
		}
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if fp.held != 4 {
		t.Fatalf("%d jobs built a shape, want the 4 that were not cancelled", fp.held)
	}
}

// lateCancel is a provider whose run ends well although its job was cancelled meanwhile: the
// cancel reached it as it finished.
type lateCancel struct {
	*fakeProvider
	entered chan struct{}
	release chan struct{}
}

func (p *lateCancel) Run(_ context.Context, req engineapi.RunRequest, evs chan<- engineapi.Event) (*engineapi.Run, error) {
	p.entered <- struct{}{}
	<-p.release
	return p.fakeProvider.Run(context.Background(), req, evs)
}

// The app cancels a job while it looks for a sheet of views. The look ended well a moment
// later, a sheet was found, and the cancelled job went on to wait for an answer.
func TestAJobCancelledWhileItLooksForASheetDoesNotGoOnToWait(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) { f.sheet = sheetFound })
	late := &lateCancel{fakeProvider: fp, entered: make(chan struct{}, 1), release: make(chan struct{})}
	r.providers.Add(late)
	ctx := context.Background()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetAsk})
	if err != nil {
		t.Fatal(err)
	}
	<-late.entered
	if !r.Cancel(j.ID) {
		t.Fatal("cancel refused")
	}
	close(late.release)
	got := wait(t, r, j.ID)
	if got.Status != library.Cancelled {
		t.Fatalf("the cancelled job is %s", got.Status)
	}
	settled(t, got)
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Keep: true}); err == nil {
		t.Fatal("a cancelled job took an answer")
	}
}

// Closing the app while a job ran left it "running" until the next start, or, once its run
// was cut, "cancelled", as if the user had cancelled it.
func TestJobsRunningWhenTheCoreStopsEndAsInterrupted(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	defer close(hold)
	fp.set(func(f *fakeProvider) { f.hold = hold })
	ctx := context.Background()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	holding(t, fp, 1)
	closed := make(chan struct{})
	go func() { r.Close(5 * time.Second); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("closing waited for a job that was told to end")
	}
	// Saved by the time Close returns: the library closes next.
	got, err := r.db.Job(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != library.Failed || got.Error != library.Interrupted {
		t.Fatalf("%s %q", got.Status, got.Error)
	}
	settled(t, got)
	if _, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"}); err == nil {
		t.Fatal("a core that is closing took a job")
	}
}

// A failed job showed Go's own words for it, such as `provider lan: Post
// "https://192.0.2.10:7861/v1/runs": dial tcp 192.0.2.10:7861: connectex: No connection could
// be made because the target machine actively refused it.`
func TestAFailedJobSaysWhatHappenedWhereAndWhatToDo(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "https://192.0.2.10:7861/v1/runs", Err: &net.OpError{Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 7861},
		Err:  errors.New("connectex: No connection could be made because the target machine actively refused it.")}}
	for _, tc := range []struct {
		name   string
		break_ func(local, lan *engine)
		want   []string
	}{
		{"the engine crashed mid-run", func(local, _ *engine) { local.answer = provider.ErrRunLost },
			[]string{"This computer: the engine stopped while this job ran", "generate again."}},
		{"it ran out of memory", func(local, _ *engine) { local.oom["shape"] = true },
			[]string{"This computer ran out of memory: CUDA out of memory. Tried to allocate 2.00 GiB. Close other programs"}},
		{"the other computer is off", func(_, lan *engine) { lan.answer = refused },
			[]string{"Studio PC could not be reached: nothing answers at 192.0.2.10:7861. Check that it is running"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, st, img := setup(t)
			local := newEngine(st, LocalProvider, provider.KindLocal, false)
			lan := newEngine(st, "lan", provider.KindEngine, false)
			tc.break_(local, lan)
			r.providers.Add(local)
			r.providers.Add(lan)
			withUser(t, studio)
			// The shape is pinned to where it fails, so the failure is the job's.
			where := LocalProvider
			if lan.answer != nil {
				where = "lan"
			}
			project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n[routing.stages]\nshape = \""+where+"\"\n")
			j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
			if err != nil {
				t.Fatal(err)
			}
			got := wait(t, r, j.ID)
			if got.Status != library.Failed {
				t.Fatalf("job %s", got.Status)
			}
			for _, w := range tc.want {
				if !strings.Contains(got.Error, w) {
					t.Fatalf("want %q in:\n%s", w, got.Error)
				}
			}
			for _, raw := range []string{"provider local", "provider lan", "dial tcp", "connectex", "Post \""} {
				if strings.Contains(got.Error, raw) {
					t.Fatalf("the job shows the raw error (%q):\n%s", raw, got.Error)
				}
			}
			settled(t, got)
		})
	}
}

// An engine that crashes mid-run is a provider that dropped out: the stages go to the next
// place that can run them (docs/decisions.md, 23), as when it cannot be reached at all.
func TestARunTheEngineLostMovesToTheNextProvider(t *testing.T) {
	r, _, _, st, img := setup(t)
	local := newEngine(st, LocalProvider, provider.KindLocal, false)
	local.with(func(e *engine) { e.answer = provider.ErrRunLost })
	r.providers.Add(local)
	r.providers.Add(newEngine(st, "lan", provider.KindEngine, false))
	withUser(t, studio)
	project := withProject(t, "[routing]\npriority = [\"local\", \"lan\"]\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if fb := fallbacks(got, "shape"); len(fb) != 1 || fb[0].From != LocalProvider || fb[0].To != "lan" {
		t.Fatalf("fallbacks %+v", fb)
	}
}

// Seven images were queued and the core died a few seconds in: the runner admits several jobs
// at once, so six were saved as running before any of their stages began, and all six came
// back as "Forja closed while this job was running".
func TestJobsAdmittedButWithoutAStartedStageRunAfterARestart(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	started, others := admitted(t, r, fp, img, 3)

	// The core dies: the next one starts on the library as it was left.
	next := restarted(r)
	fp.set(func(f *fakeProvider) { f.gate = nil })
	if err := r.db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	next.Resume(ctx)

	got, err := r.db.Job(ctx, started)
	if err != nil || got.Status != library.Failed || got.Error != library.Interrupted {
		t.Fatalf("the job that began a stage is %v, %v", got, err)
	}
	settled(t, got)
	for _, id := range others {
		got := wait(t, next, id)
		if got.Status != library.Succeeded || got.AssetID == "" {
			t.Fatalf("a job that began nothing ended %s: %s", got.Status, got.Error)
		}
	}
}

// A job that takes the core down each time it is about to start must not be started for ever.
func TestAJobFoundAdmittedButNotStartedTwiceEndsAsInterrupted(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	_, others := admitted(t, r, fp, img, 2)

	if err := r.db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range others {
		if j, _ := r.db.Job(ctx, id); j.Status != library.Queued || j.Error != "" || j.StartedAt != nil {
			t.Fatalf("the first restart left a job %s %q", j.Status, j.Error)
		}
	}
	// The second core admits them again and dies before any stage begins.
	second := restarted(r)
	t.Cleanup(func() { second.Close(time.Second) })
	entered := stalled(fp)
	second.Resume(ctx)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		ready := int(entered.Load()) == len(others)
		for _, id := range others {
			if j, _ := r.db.Job(ctx, id); j.Status != library.Running {
				ready = false
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the second core did not admit the jobs")
		}
	}

	if err := r.db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range others {
		j, _ := r.db.Job(ctx, id)
		if j.Status != library.Failed || j.Error != library.Interrupted || j.FinishedAt == nil {
			t.Fatalf("a job found unstarted twice is %s %q", j.Status, j.Error)
		}
		settled(t, j)
	}
	if queued, err := r.db.Queued(ctx); err != nil || len(queued) != 0 {
		t.Fatalf("%d jobs would run again, %v", len(queued), err)
	}
}

// Quitting the app with a batch waiting and opening it again ran only the jobs that had not
// been given a slot yet; the ones that had were failed.
func TestJobsAdmittedButNotStartedStayQueuedWhenTheCoreIsStoppedOnPurpose(t *testing.T) {
	r, fp, _, _, img := setup(t)
	ctx := context.Background()
	started, others := admitted(t, r, fp, img, 2)

	r.Close(5 * time.Second)
	got, _ := r.db.Job(ctx, started)
	if got.Status != library.Failed || got.Error != library.Interrupted {
		t.Fatalf("the job that began a stage is %s %q", got.Status, got.Error)
	}
	for _, id := range others {
		j, _ := r.db.Job(ctx, id)
		if j.Status != library.Queued || j.Error != "" || j.FinishedAt != nil || j.StartedAt != nil || j.Resumes != 0 {
			t.Fatalf("a job that began nothing is %s %q (started %v, finished %v, resumed %d times)",
				j.Status, j.Error, j.StartedAt, j.FinishedAt, j.Resumes)
		}
		if !j.NotStarted() {
			t.Fatalf("a stage of it shows as begun: %+v", j.Stages)
		}
	}

	next := restarted(r)
	fp.set(func(f *fakeProvider) { f.gate = nil })
	if err := r.db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	next.Resume(ctx)
	for _, id := range others {
		if got := wait(t, next, id); got.Status != library.Succeeded {
			t.Fatalf("a job that waited ended %s: %s", got.Status, got.Error)
		}
	}
}
