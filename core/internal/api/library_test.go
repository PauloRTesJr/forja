package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/library"
)

// assets asks the list and returns the ids it holds, in order, with the total its header gives.
func assets(t *testing.T, base, query string) (ids string, total string, status int) {
	t.Helper()
	resp, err := http.Get(base + "/v1/assets?" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []*library.Asset
	if resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatal(err)
		}
	}
	var out []string
	for _, a := range list {
		out = append(out, a.ID)
	}
	return strings.Join(out, " "), resp.Header.Get(TotalHeader), resp.StatusCode
}

// The search box says "name or preset": "mobile" and "Mobile prop" found nothing, "red barrel"
// missed red-barrel, and "%" or "_" matched every asset.
func TestTheLibrarySearchesSortsAndPagesItsAssets(t *testing.T) {
	ts, s, _ := newServer(t)
	ctx := context.Background()
	glb, _ := s.Store.PutBytes([]byte("not a GLB, which the list does not mind"))
	day := time.Now().Add(-24 * time.Hour)
	for i, a := range []*library.Asset{
		{ID: "a1", Name: "red-barrel", Preset: "mobile-prop"},
		{ID: "a2", Name: "Baú do Tesouro", Preset: "draft"},
		{ID: "a3", Name: "100%_wool", Preset: "hero-character"},
		{ID: "a4", Name: "Zebra", Preset: "draft"},
		{ID: "a5", Name: "apple", Preset: "environment-piece"},
	} {
		a.JobID, a.GLB, a.CreatedAt = "j"+a.ID, glb, day.Add(time.Duration(i)*time.Hour)
		if err := s.DB.SaveAsset(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string]string{
		"":               "a5 a4 a3 a2 a1",
		"q=mobile":       "a1", // the preset's id
		"q=Mobile+prop":  "a1", // the name the preset shows
		"q=red+barrel":   "a1", // a space finds a hyphen
		"q=RED_BARREL":   "a1",
		"q=ba%C3%9A":     "a2",       // BAÚ: case-insensitive beyond ASCII
		"q=%25":          "a3",       // a percent sign is a percent sign
		"q=_":            "a3 a2 a1", // an underscore is a separator: the names that have one
		"q=100%25+wool":  "a3",
		"q=draft":        "a4 a2",
		"q=character":    "a3",
		"q=nothing+here": "",
		"sort=oldest":    "a1 a2 a3 a4 a5",
		"sort=name":      "a3 a5 a2 a1 a4", // 100%_wool, apple, Baú..., red-barrel, Zebra
		"sort=name&q=a":  "a5 a2 a1 a4 a3", // every match, by name: apple, Baú, red-barrel, Zebra... and hero-character
	} {
		if query == "sort=name&q=a" {
			want = "a3 a5 a2 a1 a4"
		}
		got, total, status := assets(t, ts.URL, query)
		n := 0
		if want != "" {
			n = len(strings.Fields(want))
		}
		if status != 200 || got != want || total != fmt.Sprint(n) {
			t.Errorf("?%s: %d [%s] total %s, want [%s] total %d", query, status, got, total, want, n)
		}
	}
	// Pages: the total says there is more than the page holds.
	for query, want := range map[string]string{
		"limit=2":                    "a5 a4",
		"limit=2&offset=2":           "a3 a2",
		"limit=2&offset=4":           "a1",
		"limit=2&offset=9":           "",
		"limit=2&offset=1&sort=name": "a5 a2",
		"limit=1&offset=1&q=draft":   "a2",
	} {
		got, total, _ := assets(t, ts.URL, query)
		wantTotal := "5"
		if strings.Contains(query, "q=draft") {
			wantTotal = "2"
		}
		if got != want || total != wantTotal {
			t.Errorf("?%s: [%s] total %s, want [%s] total %s", query, got, total, want, wantTotal)
		}
	}
	if _, _, status := assets(t, ts.URL, "sort=size"); status != 422 {
		t.Errorf("an unknown sort answered %d", status)
	}
	// A limit past the most a page holds gives the most, not the default.
	if got, _, _ := assets(t, ts.URL, "limit=100000"); got != "a5 a4 a3 a2 a1" {
		t.Errorf("limit=100000: [%s]", got)
	}
}

func TestAnAssetIsRenamedAndItsChangesArriveAsUpdates(t *testing.T) {
	ts, s, _ := newServer(t)
	ctx := context.Background()
	glb, _ := s.Store.PutBytes([]byte("glb"))
	if err := s.DB.SaveAsset(ctx, &library.Asset{ID: "a1", JobID: "j1", Name: "chest", Preset: "draft", GLB: glb, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// A copy made for dragging it out carries its name.
	var drag DragFile
	if code := call(t, ts, "POST", "/v1/assets/a1/drag", nil, &drag); code != 200 || filepath.Base(drag.Path) != "chest.glb" {
		t.Fatalf("drag: %d %+v", code, drag)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(drag.Path)) })
	evs, unsubscribe := s.Hub.Subscribe()
	defer unsubscribe()

	var renamed library.Asset
	if code := call(t, ts, "PATCH", "/v1/assets/a1", map[string]string{"name": "  Baú do Tesouro  "}, &renamed); code != 200 ||
		renamed.Name != "Baú do Tesouro" || renamed.ID != "a1" {
		t.Fatalf("rename: %d %+v", code, renamed)
	}
	if saved, _ := s.DB.Asset(ctx, "a1"); saved.Name != "Baú do Tesouro" {
		t.Fatalf("saved as %q", saved.Name)
	}
	ev := <-evs
	if a, ok := ev.Data.(*library.Asset); ev.Type != EvAssetUpdated || !ok || a.Name != "Baú do Tesouro" {
		t.Fatalf("event %s %+v", ev.Type, ev.Data)
	}
	if _, err := os.Stat(drag.Path); err == nil {
		t.Fatal("the drag copy still carries the old name")
	}
	for name, want := range map[string]int{"": 422, "   ": 422, "two\nlines": 422, strings.Repeat("x", 121): 422, strings.Repeat("é", 120): 200} {
		if code := call(t, ts, "PATCH", "/v1/assets/a1", map[string]string{"name": name}, nil); code != want {
			t.Errorf("renaming to %q answered %d, want %d", name, code, want)
		}
	}
	if code := call(t, ts, "PATCH", "/v1/assets/nope", map[string]string{"name": "x"}, nil); code != 404 {
		t.Errorf("renaming an unknown asset answered %d", code)
	}
	for len(evs) > 0 {
		<-evs
	}

	// A thumbnail is an update of the asset, not a new asset: N of them on a first visit made
	// the app read the whole list N times.
	req, _ := http.NewRequest("PUT", ts.URL+"/v1/assets/a1/thumbnail", bytes.NewReader([]byte("webp bytes")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("thumbnail: %v %v", resp, err)
	}
	resp.Body.Close()
	ev = <-evs
	if a, ok := ev.Data.(*library.Asset); ev.Type != EvAssetUpdated || !ok || a.Thumbnail == "" {
		t.Fatalf("a thumbnail published %s %+v", ev.Type, ev.Data)
	}

	// Deleting the asset takes its drag copy along.
	if code := call(t, ts, "POST", "/v1/assets/a1/drag", nil, &drag); code != 200 {
		t.Fatalf("drag again: %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/assets/a1", nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Dir(drag.Path)); err == nil {
		t.Fatal("the drag copy outlived its asset")
	}
}

// Saving one asset as "chest" made a folder named chest; exporting into a folder silently
// replaced a file of the same name.
func TestAnExportNamesAFileAndKeepsWhatIsThere(t *testing.T) {
	ts, s, _ := newServer(t)
	ctx := context.Background()
	glb, _ := s.Store.PutBytes([]byte("glb bytes"))
	for _, id := range []string{"a1", "a2"} {
		if err := s.DB.SaveAsset(ctx, &library.Asset{ID: id, JobID: "j" + id, Name: "chest", Preset: "draft", GLB: glb, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	var res ExportResult
	file := filepath.Join(dir, "My chest")
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1"}, Format: "glb", Path: file, File: true}, &res); code != 200 ||
		res.Assets[0].Files[0] != file+".glb" {
		t.Fatalf("a file without its extension: %d %+v", code, res)
	}
	if st, err := os.Stat(file + ".glb"); err != nil || st.IsDir() {
		t.Fatalf("%s.glb: %v", file, err)
	}
	folder := filepath.Join(dir, "folder")
	for i, want := range []string{"chest.glb", "chest (2).glb"} {
		if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1"}, Format: "glb", Path: folder}, &res); code != 200 ||
			res.Assets[0].Files[0] != filepath.Join(folder, want) {
			t.Fatalf("export %d into the folder: %d %+v, want %s", i+1, code, res, want)
		}
	}
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1"}, Format: "glb", Path: folder,
		OnConflict: ConflictReplace}, &res); code != 200 || res.Assets[0].Files[0] != filepath.Join(folder, "chest.glb") {
		t.Fatalf("replace: %d %+v", code, res)
	}
	for _, bad := range []ExportRequest{
		{IDs: []string{"a1"}, Format: "glb", Path: folder, OnConflict: "ask"},
		{IDs: []string{"a1", "a2"}, Format: "glb", Path: file, File: true},
	} {
		if code := call(t, ts, "POST", "/v1/export", bad, nil); code != 422 {
			t.Errorf("%+v answered %d", bad, code)
		}
	}
}
