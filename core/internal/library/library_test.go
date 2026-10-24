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
