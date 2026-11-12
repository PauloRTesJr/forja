package library

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"forja/internal/materials"
)

// A library made before roughness and metallic could be set per material gains the column, its
// assets read no choices, and pointing an asset at another GLB changes only the GLB, the stats
// and the choices.
func TestOldLibrariesGainTheMaterialsColumnAndAnAssetCanPointAtAnotherGLB(t *testing.T) {
	dir := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "library.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE assets (id TEXT PRIMARY KEY, job_id TEXT NOT NULL, name TEXT NOT NULL,
		preset TEXT NOT NULL, glb TEXT NOT NULL, input TEXT NOT NULL, thumbnail TEXT NOT NULL DEFAULT '',
		stats TEXT NOT NULL DEFAULT '{}', project TEXT NOT NULL DEFAULT '', export_path TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL);
		INSERT INTO assets (id, job_id, name, preset, glb, input, created_at) VALUES ('old', 'j', 'chest', 'draft', 'blake3:aa', 'blake3:bb', 1);`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if old, err := db.Asset(ctx, "old"); err != nil || len(old.Materials) != 0 {
		t.Fatalf("old asset %+v, %v", old, err)
	}
	choices := []materials.Choice{{Index: 0, Roughness: 0.3, Metallic: 1}, {Index: 2, Roughness: 0.9}}
	got, err := db.SetAssetGLB(ctx, "old", "blake3:cc", map[string]any{"triangles": 12}, choices)
	if err != nil || got.GLB != "blake3:cc" || len(got.Materials) != 2 || got.Materials[0] != choices[0] || got.Stats["triangles"] != 12.0 {
		t.Fatalf("asset %+v, %v", got, err)
	}
	// A rename made from an older copy of the asset does not undo it.
	stale := *got
	stale.GLB, stale.Materials, stale.Name = "blake3:aa", nil, "crate"
	if err := db.SaveAsset(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if now, _ := db.Asset(ctx, "old"); now.GLB != "blake3:cc" || len(now.Materials) != 2 || now.Name != "crate" {
		t.Fatalf("after a stale save: %+v", now)
	}
	if _, err := db.SetAssetGLB(ctx, "nope", "blake3:cc", nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
}
