package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"forja/internal/api"
	"forja/internal/cache"
	"forja/internal/export/fakeblender"
	"forja/internal/library"
	"forja/internal/store"
)

func TestMain(m *testing.M) {
	fakeblender.Main()
	os.Exit(m.Run())
}

func TestAssetsAreExportedOpenedInBlenderAndDeleted(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	h.ok("generate", img, "--preset", "draft", "--seed", "1")
	h.ok("generate", img, "--preset", "draft", "--seed", "2")
	var assets []*library.Asset
	h.okJSON(&assets, "assets", "ls")
	if len(assets) != 2 {
		t.Fatalf("assets %+v", assets)
	}
	first, second := assets[0].ID, assets[1].ID

	t.Setenv("FORJA_BLENDER__PATH", filepath.Join(h.dir, "no-blender"))
	if out := h.ok("assets", "formats"); !strings.Contains(out, "glb     .glb    yes") ||
		!strings.Contains(out, "needs Blender") || !strings.Contains(out, "no Blender at") {
		t.Fatalf("formats without Blender:\n%s", out)
	}
	if r := h.cli("", "assets", "export", first, "--format", "fbx"); r.code == 0 || !strings.Contains(r.err, "no Blender at") {
		t.Fatalf("FBX without Blender: exit %d, %s", r.code, r.err)
	}
	dest := filepath.Join(h.dir, "exports")
	out := h.ok("assets", "export", first, second, "-o", dest)
	if want := filepath.Join(dest, "chest.glb") + "\n" + filepath.Join(dest, "chest-2.glb") + "\n"; out != want {
		t.Fatalf("export printed %q, want %q", out, want)
	}

	blender, opened := fakeblender.Use(t, "4.2.0 LTS")
	if r := h.cli("", "config", "set", "blender.path="+filepath.Join(h.dir, "nope")); r.code == 0 {
		t.Fatal("a path with no Blender was saved")
	}
	h.ok("config", "set", "blender.path="+blender)
	t.Setenv("FORJA_BLENDER__PATH", blender)
	var formats api.ExportFormats
	h.okJSON(&formats, "assets", "formats")
	if formats.Blender == nil || formats.Blender.Version != "4.2.0" || !formats.Formats[1].Available {
		t.Fatalf("formats %+v", formats)
	}
	var res api.ExportResult
	h.okJSON(&res, "assets", "export", first, "--format", ".OBJ", "-o", dest)
	want := []string{filepath.Join(dest, "chest.obj"), filepath.Join(dest, "chest_Image_0.png"), filepath.Join(dest, "chest.mtl")}
	if res.Format != "obj" || len(res.Assets) != 1 || !slices.Equal(res.Assets[0].Files, want) {
		t.Fatalf("exported %+v", res)
	}

	if out := h.ok("assets", "blender", first); out != "Opened "+first+" in Blender 4.2.0\n" {
		t.Fatalf("blender printed %q", out)
	}
	var raw []byte
	for deadline := time.Now().Add(15 * time.Second); len(raw) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		raw, _ = os.ReadFile(opened)
	}
	if !strings.Contains(string(raw), `"mode":"open"`) {
		t.Fatalf("Blender was started with %q", raw)
	}

	if r := h.cli("", "assets", "rm", first); r.code == 0 || !strings.Contains(r.err, "--yes") {
		t.Fatalf("rm with no one to ask: exit %d, %s", r.code, r.err)
	}
	if r := h.cliAt(true, "n\n", "assets", "rm", first); r.code == 0 || !strings.Contains(r.out, "Delete this asset") {
		t.Fatalf("rm answered no: exit %d, %s", r.code, r.out)
	}
	if r := h.cliAt(true, "y\n", "assets", "rm", first); r.code != 0 || !strings.Contains(r.out, "Deleted "+first) {
		t.Fatalf("rm answered yes: exit %d\n%s%s", r.code, r.out, r.err)
	}
	r := h.cli("", "--json", "assets", "rm", "--yes", second, first)
	if r.code == 0 || !strings.Contains(r.out, second) || !strings.Contains(r.err, first+": no asset") {
		t.Fatalf("rm of one there and one gone: exit %d\n%s%s", r.code, r.out, r.err)
	}
	h.okJSON(&assets, "assets", "ls")
	var jobs []*library.Job
	h.okJSON(&jobs, "jobs", "ls")
	if len(assets) != 0 || len(jobs) != 0 {
		t.Fatalf("left %d assets and %d jobs", len(assets), len(jobs))
	}
	// What was exported stays.
	if _, err := os.Stat(filepath.Join(dest, "chest.obj")); err != nil {
		t.Fatal(err)
	}
}

func TestTheCacheIsShownLimitedAndCleared(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	h.ok("generate", img, "--preset", "draft")
	// Everything was used just now; age it past the hour clearing leaves alone.
	old := time.Now().Add(-48 * time.Hour)
	_ = h.store.Walk(func(o store.Object) error {
		p, _ := h.store.Path(o.Ref)
		return os.Chtimes(p, old, old)
	})

	if out := h.ok("cache"); !strings.Contains(out, "of 50.0 GB: stage outputs") || !strings.Contains(out, "library  ") {
		t.Fatalf("cache printed:\n%s", out)
	}
	h.ok("config", "set", "general.cache_max_gb=10")
	var u cache.Usage
	h.okJSON(&u, "cache")
	if u.LimitBytes != 10<<30 || u.CacheBytes == 0 || u.LibraryBytes == 0 {
		t.Fatalf("usage %+v", u)
	}
	var res api.CacheCleared
	h.okJSON(&res, "cache", "clear")
	if res.Freed.Objects == 0 || res.Freed.Bytes != u.CacheBytes || res.Usage.CacheBytes != 0 ||
		res.Usage.LibraryBytes != u.LibraryBytes || res.Freed.Entries == 0 {
		t.Fatalf("cleared %+v of %+v", res, u)
	}
	// The same job again computes its stages anew, except the export: its output is the
	// asset's GLB, which the library kept, so its entry stayed.
	var again []*library.Job
	h.okJSON(&again, "generate", img, "--preset", "draft")
	if len(again) != 1 || again[0].Status != library.Succeeded {
		t.Fatalf("after clearing, %+v", again)
	}
	for _, s := range again[0].Stages {
		if s.Cached != (s.Use == "export") {
			t.Fatalf("stage %s cached: %v", s.ID, s.Cached)
		}
	}
}
