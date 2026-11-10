package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/provider"
)

// types lists the event types heard so far, with the data of the first job.removed.
func types(evs <-chan events.Event) (list []string, removed map[string]string) {
	for len(evs) > 0 {
		ev := <-evs
		list = append(list, ev.Type)
		if m, ok := ev.Data.(map[string]string); ok && ev.Type == EvJobRemoved && removed == nil {
			removed = m
		}
	}
	return list, removed
}

func TestRetryRunsAFailedJobAgainInItsPlace(t *testing.T) {
	r, _, hub, st, img := setup(t)
	local := newEngine(st, LocalProvider, provider.KindLocal, false)
	local.with(func(e *engine) { e.answer = &answeredErr{422} })
	r.providers.Add(local)
	ctx := context.Background()
	seed := 7
	old, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Name: "chest.png", Seed: &seed,
		Overrides: map[string]any{"shape.steps": 12}, Priority: "interactive"})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, r, old.ID); got.Status != library.Failed {
		t.Fatalf("the first run %s", got.Status)
	}
	local.with(func(e *engine) { e.answer = nil })
	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	again, err := r.Retry(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == old.ID || again.RetryOf != old.ID || again.CreatedAt.UnixMilli() != old.CreatedAt.UnixMilli() || again.Name != old.Name ||
		again.Preset != "draft" || again.Seed != 7 || again.Input != img || again.Priority != "interactive" ||
		fmt.Sprint(again.Overrides["shape.steps"]) != "12" || again.Status.Done() {
		t.Fatalf("the retry %+v", again)
	}
	heard, removed := types(evs)
	if len(heard) < 2 || heard[0] != EvJobRemoved || heard[1] != EvJobCreated || removed["id"] != old.ID ||
		removed["replaced_by"] != again.ID {
		t.Fatalf("events %v, removed %v", heard, removed)
	}
	if got := wait(t, r, again.ID); got.Status != library.Succeeded || got.AssetID == "" {
		t.Fatalf("the retry ended %s: %s", got.Status, got.Error)
	}
	if _, err := r.db.Job(ctx, old.ID); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("the job that was retried is still in the library: %v", err)
	}
	if list, _ := r.db.Jobs(ctx, 10, time.Now().Add(time.Minute)); len(list) != 1 || list[0].ID != again.ID {
		t.Fatalf("the list holds %d jobs, want the retry alone", len(list))
	}
	// Only a job that failed or was cancelled has something to retry.
	if _, err := r.Retry(ctx, again.ID); !errors.Is(err, ErrNotRetryable) {
		t.Fatalf("retrying a job that succeeded: %v", err)
	}
	if _, err := r.Retry(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("retrying an unknown job: %v", err)
	}
}

func TestRetryKeepsTheAnswerAboutTheSheetAndRefusesAJobThatStillRuns(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.sheet, f.hold = sheetFound, hold })
	ctx := context.Background()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetAsk})
	if err != nil {
		t.Fatal(err)
	}
	waiting(t, r, j.ID)
	if _, err := r.Retry(ctx, j.ID); !errors.Is(err, library.ErrActive) {
		t.Fatalf("retrying a waiting job: %v", err)
	}
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Keep: true}); err != nil {
		t.Fatal(err)
	}
	holding(t, fp, 1)
	if _, err := r.Retry(ctx, j.ID); !errors.Is(err, library.ErrActive) {
		t.Fatalf("retrying a running job: %v", err)
	}
	r.Cancel(j.ID)
	if got := wait(t, r, j.ID); got.Status != library.Cancelled || got.Error != "" {
		t.Fatalf("cancelled job: %s %q (a cancel is not an error)", got.Status, got.Error)
	}
	close(hold)
	again, err := r.Retry(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	// It looks at the sheet again, and does not ask again: the answer was to keep it whole.
	got := wait(t, r, again.ID)
	if got.Status != library.Succeeded || got.Sheet == nil || got.Sheet.Mode != library.SheetAsk || got.Sheet.Answer != library.SheetKeep {
		t.Fatalf("%s %q, sheet %+v", got.Status, got.Error, got.Sheet)
	}
}

