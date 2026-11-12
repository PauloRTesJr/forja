package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/glb"
	"forja/internal/library"
)

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// stripes is a texture of three vertical bands: wood, iron and gold.
func stripes(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cols := []color.RGBA{{139, 90, 43, 255}, {96, 98, 104, 255}, {212, 175, 55, 255}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, cols[min(2, x*10/w/4)]) // 40% wood, 40% iron, 20% gold
		}
	}
	return img
}

// texturedGLB is a one-triangle asset with a base color texture and, with orm, a
// metallic-roughness map shared with the occlusion slot.
func texturedGLB(t *testing.T, base, orm image.Image) []byte {
	t.Helper()
	var bin []byte
	var views []any
	add := func(b []byte) int {
		for len(bin)%4 != 0 {
			bin = append(bin, 0)
		}
		views = append(views, map[string]any{"buffer": 0, "byteOffset": len(bin), "byteLength": len(b)})
		bin = append(bin, b...)
		return len(views) - 1
	}
	add(make([]byte, 36))
	images := []any{map[string]any{"bufferView": add(pngBytes(t, base)), "mimeType": "image/png"}}
	textures := []any{map[string]any{"source": 0}}
	pbr := map[string]any{"baseColorTexture": map[string]any{"index": 0}, "roughnessFactor": 0.8, "metallicFactor": 0}
	mat := map[string]any{"pbrMetallicRoughness": pbr}
	if orm != nil {
		images = append(images, map[string]any{"bufferView": add(pngBytes(t, orm)), "mimeType": "image/png"})
		textures = append(textures, map[string]any{"source": 1})
		pbr["metallicRoughnessTexture"] = map[string]any{"index": 1}
		mat["occlusionTexture"] = map[string]any{"index": 1}
	}
	doc := map[string]any{
		"asset": map[string]string{"version": "2.0"}, "scene": 0, "scenes": []any{map[string]any{"nodes": []int{0}}},
		"nodes":     []any{map[string]any{"name": "crate", "mesh": 0}},
		"meshes":    []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0}, "material": 0}}}},
		"accessors": []any{map[string]any{"count": 3, "bufferView": 0}},
		"materials": []any{mat}, "textures": textures, "images": images,
		"bufferViews": views, "buffers": []any{map[string]any{"byteLength": len(bin)}},
	}
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	for len(bin)%4 != 0 {
		bin = append(bin, 0)
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint32{0x46546C67, 2, uint32(12 + 8 + len(js) + 8 + len(bin))})
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(js)), 0x4E4F534A})
	out.Write(js)
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(bin)), 0x004E4942})
	out.Write(bin)
	return out.Bytes()
}

