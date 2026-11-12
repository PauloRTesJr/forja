package materials

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"forja/internal/glb"
)

var (
	wood = color.RGBA{139, 90, 43, 255}
	iron = color.RGBA{96, 98, 104, 255}
	gold = color.RGBA{212, 175, 55, 255}
)

// bands is a texture of vertical stripes: each color takes the given share of the width.
func bands(w, h int, colors []color.RGBA, shares []float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		c, edge, acc := colors[len(colors)-1], 0.0, 0.0
		for i, s := range shares {
			acc += s
			if float64(x) < acc*float64(w) {
				c = colors[i]
				break
			}
		}
		_ = edge
		for y := 0; y < h; y++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestThreeFlatColorsGiveThreeMaterialsWithTheirShares(t *testing.T) {
	img := bands(512, 512, []color.RGBA{iron, wood, gold}, []float64{0.2, 0.5, 0.3})
	got := Find(img)
	if len(got) != 3 {
		t.Fatalf("%d materials: %+v", len(got), got)
	}
	// Largest share first.
	for i, want := range []struct {
		hex   string
		share float64
	}{{"#8b5a2b", 0.5}, {"#d4af37", 0.3}, {"#606268", 0.2}} {
		if !hexNear(got[i].Hex(), want.hex) || math.Abs(got[i].Share-want.share) > 0.01 {
			t.Errorf("material %d: %s %.3f, want %s %.2f", i, got[i].Hex(), got[i].Share, want.hex, want.share)
		}
	}
}

func TestFindIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	img := bands(640, 480, []color.RGBA{iron, wood, gold}, []float64{0.3, 0.4, 0.3})
	// Noise: every texel moves a little, so the search has real work to do.
	for i := range img.Pix {
		if i%4 != 3 {
			img.Pix[i] = uint8(max(0, min(255, int(img.Pix[i])+rng.Intn(21)-10)))
		}
	}
	first := Find(img)
	for i := 0; i < 5; i++ {
		if again := Find(img); !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
	if len(first) != 3 {
		t.Errorf("noisy texture: %d materials: %+v", len(first), first)
	}
}

func TestSimilarColorsAreOneMaterialAndSpecksAreFolded(t *testing.T) {
	near := color.RGBA{wood.R + 6, wood.G + 4, wood.B + 3, 255}
	img := bands(400, 400, []color.RGBA{wood, near, iron}, []float64{0.3, 0.3, 0.4})
	if got := Find(img); len(got) != 2 {
		t.Errorf("close colors: %d materials: %+v", len(got), got)
	}
	// A few texels of another color are not a material.
	img = bands(400, 400, []color.RGBA{wood, iron}, []float64{0.5, 0.5})
	for x := 0; x < 400; x += 20 {
		img.SetRGBA(x, 7, gold)
	}
	if got := Find(img); len(got) != 2 {
		t.Errorf("specks: %d materials: %+v", len(got), got)
	}
	// One flat color is one material owning all of it.
	got := Find(bands(64, 64, []color.RGBA{gold}, []float64{1}))
	if len(got) != 1 || got[0].Share != 1 || got[0].Hex() != "#d4af37" {
		t.Errorf("flat: %+v", got)
	}
	// No more than six, however many colors there are.
	rng := rand.New(rand.NewSource(1))
	many := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for i := 0; i < len(many.Pix); i += 4 {
		many.Pix[i], many.Pix[i+1], many.Pix[i+2], many.Pix[i+3] = uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255
	}
	if got := Find(many); len(got) > MaxClusters || len(got) == 0 {
		t.Errorf("%d materials", len(got))
	}
}

func TestPaintWritesEachMaterialsValuesAndKeepsTheRedChannel(t *testing.T) {
	img := bands(256, 64, []color.RGBA{wood, iron}, []float64{0.5, 0.5})
	clusters := Find(img)
	if len(clusters) != 2 {
		t.Fatalf("%+v", clusters)
	}
	// Index 0 is whichever is larger; both are half, so find wood by color.
	values := make([]Value, 2)
	for i, c := range clusters {
		if c.RGB[0] > 120 {
			values[i] = Value{Roughness: 0.9, Metallic: 0}
		} else {
			values[i] = Value{Roughness: 0.3, Metallic: 1}
		}
	}
	occlusion := image.NewRGBA(image.Rect(0, 0, 256, 64))
	for i := 0; i < len(occlusion.Pix); i += 4 {
		occlusion.Pix[i], occlusion.Pix[i+1], occlusion.Pix[i+2], occlusion.Pix[i+3] = uint8(i/4%200), 255, 255, 255
	}
	out := Paint(img, occlusion, clusters, values)
	px := func(x, y int) color.RGBA { return out.RGBAAt(x, y) }
	if g, b := px(40, 30).G, px(40, 30).B; g != 230 || b != 0 {
		t.Errorf("wood: G %d B %d, want 230 0", g, b)
	}
	if g, b := px(220, 30).G, px(220, 30).B; g != 77 || b != 255 {
		t.Errorf("iron: G %d B %d, want 77 255", g, b)
	}
	for x := 0; x < 256; x++ {
		if px(x, 0).R != uint8(x%200) {
			t.Fatalf("red changed at %d: %d", x, px(x, 0).R)
		}
	}
	// The border between them is smoothed, not a step.
	step := 0
	for x := 120; x < 136; x++ {
		if g := int(px(x, 30).G); g != 230 && g != 77 {
			step++
		}
	}
	if step == 0 {
		t.Error("the border between materials is a hard step")
	}
	// Without an existing map the red channel is full.
	if bare := Paint(img, nil, clusters, values); bare.RGBAAt(5, 5).R != 255 || bare.Bounds() != img.Bounds() {
		t.Errorf("bare map %v %v", bare.RGBAAt(5, 5), bare.Bounds())
	}
	// A map of another size is painted at its own size, from the base color scaled to it.
	small := image.NewRGBA(image.Rect(0, 0, 32, 8))
	if got := Paint(img, small, clusters, values); got.Bounds() != small.Bounds() || got.RGBAAt(4, 4).G != 230 {
		t.Errorf("small map %v %v", got.Bounds(), got.RGBAAt(4, 4))
	}
	if again := Paint(img, occlusion, clusters, values); !bytes.Equal(again.Pix, out.Pix) {
		t.Error("painting twice gives different maps")
	}
}

func TestVariationTellsAFlatMapFromAPaintedOne(t *testing.T) {
	flat := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(flat.Pix); i += 4 {
		flat.Pix[i], flat.Pix[i+1], flat.Pix[i+2], flat.Pix[i+3] = 10, 255, 128, 255
	}
	spread, g, b := variation(flat)
	if spread != 0 || g != 1 || math.Abs(b-128.0/255) > 1e-9 {
		t.Errorf("flat: %d %v %v", spread, g, b)
	}
	flat.Pix[5] = 100
	if spread, _, _ := variation(flat); spread != 155 {
		t.Errorf("painted: %d", spread)
	}
}

// ---- GLB fixtures

type gltfBuilder struct {
	bin   []byte
	views []any
}

func (b *gltfBuilder) view(data []byte) int {
	for len(b.bin)%4 != 0 {
		b.bin = append(b.bin, 0)
	}
	b.views = append(b.views, map[string]any{"buffer": 0, "byteOffset": len(b.bin), "byteLength": len(data)})
	b.bin = append(b.bin, data...)
	return len(b.views) - 1
}

func encode(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// asset is a one-mesh GLB with a base color texture and, when orm is not nil, a
// metallic-roughness map shared with the occlusion slot.
func asset(t testing.TB, base, orm image.Image) []byte {
	var b gltfBuilder
	b.view([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	baseView := b.view(encode(t, base))
	images := []any{map[string]any{"bufferView": baseView, "mimeType": "image/png"}}
	textures := []any{map[string]any{"source": 0}}
	pbr := map[string]any{"baseColorTexture": map[string]any{"index": 0}, "roughnessFactor": 0.8, "metallicFactor": 0}
	mat := map[string]any{"pbrMetallicRoughness": pbr}
	if orm != nil {
		images = append(images, map[string]any{"bufferView": b.view(encode(t, orm)), "mimeType": "image/png"})
		textures = append(textures, map[string]any{"source": 1})
		pbr["metallicRoughnessTexture"] = map[string]any{"index": 1}
		mat["occlusionTexture"] = map[string]any{"index": 1}
	}
	b.view([]byte{9, 9, 9, 9})
	doc := map[string]any{
		"asset": map[string]string{"version": "2.0"}, "scene": 0, "scenes": []any{map[string]any{"nodes": []int{0}}},
		"nodes":     []any{map[string]any{"name": "crate", "mesh": 0}},
		"meshes":    []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0}, "material": 0}}}},
		"accessors": []any{map[string]any{"count": 3, "bufferView": 0}},
		"materials": []any{mat}, "textures": textures, "images": images,
		"bufferViews": b.views, "buffers": []any{map[string]any{"byteLength": len(b.bin)}},
	}
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	for len(b.bin)%4 != 0 {
		b.bin = append(b.bin, 0)
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint32{0x46546C67, 2, uint32(12 + 8 + len(js) + 8 + len(b.bin))})
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(js)), 0x4E4F534A})
	out.Write(js)
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(b.bin)), 0x004E4942})
	out.Write(b.bin)
	return out.Bytes()
}

