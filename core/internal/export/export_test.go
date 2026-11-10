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
	code := m.Run()
	fakeblender.Cleanup()
	os.Exit(code)
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
		filepath.Join(dir, "picked", "My Chest.GLB"), Options{})
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
	}, folder, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(folder), []string{"Chest (2).glb", "a-b-c.glb", "chest-2.glb", "chest.glb"}; !slices.Equal(got, want) {
		t.Fatalf("folder holds %v, want %v", got, want)
	}
	if out[1].Files[0] != filepath.Join(folder, "Chest (2).glb") {
		t.Fatalf("the second chest went to %v", out[1].Files)
	}
}

func TestOtherFormatsNeedBlender(t *testing.T) {
	f, _ := LookupFormat("fbx")
	_, err := ToFiles(context.Background(), nil, f, []Item{{Asset: "a1", Name: "chest", GLB: "x"}}, t.TempDir(), Options{})
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
	}, dir, Options{})
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
	if out[2].Files[0] != filepath.Join(dir, "chest (2).obj") {
		t.Fatalf("third asset %+v", out[2])
	}
	// Nothing of the hidden folder Blender wrote into is left.
	if got := names(dir); !slices.Equal(got, []string{"chest (2).mtl", "chest (2).obj", "chest (2)_Image_0.png", "chest.mtl", "chest.obj", "chest_Image_0.png"}) {
		t.Fatalf("folder holds %v", got)
	}

	fbx, _ := LookupFormat("fbx")
	out, err = ToFiles(context.Background(), b, fbx, []Item{{Asset: "a1", Name: "chest", GLB: good}}, filepath.Join(dir, "Hero.fbx"), Options{})
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

// Saving one asset as "chest" made a folder named chest with chest.glb inside; exporting
// into a folder that already held chest.glb replaced it without a word.
func TestAFileGetsItsExtensionAndATakenNameIsNumbered(t *testing.T) {
	dir := t.TempDir()
	glb := write(t, filepath.Join(dir, "store-object"), "new bytes")
	f, _ := LookupFormat("glb")
	one := []Item{{Asset: "a1", Name: "chest", GLB: glb}}
	ctx := context.Background()

	out, err := ToFiles(ctx, nil, f, one, filepath.Join(dir, "saved", "My chest"), Options{File: true})
	if err != nil || out[0].Files[0] != filepath.Join(dir, "saved", "My chest.glb") {
		t.Fatalf("a file without its extension: %+v, %v", out, err)
	}
	if st, err := os.Stat(out[0].Files[0]); err != nil || st.IsDir() {
		t.Fatalf("%s is not a file: %v", out[0].Files[0], err)
	}
	if _, err := ToFiles(ctx, nil, f, append(one, one...), filepath.Join(dir, "saved", "two"), Options{File: true}); err == nil {
		t.Fatal("two assets went into one file")
	}

	folder := filepath.Join(dir, "folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := write(t, filepath.Join(folder, "chest.glb"), "the user's own file")
	out, err = ToFiles(ctx, nil, f, one, folder, Options{})
	if err != nil || out[0].Files[0] != filepath.Join(folder, "chest (2).glb") {
		t.Fatalf("a taken name: %+v, %v", out, err)
	}
	if got, _ := os.ReadFile(mine); string(got) != "the user's own file" {
		t.Fatalf("the file that was there now holds %q", got)
	}
	out, err = ToFiles(ctx, nil, f, one, folder, Options{})
	if err != nil || out[0].Files[0] != filepath.Join(folder, "chest (3).glb") {
		t.Fatalf("a name taken twice: %+v, %v", out, err)
	}
	// Asked to replace, it does; and a file the caller named is the caller's choice.
	out, err = ToFiles(ctx, nil, f, one, folder, Options{Replace: true})
	if err != nil || !strings.EqualFold(out[0].Files[0], mine) {
		t.Fatalf("replace: %+v, %v", out, err)
	}
	if got, _ := os.ReadFile(mine); string(got) != "new bytes" {
		t.Fatalf("replace left %q", got)
	}
	out, err = ToFiles(ctx, nil, f, one, filepath.Join(folder, "chest (2).glb"), Options{})
	if err != nil || out[0].Files[0] != filepath.Join(folder, "chest (2).glb") {
		t.Fatalf("a named file: %+v, %v", out, err)
	}
}

func TestANameShowsAsTypedAndIsMadeSafeOnlyForFiles(t *testing.T) {
	for in, want := range map[string]string{
		"chest.png": "chest", "Baú do Tesouro.PNG": "Baú do Tesouro", "宝箱.webp": "宝箱", "My Chest v1.2": "My Chest v1.2",
		"  red-barrel.jpeg ": "red-barrel", "": "asset", ".png": ".png", "tab\there.png": "tab here",
		strings.Repeat("é", 200) + ".png": strings.Repeat("é", 120),
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := DisplayName(filepath.Join("some", "folder", "Dragão.png")); got != "Dragão" {
		t.Errorf("a path shows as %q", got)
	}
	// Where files are made: the letters stay in a library export, a project folder gets ASCII.
	if FileName("Baú: do/Tesouro?") != "Baú- do-Tesouro-" || Slug("Baú do Tesouro") != "ba-do-tesouro" {
		t.Errorf("file names: %q, %q", FileName("Baú: do/Tesouro?"), Slug("Baú do Tesouro"))
	}
}

// Settings used to run any file typed into "Blender's path" with --version for up to 30 s.
func TestOnlyAProgramNamedBlenderIsAskedForItsVersion(t *testing.T) {
	dir := t.TempDir()
	notes := write(t, filepath.Join(dir, "notes.exe"), "x")
	if _, err := FindBlender(notes); err == nil || !strings.Contains(err.Error(), "notes.exe is not Blender's program") {
		t.Fatalf("a file with another name: %v", err)
	}
	if err := CheckBlenderPath("blender"); err == nil || !strings.Contains(err.Error(), "full path") {
		t.Fatalf("a relative path: %v", err)
	}
	for _, name := range []string{"blender", "blender.exe", "Blender", "Blender.EXE"} {
		if err := CheckBlenderPath(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { probeTimeout = old })
	path, _ := fake(t, fakeblender.Hangs)
	start := time.Now()
	if _, err := FindBlender(path); err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Fatalf("a program that never answers: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the probe took %v", took)
	}
}