func TestFinishedJobsLeaveTheListOneByOneOrTogether(t *testing.T) {
	r, fp, hub, st, img := setup(t)
	ctx := context.Background()
	made, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	made = wait(t, r, made.ID)
	asset, err := r.db.Asset(ctx, made.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.hold = hold })
	var cancelled []string
	for seed := 1; seed <= 2; seed++ {
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
		if err != nil {
			t.Fatal(err)
		}
		cancelled = append(cancelled, j.ID)
	}
	holding(t, fp, 2)
	if err := r.Remove(ctx, cancelled[0]); !errors.Is(err, library.ErrActive) {
		t.Fatalf("removing a running job: %v", err)
	}
	for _, id := range cancelled {
		r.Cancel(id)
		wait(t, r, id)
	}
	seed := 3
	running, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	holding(t, fp, 3)
	defer close(hold)

	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	if err := r.Remove(ctx, made.ID); err != nil {
		t.Fatal(err)
	}
	if heard, removed := types(evs); len(heard) != 1 || heard[0] != EvJobRemoved || removed["id"] != made.ID {
		t.Fatalf("events %v %v", heard, removed)
	}
	// The asset it made is untouched and still finds its job.
	if got, err := r.Job(ctx, made.ID); err != nil || !got.Hidden || len(got.Stages) == 0 {
		t.Fatalf("the job behind the asset: %+v, %v", got, err)
	}
	if a, err := r.db.Asset(ctx, asset.ID); err != nil || !st.Has(a.GLB) {
		t.Fatalf("the asset of a removed job: %v, %v", a, err)
	}
	if err := r.Remove(ctx, "nope"); !errors.Is(err, library.ErrNotFound) {
		t.Fatalf("removing an unknown job: %v", err)
	}
	removed, err := r.Clear(ctx)
	if err != nil || len(removed) != 2 {
		t.Fatalf("cleared %v, %v; want the two cancelled jobs", removed, err)
	}
	list, _ := r.db.Jobs(ctx, 10, time.Now().Add(time.Minute))
	if len(list) != 1 || list[0].ID != running.ID {
		t.Fatalf("%d jobs left on the list, want the running one", len(list))
	}
	if again, err := r.Clear(ctx); err != nil || again == nil || len(again) != 0 {
		t.Fatalf("clearing an already clear list: %v, %v", again, err)
	}
}

// After a restart every job that had only waited for its turn showed as failed.
func TestJobsThatOnlyWaitedForTheirTurnRunAfterARestart(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.hold = hold })
	ctx := context.Background()
	var ids []string
	for seed := 1; seed <= 5; seed++ { // 4 slots: the fifth waits
		j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, j.ID)
		if seed == 4 {
			holding(t, fp, 4)
		}
	}
	// A sample of a benchmark waits too: nobody will measure it after the restart.
	_, sample, err := r.Measure(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if j, _ := r.db.Job(ctx, ids[4]); j != nil && j.Status == library.Queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the fifth job is not saved as queued")
		}
	}
	r.Close(5 * time.Second)
	benchJob := (<-sample).Job.ID
	for _, id := range ids[:4] {
		if j, _ := r.db.Job(ctx, id); j.Status != library.Failed || j.Error != library.Interrupted {
			t.Fatalf("a job that was running is %s %q", j.Status, j.Error)
		}
	}
	if j, _ := r.db.Job(ctx, ids[4]); j.Status != library.Queued || j.Error != "" {
		t.Fatalf("the job that waited is %s %q after the core stopped", j.Status, j.Error)
	}

	// The next core, on the same library.
	close(hold)
	next := NewRunner(r.db, r.store, events.NewHub(), r.providers, r.catalog, r.config,
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test", 4)
	if err := r.db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	next.Resume(ctx)
	got := wait(t, next, ids[4])
	if got.Status != library.Succeeded || got.AssetID == "" {
		t.Fatalf("the job that waited ended %s: %s", got.Status, got.Error)
	}
	if j, _ := next.Job(ctx, benchJob); j.Status != library.Failed || j.Error != library.Interrupted {
		t.Fatalf("the sample of the benchmark is %s %q", j.Status, j.Error)
	}
	// A job interrupted while it ran can be run again.
	again, err := next.Retry(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(t, next, again.ID); got.Status != library.Succeeded {
		t.Fatalf("the retry of an interrupted job ended %s: %s", got.Status, got.Error)
	}
}

// POST /v1/jobs with [ok, missing, ok] answered 422 after the first job was already running.
func TestOneBadImageRefusesTheWholeBatchBeforeAnyJobIsCreated(t *testing.T) {
	r, _, hub, _, _ := setup(t)
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	const png = "\x89PNG\r\n\x1a\n"
	ok1, ok2 := write("chest.png", png+"chest"), write("barrel.png", png+"barrel")
	ctx := context.Background()
	for _, tc := range []struct{ name, path, want string }{
		{"missing", filepath.Join(dir, "gone.png"), "gone.png was not found"},
		{"empty", write("empty.png", ""), "empty.png is empty (0 bytes)"},
		{"not an image", write("notes.png", "shopping list\nmilk\n"), "notes.png is not an image (it looks like text/plain)"},
		{"a folder", dir, "is a folder, not an image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, unsubscribe := hub.Subscribe()
			defer unsubscribe()
			reqs := []SubmitRequest{{Path: ok1, Preset: "draft"}, {Path: tc.path, Preset: "draft"}, {Path: ok2, Preset: "draft"}}
			created, err := r.SubmitAll(ctx, reqs)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(created) != 0 {
				t.Fatalf("created %d jobs, err %v; want none and %q", len(created), err, tc.want)
			}
			if list, _ := r.db.Jobs(ctx, 10, time.Now().Add(time.Minute)); len(list) != 0 || len(evs) != 0 {
				t.Fatalf("%d jobs and %d events came of a refused batch", len(list), len(evs))
			}
			// The same goes for a single job, and for a view.
			if _, err := r.Submit(ctx, reqs[1]); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("alone: %v", err)
			}
			if _, err := r.Submit(ctx, SubmitRequest{Path: ok1, Preset: "draft", Views: map[string]string{"back": tc.path}}); err == nil ||
				!strings.Contains(err.Error(), "the back view: ") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("as a view: %v", err)
			}
		})
	}
	created, err := r.SubmitAll(ctx, []SubmitRequest{{Path: ok1, Preset: "draft"}, {Path: ok2, Preset: "draft"}})
	if err != nil || len(created) != 2 {
		t.Fatalf("a good batch: %d jobs, %v", len(created), err)
	}
	for _, j := range created {
		if got := wait(t, r, j.ID); got.Status != library.Succeeded {
			t.Fatalf("%s: %s", got.Status, got.Error)
		}
	}
}