func saveAsset(t *testing.T, s *Server, id string, data []byte) {
	t.Helper()
	ref, err := s.Store.PutBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := glb.Read(bytes.NewReader(data))
	if err := s.DB.SaveAsset(context.Background(), &library.Asset{ID: id, JobID: "j" + id, Name: "crate", Preset: "draft",
		GLB: ref, Stats: st.Map(), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func f64(v float64) *float64 { return &v }

// metallicRoughness reads the map an asset's GLB carries now.
func metallicRoughness(t *testing.T, s *Server, id string) *image.RGBA {
	t.Helper()
	a, err := s.DB.Asset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := glb.Load(must(s.Store.Path(a.GLB)))
	if err != nil {
		t.Fatal(err)
	}
	mats, _ := f.Materials()
	if mats[0].MetallicRoughness == nil {
		t.Fatal("the asset has no metallic-roughness map")
	}
	raw, _, err := f.Image(mats[0].MetallicRoughness.Image)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewRGBA(img.Bounds())
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			rgba.Set(x, y, img.At(x, y))
		}
	}
	return rgba
}

func TestMaterialsListedAndSetOnAnAsset(t *testing.T) {
	ts, s, _ := newServer(t)
	saveAsset(t, s, "a1", texturedGLB(t, stripes(200, 100), nil))
	before, _ := s.DB.Asset(context.Background(), "a1")
	events, stop := s.Hub.Subscribe()
	defer stop()

	var list AssetMaterials
	if code := call(t, ts, "GET", "/v1/assets/a1/materials", nil, &list); code != 200 || len(list.Materials) != 3 || list.Painted || list.Reason != "" {
		t.Fatalf("status %d, %+v", code, list)
	}
	// Largest share first: wood and iron are 40% each, gold 20%; nothing was set, so every
	// material shows the file's constant factors.
	if list.Materials[2].Color != "#d4af37" || list.Materials[2].Share < 0.19 || list.Materials[2].Share > 0.21 {
		t.Errorf("materials %+v", list.Materials)
	}
	for _, m := range list.Materials {
		if m.Roughness != 0.8 || m.Metallic != 0 {
			t.Errorf("material %+v", m)
		}
	}

	set := MaterialsRequest{Materials: []MaterialSet{{Index: 2, Roughness: f64(0.25), Metallic: f64(1)}}}
	var after AssetMaterials
	if code := call(t, ts, "PUT", "/v1/assets/a1/materials", set, &after); code != 200 {
		t.Fatalf("status %d", code)
	}
	if after.Materials[2].Roughness != 0.25 || after.Materials[2].Metallic != 1 || after.Materials[0].Roughness != 0.8 {
		t.Errorf("answered %+v", after.Materials)
	}

	// The asset points at a new object, and keeps its stats.
	a, _ := s.DB.Asset(context.Background(), "a1")
	if a.GLB == before.GLB || !s.Store.Has(a.GLB) || !s.Store.Has(before.GLB) {
		t.Errorf("asset GLB %s, was %s", a.GLB, before.GLB)
	}
	if a.Stats["triangles"] != before.Stats["triangles"] || a.Stats["materials"] != before.Stats["materials"] || len(a.Materials) != 3 {
		t.Errorf("stats %v, choices %v", a.Stats, a.Materials)
	}
	// The asset-changed event carries the new GLB.
	select {
	case ev := <-events:
		got, ok := ev.Data.(*library.Asset)
		if ev.Type != EvAssetUpdated || !ok || got.GLB != a.GLB {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	// The map carries the values: gold is rough 0.25 (64) and metallic (255), wood keeps 0.8 (204) and 0.
	m := metallicRoughness(t, s, "a1")
	if px := m.RGBAAt(190, 50); px.G != 64 || px.B != 255 {
		t.Errorf("gold texel %v", px)
	}
	if px := m.RGBAAt(20, 50); px.G != 204 || px.B != 0 {
		t.Errorf("wood texel %v", px)
	}
	// Read again, the asset shows what was set; another request leaves the omitted ones alone.
	var again AssetMaterials
	call(t, ts, "GET", "/v1/assets/a1/materials", nil, &again)
	if again.Painted || again.Materials[2].Roughness != 0.25 || again.Materials[2].Metallic != 1 {
		t.Errorf("after: %+v", again)
	}
	if code := call(t, ts, "PUT", "/v1/assets/a1/materials",
		MaterialsRequest{Materials: []MaterialSet{{Index: 0, Metallic: f64(0.5)}}}, &after); code != 200 {
		t.Fatalf("second set: status %d", code)
	}
	if after.Materials[2].Roughness != 0.25 || after.Materials[0].Metallic != 0.5 || after.Materials[0].Roughness != 0.8 {
		t.Errorf("second answer %+v", after.Materials)
	}

	// Exports and the drag-out copy read the asset's current GLB.
	dest := t.TempDir()
	var res ExportResult
	if code := call(t, ts, "POST", "/v1/export", ExportRequest{IDs: []string{"a1"}, Format: "glb", Path: dest}, &res); code != 200 {
		t.Fatalf("export status %d", code)
	}
	a, _ = s.DB.Asset(context.Background(), "a1")
	want, _ := os.ReadFile(must(s.Store.Path(a.GLB)))
	if got, _ := os.ReadFile(filepath.Join(dest, "crate.glb")); !bytes.Equal(got, want) {
		t.Error("the export is not the asset's current GLB")
	}
}

func TestMaterialsRequestsAreValidated(t *testing.T) {
	ts, s, _ := newServer(t)
	saveAsset(t, s, "a1", texturedGLB(t, stripes(64, 64), nil))
	before, _ := s.DB.Asset(context.Background(), "a1")
	for name, tc := range map[string]struct {
		id   string
		body any
		code int
	}{
		"no such asset":      {"nope", MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(0.5)}}}, 404},
		"not json":           {"a1", "just words", 400},
		"nothing named":      {"a1", MaterialsRequest{}, 422},
		"unknown material":   {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: 9, Roughness: f64(0.5)}}}, 422},
		"negative index":     {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: -1, Roughness: f64(0.5)}}}, 422},
		"rougher than rough": {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(1.5)}}}, 422},
		"below zero":         {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: 0, Metallic: f64(-0.1)}}}, 422},
		"no values":          {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: 0}}}, 422},
		"the same twice":     {"a1", MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(0.1)}, {Index: 0, Metallic: f64(0.1)}}}, 422},
	} {
		var p map[string]any
		if code := call(t, ts, "PUT", "/v1/assets/"+tc.id+"/materials", tc.body, &p); code != tc.code {
			t.Errorf("%s: status %d (%v), want %d", name, code, p, tc.code)
		}
	}
	if after, _ := s.DB.Asset(context.Background(), "a1"); after.GLB != before.GLB {
		t.Error("a refused request changed the asset")
	}
	if code := call(t, ts, "GET", "/v1/assets/nope/materials", nil, nil); code != 404 {
		t.Errorf("GET unknown asset: %d", code)
	}
}

