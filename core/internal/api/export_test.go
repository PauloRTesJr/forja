package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/export/fakeblender"
	"forja/internal/library"
)

func TestMain(m *testing.M) {
	fakeblender.Main()
	os.Exit(m.Run())
}

// twoChests saves two assets of the same name, each with its job, and returns their GLB.
func twoChests(t *testing.T, s *Server) string {
	t.Helper()
	ctx := context.Background()
	glb, err := s.Store.PutBytes(glbOf(`{"asset": {"version": "2.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"1", "2"} {
		at := time.Now().Add(time.Duration(i-2) * time.Minute)
		if err := s.DB.SaveJob(ctx, &library.Job{ID: "j" + id, Name: "chest", Preset: "draft", Input: glb,
			Status: library.Succeeded, CreatedAt: at, AssetID: "a" + id}); err != nil {
			t.Fatal(err)
		}
		if err := s.DB.SaveAsset(ctx, &library.Asset{ID: "a" + id, JobID: "j" + id, Name: "chest", Preset: "draft", GLB: glb,
			CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	return glb
}

func TestAssetsExportToGLBWithoutBlenderAndToOtherFormatsWithIt(t *testing.T) {
	ts, s, _ := newServer(t)
	glb := twoChests(t, s)
	t.Setenv("FORJA_BLENDER__PATH", filepath.Join(t.TempDir(), "no-blender"))

	var formats ExportFormats
	if code := call(t, ts, "GET", "/v1/export/formats", nil, &formats); code != 200 || formats.Blender != nil ||
		!strings.Contains(formats.BlenderProblem, "no Blender at") {
		t.Fatalf("status %d, %+v", code, formats)
	}
	for _, f := range formats.Formats {
		if f.Available != (f.ID == "glb") || (!f.Available && f.Problem != formats.BlenderProblem) {
			t.Fatalf("without Blender, %+v", f)
		}
	}
	dest := t.TempDir()
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1"}, Format: "fbx", Path: dest}, nil); code != 422 {
		t.Fatalf("FBX without Blender answered %d", code)
	}
	var res ExportResult
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1", "a2"}, Format: "glb", Path: dest}, &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(res.Assets) != 2 || res.Assets[0].Files[0] != filepath.Join(dest, "chest.glb") ||
		res.Assets[1].Files[0] != filepath.Join(dest, "chest-2.glb") {
		t.Fatalf("exported %+v", res)
	}
	want, _ := os.ReadFile(must(s.Store.Path(glb)))
	if got, _ := os.ReadFile(res.Assets[1].Files[0]); string(got) != string(want) {
		t.Fatal("the GLB was not copied as it is")
	}
	for _, bad := range []struct {
		req  ExportRequest
		code int
	}{
		{ExportRequest{IDs: []string{"a1"}, Format: "max", Path: dest}, 422},
		{ExportRequest{IDs: []string{"a1"}, Format: "glb", Path: "relative/folder"}, 422},
		{ExportRequest{Format: "glb", Path: dest}, 422},
		{ExportRequest{IDs: []string{"a1", "nope"}, Format: "glb", Path: dest}, 404},
	} {
		if code := call(t, ts, "POST", "/v1/export", bad.req, nil); code != bad.code {
			t.Errorf("%+v answered %d, want %d", bad.req, code, bad.code)
		}
	}

	blender, _ := fakeblender.Use(t, "5.2.1 LTS")
	t.Setenv("FORJA_BLENDER__PATH", blender)
	formats = ExportFormats{}
	if code := call(t, ts, "GET", "/v1/export/formats", nil, &formats); code != 200 || formats.Blender == nil ||
		formats.Blender.Version != "5.2.1" || formats.BlenderProblem != "" {
		t.Fatalf("status %d, %+v", code, formats)
	}
	res = ExportResult{}
	file := filepath.Join(dest, "Printed Chest.stl")
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a2"}, Format: "stl", Path: file}, &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if res.Format != "stl" || len(res.Assets) != 1 || res.Assets[0].Asset != "a2" || res.Assets[0].Files[0] != file {
		t.Fatalf("exported %+v", res)
	}
}

func TestAnAssetOpensInBlender(t *testing.T) {
	ts, s, _ := newServer(t)
	glb := twoChests(t, s)
	blender, opened := fakeblender.Use(t, "5.2.1")
	t.Setenv("FORJA_BLENDER__PATH", blender)
	if code := call(t, ts, "POST", "/v1/assets/a1/blender", nil, nil); code != 202 {
		t.Fatalf("status %d", code)
	}
	var raw []byte
	for deadline := time.Now().Add(15 * time.Second); len(raw) == 0 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		raw, _ = os.ReadFile(opened)
	}
	var job struct {
		Mode  string `json:"mode"`
		Items []struct {
			GLB string `json:"glb"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &job); err != nil || job.Mode != "open" || job.Items[0].GLB != must(s.Store.Path(glb)) {
		t.Fatalf("Blender was started with %s (%v)", raw, err)
	}
	if code := call(t, ts, "POST", "/v1/assets/nope/blender", nil, nil); code != 404 {
		t.Fatalf("an unknown asset answered %d", code)
	}
}

func TestDeletingAnAssetTakesItsJobAndTellsClients(t *testing.T) {
	ts, s, _ := newServer(t)
	twoChests(t, s)
	events, stop := s.Hub.Subscribe()
	defer stop()
	if code := call(t, ts, "DELETE", "/v1/assets/a1", nil, nil); code != 204 {
		t.Fatalf("status %d", code)
	}
	select {
	case ev := <-events:
		if d, ok := ev.Data.(AssetDeleted); ev.Type != EvAssetDeleted || !ok || d.ID != "a1" || d.Job != "j1" {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	var list []library.Asset
	if code := call(t, ts, "GET", "/v1/assets", nil, &list); code != 200 || len(list) != 1 || list[0].ID != "a2" {
		t.Fatalf("status %d, assets %+v", code, list)
	}
	if code := call(t, ts, "GET", "/v1/jobs/j1", nil, nil); code != 404 {
		t.Fatalf("its job answered %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/assets/a1", nil, nil); code != 404 {
		t.Fatalf("deleting it again answered %d", code)
	}
}

func must(path string, err error) string {
	if err != nil {
		panic(err)
	}
	return path
}

func TestAnAssetIsReadiedForDraggingOutOfTheApp(t *testing.T) {
	tmp := t.TempDir()
	for _, k := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(k, tmp)
	}
	ts, s, _ := newServer(t)
	glb := twoChests(t, s)
	stale := filepath.Join(tmp, "forja-drag", "yesterday")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(stale, old, old)

	var d DragFile
	if code := call(t, ts, "POST", "/v1/assets/a1/drag", nil, &d); code != 200 {
		t.Fatalf("status %d", code)
	}
	if d.Path != filepath.Join(tmp, "forja-drag", "a1", "chest.glb") {
		t.Fatalf("path %s", d.Path)
	}
	want, _ := os.ReadFile(must(s.Store.Path(glb)))
	if got, _ := os.ReadFile(d.Path); string(got) != string(want) {
		t.Fatal("the dragged file is not the asset's GLB")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Fatal("yesterday's drag folder was kept")
	}

	// Asked again, it hands back the same file without writing it again.
	_ = os.Chtimes(d.Path, old, old)
	var again DragFile
	call(t, ts, "POST", "/v1/assets/a1/drag", nil, &again)
	if info, _ := os.Stat(d.Path); again.Path != d.Path || !info.ModTime().Equal(old) {
		t.Fatalf("asked again: %s, written at %s", again.Path, info.ModTime())
	}
	// A drop that moved it away (a folder on the same drive) gets it written again.
	_ = os.Remove(d.Path)
	if code := call(t, ts, "POST", "/v1/assets/a1/drag", nil, nil); code != 200 {
		t.Fatalf("status %d", code)
	}
	if _, err := os.Stat(d.Path); err != nil {
		t.Fatalf("not written again: %v", err)
	}
	if code := call(t, ts, "POST", "/v1/assets/nope/drag", nil, nil); code != 404 {
		t.Fatalf("an unknown asset answered %d", code)
	}
}