// "Finished in 4.9 s" added up the stages of a job that took 13 s; and the time a job spends
// in the queue is not time it ran.
func TestAJobSaysWhenItStartedAndHowLongItTook(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.hold = hold })
	ctx := context.Background()
	last, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	if last.StartedAt != nil || last.DurationSec != 0 {
		t.Fatalf("a job that was only submitted started at %v", last.StartedAt)
	}
	holding(t, fp, 1)
	time.Sleep(200 * time.Millisecond) // time between its stages, which no stage counts
	if j, _ := r.Job(ctx, last.ID); j.StartedAt == nil || j.DurationSec != 0 {
		t.Fatalf("a running job: started %v, took %v s", j.StartedAt, j.DurationSec)
	}
	close(hold)
	got := wait(t, r, last.ID)
	if got.StartedAt == nil || got.FinishedAt == nil || got.StartedAt.Before(got.CreatedAt.Add(-time.Millisecond)) {
		t.Fatalf("created %v, started %v, finished %v", got.CreatedAt, got.StartedAt, got.FinishedAt)
	}
	stages := 0.0
	for _, s := range got.Stages {
		stages += s.Seconds
	}
	if got.DurationSec < 0.2 || stages >= 0.2 {
		t.Fatalf("the job took %v s by the clock and its stages add up to %v s", got.DurationSec, stages)
	}
	if want := got.FinishedAt.Sub(*got.StartedAt).Seconds(); got.DurationSec != want || got.DurationSec <= 0 {
		t.Fatalf("duration %v s, want the %v s from start to finish", got.DurationSec, want)
	}
	// The same from the library, as a list read after a restart shows it.
	saved, _ := r.db.Job(ctx, last.ID)
	if saved.StartedAt == nil || saved.DurationSec <= 0 {
		t.Fatalf("saved: started %v, %v s", saved.StartedAt, saved.DurationSec)
	}
}

func TestAnErrorIsOneLineForPeopleWithTheRestKeptAsItsDetail(t *testing.T) {
	traceback := "Traceback (most recent call last):\n  File \"/opt/forja/envs/hunyuan/lib/site-packages/torch/nn/modules/module.py\", line 1553, in _call_impl\n" +
		"    return forward_call(*args, **kwargs)\nRuntimeError: mat1 and mat2 shapes cannot be multiplied (1x1024 and 768x320)\n"
	if got := brief(traceback); got != "RuntimeError: mat1 and mat2 shapes cannot be multiplied (1x1024 and 768x320)" {
		t.Fatalf("a traceback in brief: %q", got)
	}
	if got := brief("the image has no subject\nsecond line"); got != "the image has no subject" {
		t.Fatalf("a message in brief: %q", got)
	}
	if got := brief(strings.Repeat("x", 500)); len([]rune(got)) != 301 {
		t.Fatalf("a long line is cut to %d characters", len([]rune(got)))
	}
	if detail("same", " same\n") != "" || detail("short", traceback) != traceback {
		t.Fatal("detail is the raw text only when it says more")
	}
}

// The app asks about a sheet and the user answers "use the image whole". The job then starts
// over with the stage its look began with, under the id the look sent it with: the engine,
// which answers a repeated id with the run it has, handed back the look, and the job ended
// with "the last stage produced no output".
func TestASheetKeptWholeAfterTheQuestionRunsTheWholeJob(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) { f.sheet = sheetFound })
	ctx := context.Background()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetAsk})
	if err != nil {
		t.Fatal(err)
	}
	waiting(t, r, j.ID)
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Keep: true}); err != nil {
		t.Fatal(err)
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded || got.AssetID == "" {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
}

func TestAFolderExpandsToItsImagesByName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.png", "A.png", "z.webp", "c.JPEG", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "deep.png"), []byte("x"), 0o644)
	single := filepath.Join(t.TempDir(), "one.png")
	got, err := ExpandFolders([]string{single, dir})
	if err != nil {
		t.Fatal(err)
	}
	var base []string
	for _, p := range got {
		base = append(base, filepath.Base(p))
	}
	if want := "one.png A.png b.png c.JPEG z.webp"; strings.Join(base, " ") != want {
		t.Fatalf("got %s, want %s", strings.Join(base, " "), want)
	}
}
