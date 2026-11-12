package library

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A library made before jobs had views, a sheet and a seed.
const oldJobs = `CREATE TABLE jobs (
	id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, finished_at INTEGER, status TEXT NOT NULL,
	name TEXT NOT NULL, preset TEXT NOT NULL, input TEXT NOT NULL, project TEXT NOT NULL DEFAULT '',
	priority TEXT NOT NULL DEFAULT 'batch', overrides TEXT NOT NULL DEFAULT '{}',
	stages TEXT NOT NULL DEFAULT '[]', asset_id TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '');
INSERT INTO jobs (id, created_at, status, name, preset, input) VALUES ('old', 1, 'succeeded', 'chest', 'draft', 'blake3:aa');`

func TestOldLibrariesGainTheNewColumnsAndWaitingJobsSurviveRestarts(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "library.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldJobs); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old, err := db.Job(ctx, "old")
	if err != nil || old.Seed != 42 || len(old.Views) != 0 || old.Sheet != nil {
		t.Fatalf("old job %+v, %v", old, err)
	}

	j := &Job{ID: "sheet", Name: "creature", Preset: "draft", Input: "blake3:bb", Seed: 7, Status: Waiting,
		CreatedAt: time.Now(), Views: map[string]string{"left": "blake3:cc"},
		Sheet: &Sheet{Mode: SheetAsk, Checked: true, Found: true, Size: [2]int{1200, 1300},
			Parts: []SheetPart{{Box: [4]int{1, 2, 3, 4}, Kind: "figure", View: "front"}}}}
	if err := db.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := db.Job(ctx, "sheet")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Waiting || got.Seed != 7 || got.Views["left"] != "blake3:cc" || got.Sheet.Parts[0].View != "front" ||
		got.Sheet.Size != [2]int{1200, 1300} {
		t.Fatalf("after a restart %+v %+v", got, got.Sheet)
	}
	if db.Close() != nil {
		t.Fatal("close")
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("opening a migrated library again: %v", err)
	}
	again.Close()
}