func parse(t testing.TB, data []byte) *glb.File {
	t.Helper()
	f, err := glb.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// mapOf is the metallic-roughness image of the file's only material.
func mapOf(t testing.TB, data []byte) *image.RGBA {
	t.Helper()
	f := parse(t, data)
	mats, err := f.Materials()
	if err != nil || mats[0].MetallicRoughness == nil {
		t.Fatalf("no map: %v %+v", err, mats)
	}
	raw, _, err := f.Image(mats[0].MetallicRoughness.Image)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return pixels(img)
}

func ptr(v float64) *float64 { return &v }

// hexNear says two #rrggbb colors are within 3 steps per channel: a block of texels that
// straddles two stripes pulls a material's average color a little.
func hexNear(a, b string) bool {
	var ar, ag, ab, br, bg, bb int
	if _, err := fmt.Sscanf(a, "#%02x%02x%02x", &ar, &ag, &ab); err != nil {
		return false
	}
	if _, err := fmt.Sscanf(b, "#%02x%02x%02x", &br, &bg, &bb); err != nil {
		return false
	}
	d := func(x, y int) bool { return x-y <= 3 && y-x <= 3 }
	return d(ar, br) && d(ag, bg) && d(ab, bb)
}

func TestAnalyzeAndEditAGLBWithoutAMap(t *testing.T) {
	base := bands(256, 256, []color.RGBA{wood, iron, gold}, []float64{0.5, 0.3, 0.2})
	file := parse(t, asset(t, base, nil))
	a, err := Analyze(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Materials) != 3 || a.Painted {
		t.Fatalf("%+v", a)
	}
	// Nothing was set: every material shows the file's constant factors.
	for i, m := range a.Materials {
		if m.Index != i || m.Roughness != 0.8 || m.Metallic != 0 || m.Texture != 0 {
			t.Errorf("material %d: %+v", i, m)
		}
	}
	if !hexNear(a.Materials[0].Color, "#8b5a2b") {
		t.Errorf("colors %+v", a.Materials)
	}
	// The iron: find which index it is.
	ironAt := 1
	if hexNear(a.Materials[2].Color, "#606268") {
		ironAt = 2
	}
	out, choices, err := a.Edit([]Set{{Index: ironAt, Roughness: ptr(0.35), Metallic: ptr(1)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	// All three are stored, with the ones not named keeping their values.
	want := []Choice{{0, 0.8, 0}, {1, 0.8, 0}, {2, 0.8, 0}}
	want[ironAt] = Choice{ironAt, 0.35, 1}
	if !reflect.DeepEqual(choices, want) {
		t.Errorf("choices %+v, want %+v", choices, want)
	}
	m := mapOf(t, out)
	if m.Bounds() != base.Bounds() {
		t.Errorf("map is %v, base color %v", m.Bounds(), base.Bounds())
	}
	ironX := 166 // middle of the iron stripe (50% to 80% of 256 is 128 to 205)
	woodX := 40
	if px := m.RGBAAt(ironX, 100); px.R != 255 || px.G != 89 || px.B != 255 {
		t.Errorf("iron texel %v", px)
	}
	if px := m.RGBAAt(woodX, 100); px.R != 255 || px.G != 204 || px.B != 0 {
		t.Errorf("wood texel %v", px)
	}
	// The material now takes its values from the map.
	f := parse(t, out)
	mats, _ := f.Materials()
	if mats[0].Roughness != 1 || mats[0].Metallic != 1 {
		t.Errorf("factors %+v", mats[0])
	}
	// Read back with the stored choices, the file shows them and is no one's painting.
	b, err := Analyze(f, choices)
	if err != nil || b.Painted {
		t.Fatalf("%v %+v", err, b)
	}
	if b.Materials[ironAt].Roughness != 0.35 || b.Materials[ironAt].Metallic != 1 {
		t.Errorf("after: %+v", b.Materials)
	}
	// Editing it again works without overwrite, and omitted fields keep their values.
	out2, _, err := b.Edit([]Set{{Index: ironAt, Metallic: ptr(0.5)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if px := mapOf(t, out2).RGBAAt(ironX, 100); px.G != 89 || px.B != 128 {
		t.Errorf("iron texel after a second edit %v", px)
	}
	// Same input, same bytes.
	again, _ := Analyze(parse(t, asset(t, base, nil)), nil)
	out3, _, _ := again.Edit([]Set{{Index: ironAt, Roughness: ptr(0.35), Metallic: ptr(1)}}, false)
	if !bytes.Equal(out, out3) {
		t.Error("the same edit of the same file gave different bytes")
	}
}

func TestAFlatMapShowsItsValuesTimesTheFactors(t *testing.T) {
	base := bands(64, 64, []color.RGBA{wood, iron}, []float64{0.5, 0.5})
	orm := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := 0; i < len(orm.Pix); i += 4 {
		orm.Pix[i], orm.Pix[i+1], orm.Pix[i+2], orm.Pix[i+3] = 200, 255, 255, 255
	}
	a, err := Analyze(parse(t, asset(t, base, orm)), nil)
	if err != nil || a.Painted {
		t.Fatalf("%v %+v", err, a)
	}
	if a.Materials[0].Roughness != 0.8 || a.Materials[0].Metallic != 0 {
		t.Errorf("%+v", a.Materials)
	}
	// Editing keeps the occlusion in red, at the map's own size.
	out, _, err := a.Edit([]Set{{Index: 0, Roughness: ptr(0.1)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	m := mapOf(t, out)
	if m.Bounds() != orm.Bounds() || m.RGBAAt(3, 3).R != 200 {
		t.Errorf("map %v, red %d", m.Bounds(), m.RGBAAt(3, 3).R)
	}
}

func TestAMapThatVariesWasPaintedByItsModel(t *testing.T) {
	base := bands(64, 64, []color.RGBA{wood, iron}, []float64{0.5, 0.5})
	orm := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			orm.SetRGBA(x, y, color.RGBA{255, uint8(x * 4), uint8(255 - y*4), 255})
		}
	}
	data := asset(t, base, orm)
	a, err := Analyze(parse(t, data), nil)
	if err != nil || !a.Painted {
		t.Fatalf("%v painted=%v", err, a != nil && a.Painted)
	}
	set := []Set{{Index: 0, Roughness: ptr(0.5)}}
	if _, _, err := a.Edit(set, false); !errors.Is(err, ErrPainted) {
		t.Fatalf("err %v, want ErrPainted", err)
	}
	out, choices, err := a.Edit(set, true)
	if err != nil || len(choices) != len(a.Materials) {
		t.Fatalf("%v %+v", err, choices)
	}
	// Overwritten, the map is ours: it varies by material, and the stored choices say so.
	b, err := Analyze(parse(t, out), choices)
	if err != nil || b.Painted {
		t.Fatalf("%v painted=%v", err, b != nil && b.Painted)
	}
	// Without them it would look painted again, which is why they are stored.
	c, _ := Analyze(parse(t, out), nil)
	if !c.Painted {
		t.Error("a map written with two different values should look painted without its stored choices")
	}
}

func TestEditRefusesWhatItCannotDo(t *testing.T) {
	base := bands(64, 64, []color.RGBA{wood, iron}, []float64{0.5, 0.5})
	a, _ := Analyze(parse(t, asset(t, base, nil)), nil)
	var in *InputError
	for name, sets := range map[string][]Set{
		"nothing":      nil,
		"unknown":      {{Index: 5, Roughness: ptr(0.5)}},
		"negative":     {{Index: -1, Roughness: ptr(0.5)}},
		"too rough":    {{Index: 0, Roughness: ptr(1.2)}},
		"too smooth":   {{Index: 0, Metallic: ptr(-0.1)}},
		"not a number": {{Index: 0, Metallic: ptr(math.NaN())}},
		"twice":        {{Index: 0, Roughness: ptr(0.5)}, {Index: 0, Metallic: ptr(0.5)}},
		"no values":    {{Index: 0}},
	} {
		if _, _, err := a.Edit(sets, false); !errors.As(err, &in) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	// No base color texture, no materials.
	data := asset(t, base, nil)
	f := parse(t, data)
	f2, _ := glb.Parse(bytes.ReplaceAll(data, []byte(`"baseColorTexture":{"index":0},`), []byte(`"baseColorTextur_":{"index":0},`)))
	_ = f
	if _, err := Analyze(f2, nil); !errors.Is(err, ErrNoTexture) {
		t.Errorf("err %v, want ErrNoTexture", err)
	}
}

func BenchmarkEdit2K(b *testing.B) {
	base := bands(2048, 2048, []color.RGBA{wood, iron, gold}, []float64{0.5, 0.3, 0.2})
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < len(base.Pix); i += 4 {
		for k := 0; k < 3; k++ {
			base.Pix[i+k] = uint8(max(0, min(255, int(base.Pix[i+k])+rng.Intn(9)-4)))
		}
	}
	data := asset(b, base, nil)
	b.ResetTimer()
	for b.Loop() {
		f, err := glb.Parse(data)
		if err != nil {
			b.Fatal(err)
		}
		a, err := Analyze(f, nil)
		if err != nil {
			b.Fatal(err)
		}
		if _, _, err := a.Edit([]Set{{Index: 0, Roughness: ptr(0.4)}}, false); err != nil {
			b.Fatal(err)
		}
	}
}
