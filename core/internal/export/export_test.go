package export

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"forja/internal/export/fakeblender"
)

func TestMain(m *testing.M) {
	fakeblender.Main()
	os.Exit(m.Run())
}

// fake makes this test binary a Blender of version and forgets what was probed before.
func fake(t *testing.T, version string) (path, opened string) {
	path, opened = fakeblender.Use(t, version)
	probeMu.Lock()
	probes = map[string]probed{}
	probeMu.Unlock()
	return path, opened
}

func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func names(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestBlenderIsUsedOnlyWhenItIsNewEnough(t *testing.T) {
	path, _ := fake(t, "4.2.3 LTS")
	b, err := FindBlender(path)
	if err != nil || b.Path != path || b.Version != "4.2.3" {
		t.Fatalf("found %+v, %v", b, err)
	}
	path, _ = fake(t, "3.6.5")
	if _, err := FindBlender(path); !errors.Is(err, errTooOld) || !strings.Contains(err.Error(), "Blender 3.6.5") ||
		!strings.Contains(err.Error(), "4.2 or newer") {
		t.Fatalf("an old Blender: %v", err)
	}
	if _, err := FindBlender(filepath.Join(t.TempDir(), "blender")); err == nil || !strings.Contains(err.Error(), "no Blender at") {
		t.Fatalf("a path with nothing there: %v", err)
	}
}

func TestInstallsAreTriedNewestFirst(t *testing.T) {
	got := newestFirst([]string{
		filepath.Join("pf", "Blender 4.2", "blender.exe"), filepath.Join("pf", "Blender 4.10", "blender.exe"),
		filepath.Join("pf", "Blender 5.2", "blender.exe"), filepath.Join("pf", "Blender", "blender.exe"),
	})
	var dirs []string
	for _, p := range got {
		dirs = append(dirs, filepath.Base(filepath.Dir(p)))
	}
	if want := []string{"Blender 5.2", "Blender 4.10", "Blender 4.2", "Blender"}; !slices.Equal(dirs, want) {
		t.Fatalf("order %v, want %v", dirs, want)
	}
}

func TestAGLBIsCopiedWithoutBlenderAndRepeatedNamesAreNumbered(t *testing.T) {
	dir := t.TempDir()
	glb := write(t, filepath.Join(dir, "store-object"), "glTF bytes")
	f, _ := LookupFormat("glb")
	out, err := ToFiles(context.Background(), nil, f, []Item{{Asset: "a1", Name: "chest", GLB: glb}},
		filepath.Join(dir, "picked", "My Chest.GLB"))
	if err != nil || len(out) != 1 || out[0].Asset != "a1" || len(out[0].Files) != 1 {
		t.Fatalf("one file: %+v, %v", out, err)
	}
	if got, _ := os.ReadFile(out[0].Files[0]); string(got) != "glTF bytes" || filepath.Base(out[0].Files[0]) != "My Chest.GLB" {
		t.Fatalf("wrote %s: %q", out[0].Files[0], got)
	}

	folder := filepath.Join(dir, "many")
	out, err = ToFiles(context.Background(), nil, f, []Item{
		{Asset: "a1", Name: "chest", GLB: glb}, {Asset: "a2", Name: "Chest", GLB: glb}, {Asset: "a3", Name: "chest-2", GLB: glb},
		{Asset: "a4", Name: `a:b/c`, GLB: glb},
	}, folder)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(folder), []string{"Chest-2.glb", "a-b-c.glb", "chest-2-2.glb", "chest.glb"}; !slices.Equal(got, want) {
		t.Fatalf("folder holds %v, want %v", got, want)
	}
	if out[1].Files[0] != filepath.Join(folder, "Chest-2.glb") {
		t.Fatalf("the second chest went to %v", out[1].Files)
	}
}

func TestOtherFormatsNeedBlender(t *testing.T) {
	f, _ := LookupFormat("fbx")
	_, err := ToFiles(context.Background(), nil, f, []Item{{Asset: "a1", Name: "chest", GLB: "x"}}, t.TempDir())
	if !errors.Is(err, ErrNoBlender) {
		t.Fatalf("err %v", err)
	}
}

func TestBlenderConvertsEachAssetAndOneThatFailsLeavesTheOthers(t *testing.T) {
	path, _ := fake(t, "5.2.1 LTS")
	b, err := FindBlender(path)
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	good := write(t, filepath.Join(src, "good"), "glTF")
	bad := write(t, filepath.Join(src, "bad"), fakeblender.Unreadable)
	dir := filepath.Join(t.TempDir(), "out")
	obj, _ := LookupFormat("obj")
	out, err := ToFiles(context.Background(), b, obj, []Item{
		{Asset: "a1", Name: "chest", GLB: good}, {Asset: "a2", Name: "broken", GLB: bad}, {Asset: "a3", Name: "chest", GLB: good},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "chest.obj"), filepath.Join(dir, "chest_Image_0.png"), filepath.Join(dir, "chest.mtl")}
	if !slices.Equal(out[0].Files, want) || out[0].Error != "" {
		t.Fatalf("first asset %+v, want %v", out[0], want)
	}
	if out[1].Asset != "a2" || len(out[1].Files) != 0 || !strings.Contains(out[1].Error, "cannot read") {
		t.Fatalf("broken asset %+v", out[1])
	}
	if out[2].Files[0] != filepath.Join(dir, "chest-2.obj") {
		t.Fatalf("third asset %+v", out[2])
	}
	// Nothing of the hidden folder Blender wrote into is left.
	if got := names(dir); !slices.Equal(got, []string{"chest-2.mtl", "chest-2.obj", "chest-2_Image_0.png", "chest.mtl", "chest.obj", "chest_Image_0.png"}) {
		t.Fatalf("folder holds %v", got)
	}

	fbx, _ := LookupFormat("fbx")
	out, err = ToFiles(context.Background(), b, fbx, []Item{{Asset: "a1", Name: "chest", GLB: good}}, filepath.Join(dir, "Hero.fbx"))
	if err != nil || len(out[0].Files) != 1 || out[0].Files[0] != filepath.Join(dir, "Hero.fbx") {
		t.Fatalf("one FBX: %+v, %v", out, err)
	}
	if got, _ := os.ReadFile(out[0].Files[0]); string(got) != "fake fbx of good" {
		t.Fatalf("Hero.fbx holds %q", got)
	}
}

func TestOpeningStartsBlenderWithTheAssetsAndDoesNotWait(t *testing.T) {
	path, opened := fake(t, "5.2.1")
	// A non-ASCII name, as a home folder may have, reaches Blender intact.
	glb := write(t, filepath.Join(t.TempDir(), "baú.glb"), "glTF")
	if err := (&Blender{Path: path, Version: "5.2.1"}).Open([]string{glb}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	for deadline := time.Now().Add(15 * time.Second); len(raw) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		raw, _ = os.ReadFile(opened)
	}
	var j job
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("Blender was started with %q: %v", raw, err)
	}
	if j.Mode != "open" || len(j.Items) != 1 || j.Items[0].GLB != glb {
		t.Fatalf("Blender was asked %+v", j)
	}
}