func TestDeletingAnAssetRemovesTheJobThatMadeItAndNothingElse(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, id := range []string{"j1", "j2"} {
		if err := db.SaveJob(ctx, &Job{ID: id, Name: "chest", Preset: "draft", Input: "blake3:aa", Status: Succeeded,
			CreatedAt: time.Now(), AssetID: "a" + id[1:]}); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveAsset(ctx, &Asset{ID: "a" + id[1:], JobID: id, Name: "chest", Preset: "draft", GLB: "blake3:bb",
			CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	gone, err := db.DeleteAsset(ctx, "a1")
	if err != nil || gone.JobID != "j1" {
		t.Fatalf("deleted %+v, %v", gone, err)
	}
	if _, err := db.Asset(ctx, "a1"); err != ErrNotFound {
		t.Fatalf("the asset is still there: %v", err)
	}
	if _, err := db.Job(ctx, "j1"); err != ErrNotFound {
		t.Fatalf("its job is still there: %v", err)
	}
	if _, err := db.Asset(ctx, "a2"); err != nil {
		t.Fatalf("another asset went too: %v", err)
	}
	if _, err := db.Job(ctx, "j2"); err != nil {
		t.Fatalf("another job went too: %v", err)
	}
	if _, err := db.DeleteAsset(ctx, "a1"); err != ErrNotFound {
		t.Fatalf("deleting it twice: %v", err)
	}
}

// A job the core left running showed as failed with a stage still running: a spinner that
// never stops on a job that ended.
func TestAnInterruptedJobEndsWithEveryStageSettled(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, j := range []*Job{
		{ID: "running", Status: Running, Stages: []Stage{{ID: "bg", Status: Succeeded}, {ID: "shape", Status: Running},
			{ID: "export", Status: Queued}}},
		{ID: "queued", Status: Queued, Stages: []Stage{{ID: "bg", Status: Queued}}},
		{ID: "done", Status: Succeeded, Stages: []Stage{{ID: "bg", Status: Succeeded}}},
	} {
		j.Name, j.Preset, j.Input, j.CreatedAt = "chest", "draft", "blake3:aa", time.Now()
		if err := db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"running"} {
		j, err := db.Job(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Status != Failed || j.Error != Interrupted || j.FinishedAt == nil {
			t.Fatalf("%s: %s %q finished %v", id, j.Status, j.Error, j.FinishedAt)
		}
		for _, s := range j.Stages {
			if !s.Status.Done() {
				t.Fatalf("%s: stage %s is still %s on a job that ended", id, s.ID, s.Status)
			}
		}
	}
	if j, _ := db.Job(ctx, "running"); j.Stages[0].Status != Succeeded {
		t.Fatalf("the stage that succeeded shows as %s", j.Stages[0].Status)
	}
	// A job that only waited for its turn waits on: the runner starts it again.
	if j, _ := db.Job(ctx, "queued"); j.Status != Queued || j.Error != "" || j.FinishedAt != nil {
		t.Fatalf("the queued job is %s %q", j.Status, j.Error)
	}
	if queued, err := db.Queued(ctx); err != nil || len(queued) != 1 || queued[0].ID != "queued" {
		t.Fatalf("queued jobs %v, %v", queued, err)
	}
	if j, _ := db.Job(ctx, "done"); j.Status != Succeeded || j.Error != "" {
		t.Fatalf("a finished job changed: %s %q", j.Status, j.Error)
	}
}

// Jobs dropped together are created within one millisecond; without a second sort key their
// order in the queue changed from one listing to the next.
func TestJobsAndAssetsMadeInTheSameMillisecondListInOneOrder(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := time.UnixMilli(1_700_000_000_000)
	// Saved out of order, and then touched, which reorders rows that have no order of their own.
	for _, id := range []string{"01C", "01A", "01E", "01B", "01D"} {
		if err := db.SaveJob(ctx, &Job{ID: id, Name: "chest", Preset: "draft", Input: "blake3:aa", Status: Queued, CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := db.SaveAsset(ctx, &Asset{ID: id, JobID: id, Name: "chest", Preset: "draft", GLB: "blake3:bb", CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := db.Jobs(ctx, 10, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := db.Assets(ctx, 10, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var gotJobs, gotAssets string
	for _, j := range jobs {
		gotJobs += j.ID + " "
	}
	for _, a := range assets {
		gotAssets += a.ID + " "
	}
	const want = "01E 01D 01C 01B 01A " // newest first: ULIDs made later sort later
	if gotJobs != want || gotAssets != want {
		t.Fatalf("jobs %q, assets %q, want %q", gotJobs, gotAssets, want)
	}
}

func TestAFinishedJobLeavesTheListButItsAssetKeepsReadingIt(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := time.Now()
	for _, j := range []*Job{
		{ID: "made", Status: Succeeded, AssetID: "a1"},
		{ID: "failed", Status: Failed},
		{ID: "running", Status: Running},
		{ID: "waiting", Status: Waiting},
	} {
		j.Name, j.Preset, j.Input, j.CreatedAt = "chest", "draft", "blake3:aa", at
		if err := db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SaveAsset(ctx, &Asset{ID: "a1", JobID: "made", Name: "chest", Preset: "draft", GLB: "blake3:bb", CreatedAt: at}); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]error{"made": nil, "failed": nil, "running": ErrActive, "waiting": ErrActive, "nope": ErrNotFound} {
		if err := db.RemoveJob(ctx, id); err != want {
			t.Fatalf("removing %s: %v, want %v", id, err, want)
		}
	}
	list, err := db.Jobs(ctx, 10, at.Add(time.Minute))
	if err != nil || len(list) != 2 {
		t.Fatalf("the list holds %d jobs, want the running and the waiting one: %v", len(list), err)
	}
	// The asset's page reads its job's stages.
	made, err := db.Job(ctx, "made")
	if err != nil || !made.Hidden || made.Status != Succeeded {
		t.Fatalf("the job behind an asset: %+v, %v", made, err)
	}
	if _, err := db.Job(ctx, "failed"); err != ErrNotFound {
		t.Fatalf("a failed job that was removed is still in the library: %v", err)
	}
	// Saving the hidden job again (its asset's thumbnail arrives) keeps it hidden.
	if err := db.SaveJob(ctx, made); err != nil {
		t.Fatal(err)
	}
	if list, _ := db.Jobs(ctx, 10, at.Add(time.Minute)); len(list) != 2 {
		t.Fatalf("a hidden job came back to the list: %d jobs", len(list))
	}
	if finished, _ := db.Finished(ctx); len(finished) != 0 {
		t.Fatalf("%d finished jobs are left on the list", len(finished))
	}
}

func TestAJobKeepsWhenItStartedAndWhatItRetried(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	created := time.UnixMilli(1_700_000_000_000)
	started, finished := created.Add(8*time.Second), created.Add(13*time.Second)
	j := &Job{ID: "j", Name: "chest", Preset: "draft", Input: "blake3:aa", Status: Failed, CreatedAt: created,
		StartedAt: &started, FinishedAt: &finished, RetryOf: "old", Error: "This computer ran out of memory.",
		ErrorDetail: "Traceback (most recent call last):\n  ...\ntorch.OutOfMemoryError", Sample: true,
		Stages: []Stage{{ID: "shape", Status: Failed, Error: "torch.OutOfMemoryError", ErrorDetail: "Traceback..."}}}
	if err := db.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, err := db.Job(ctx, "j")
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) || got.DurationSec != 5 || got.RetryOf != "old" || !got.Sample ||
		got.ErrorDetail != j.ErrorDetail || got.Stages[0].ErrorDetail != "Traceback..." {
		t.Fatalf("%+v", got)
	}
}

// A library made before the count of restarts that ran a job again gains its column, and its
// jobs read as never resumed.
func TestOldLibrariesGainTheResumesColumnAndTheirJobsReadZero(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "library.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(oldJobs); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name = 'resumes'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the resumes column: %d, %v", n, err)
	}
	if old, err := db.Job(ctx, "old"); err != nil || old.Resumes != 0 {
		t.Fatalf("old job %+v, %v", old, err)
	}
	// The count survives being saved and read back, and a plain save keeps it.
	j := &Job{ID: "again", Name: "chest", Preset: "draft", Input: "blake3:aa", Status: Queued, CreatedAt: time.Now(), Resumes: 1,
		Stages: []Stage{{ID: "bg", Status: Queued}}}
	if err := db.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Job(ctx, "again"); got.Resumes != 1 {
		t.Fatalf("resumes %d", got.Resumes)
	}
}

// A job saved as running whose stages all still wait was admitted, not run: it is queued again
// once, and failed the next time.
func TestAJobAdmittedButNotStartedIsQueuedAgainOnceByFailUnfinished(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	at := time.Now()
	for _, j := range []*Job{
		{ID: "admitted", Status: Running, Stages: []Stage{{ID: "bg", Status: Queued}, {ID: "shape", Status: Queued}}},
		{ID: "begun", Status: Running, Stages: []Stage{{ID: "bg", Status: Running}, {ID: "shape", Status: Queued}}},
		{ID: "looked", Status: Running, Sheet: &Sheet{Mode: SheetKeep, Checked: true}, Stages: []Stage{{ID: "bg", Status: Queued}}},
		{ID: "sample", Status: Running, Sample: true, Stages: []Stage{{ID: "bg", Status: Queued}}},
	} {
		j.Name, j.Preset, j.Input, j.CreatedAt, j.StartedAt = "chest", "draft", "blake3:aa", at, &at
		if err := db.SaveJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := db.Job(ctx, "admitted"); j.Status != Queued || j.Error != "" || j.StartedAt != nil || j.Resumes != 1 {
		t.Fatalf("admitted: %s %q, started %v, resumed %d", j.Status, j.Error, j.StartedAt, j.Resumes)
	}
	for _, id := range []string{"begun", "looked", "sample"} {
		if j, _ := db.Job(ctx, id); j.Status != Failed || j.Error != Interrupted {
			t.Fatalf("%s: %s %q", id, j.Status, j.Error)
		}
	}
	// Admitted again, and found so again.
	j, _ := db.Job(ctx, "admitted")
	j.Status = Running
	if err := db.SaveJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := db.FailUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	if j, _ := db.Job(ctx, "admitted"); j.Status != Failed || j.Error != Interrupted || j.Resumes != 1 {
		t.Fatalf("admitted twice: %s %q, resumed %d", j.Status, j.Error, j.Resumes)
	}
}

// A stage's measures about its outputs come back merged, and a stage that measured nothing has none.
func TestAStageMergesTheMetricsOfItsOutputs(t *testing.T) {
	s := Stage{Outputs: []Output{
		{Hash: "a", Metrics: map[string]float64{"match_iou": 0.9, "uv_coverage": 0.5}},
		{Hash: "b"},
		{Hash: "c", Metrics: map[string]float64{"uv_coverage": 0.7}},
	}}
	got := s.Metrics()
	if len(got) != 2 || got["match_iou"] != 0.9 || got["uv_coverage"] != 0.7 {
		t.Fatalf("merged %v", got)
	}
	if (&Stage{Outputs: []Output{{Hash: "a"}}}).Metrics() != nil || (&Stage{}).Metrics() != nil {
		t.Fatal("a stage that measured nothing has no metrics")
	}
}