func TestAnAssetWithoutAMaterialToTellApartSaysWhy(t *testing.T) {
	ts, s, _ := newServer(t)
	// No textures at all.
	saveAsset(t, s, "plain", glbOf(`{"asset": {"version": "2.0"}, "scenes": [{"nodes": [0]}],
		"nodes": [{"mesh": 0}], "meshes": [{"primitives": [{"attributes": {"POSITION": 0}, "material": 0}]}],
		"accessors": [{"count": 3}], "materials": [{"pbrMetallicRoughness": {"baseColorFactor": [1, 0, 0, 1]}}]}`))
	var list AssetMaterials
	if code := call(t, ts, "GET", "/v1/assets/plain/materials", nil, &list); code != 200 || len(list.Materials) != 0 ||
		!strings.Contains(list.Reason, "no base color texture") {
		t.Fatalf("status %d, %+v", code, list)
	}
	var p map[string]any
	if code := call(t, ts, "PUT", "/v1/assets/plain/materials",
		MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(0.5)}}}, &p); code != 422 {
		t.Errorf("PUT answered %d", code)
	}
	// A texture stored as KTX2.
	ktx := texturedGLB(t, stripes(8, 8), nil)
	ktx = bytes.Replace(ktx, []byte("\x89PNG"), []byte("\xABKTX"), 1)
	saveAsset(t, s, "ktx", ktx)
	if code := call(t, ts, "GET", "/v1/assets/ktx/materials", nil, &list); code != 200 || !strings.Contains(list.Reason, "PNG and JPEG") {
		t.Fatalf("status %d, %+v", code, list)
	}
	// A GLB missing from the store, and one that is no GLB.
	if err := s.DB.SaveAsset(context.Background(), &library.Asset{ID: "gone", JobID: "jg", Name: "gone", Preset: "draft",
		GLB: strings.Repeat("ab", 32), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if code := call(t, ts, "GET", "/v1/assets/gone/materials", nil, &list); code != 200 || !strings.Contains(list.Reason, "missing from the store") {
		t.Fatalf("status %d, %+v", code, list)
	}
}

func TestPaintedMapsAreOverwrittenOnlyWhenAsked(t *testing.T) {
	ts, s, _ := newServer(t)
	orm := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			orm.SetRGBA(x, y, color.RGBA{230, uint8(40 + x*3), uint8(y * 4), 255})
		}
	}
	saveAsset(t, s, "p", texturedGLB(t, stripes(64, 64), orm))
	before, _ := s.DB.Asset(context.Background(), "p")

	var list AssetMaterials
	if code := call(t, ts, "GET", "/v1/assets/p/materials", nil, &list); code != 200 || !list.Painted || len(list.Materials) != 3 {
		t.Fatalf("status %d, %+v", code, list)
	}
	set := MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(0.5)}}}
	var refused map[string]any
	if code := call(t, ts, "PUT", "/v1/assets/p/materials", set, &refused); code != 409 || refused["painted"] != true ||
		!strings.Contains(refused["detail"].(string), "painted by its model") {
		t.Fatalf("status %d, %v", code, refused)
	}
	if now, _ := s.DB.Asset(context.Background(), "p"); now.GLB != before.GLB {
		t.Fatal("a refused request changed the asset")
	}
	set.Overwrite = true
	if code := call(t, ts, "PUT", "/v1/assets/p/materials", set, &list); code != 200 || list.Painted {
		t.Fatalf("status %d, %+v", code, list)
	}
	// The occlusion in red stays; the map is now the person's, so setting more needs no overwrite.
	if px := metallicRoughness(t, s, "p").RGBAAt(3, 3); px.R != 230 {
		t.Errorf("red %d", px.R)
	}
	set.Overwrite = false
	set.Materials[0].Roughness = f64(0.6)
	if code := call(t, ts, "PUT", "/v1/assets/p/materials", set, &list); code != 200 {
		t.Fatalf("status %d", code)
	}
	var again AssetMaterials
	call(t, ts, "GET", "/v1/assets/p/materials", nil, &again)
	if again.Painted || again.Materials[0].Roughness != 0.6 {
		t.Errorf("%+v", again)
	}
}

func TestEditingMaterialsKeepsOtherRequestsMoving(t *testing.T) {
	ts, s, _ := newServer(t)
	saveAsset(t, s, "big", texturedGLB(t, stripes(2048, 2048), nil))
	start := time.Now()
	var list AssetMaterials
	if code := call(t, ts, "PUT", "/v1/assets/big/materials",
		MaterialsRequest{Materials: []MaterialSet{{Index: 0, Roughness: f64(0.1)}}}, &list); code != 200 {
		t.Fatalf("status %d", code)
	}
	t.Logf("2K texture: cluster, paint and rewrite took %v", time.Since(start))
	// While one edit runs, a health check is still answered at once.
	done := make(chan struct{})
	go func() {
		call(t, ts, "PUT", "/v1/assets/big/materials",
			MaterialsRequest{Materials: []MaterialSet{{Index: 1, Roughness: f64(0.3)}}}, nil)
		close(done)
	}()
	probe := time.Now()
	if code := call(t, ts, "GET", "/v1/health", nil, nil); code != 200 || time.Since(probe) > 2*time.Second {
		t.Errorf("health took %v during an edit", time.Since(probe))
	}
	<-done
}
