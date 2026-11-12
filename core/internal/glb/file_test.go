package glb

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// pngOf is a PNG of the given size, noisy so that its length depends on its size.
func pngOf(t testing.TB, w, h int, seed int64) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Intn(256))
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// binary is a binary chunk under construction: every blob starts on a multiple of four bytes.
type binary_ struct{ b []byte }

func (c *binary_) add(data []byte) (offset, length int) {
	for len(c.b)%4 != 0 {
		c.b = append(c.b, 0)
	}
	offset = len(c.b)
	c.b = append(c.b, data...)
	return offset, len(data)
}

func glbOf(t testing.TB, doc map[string]any, bin []byte) []byte {
	t.Helper()
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	for len(bin)%4 != 0 {
		bin = append(bin, 0)
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint32{0x46546C67, 2, uint32(12 + 8 + len(js) + 8 + len(bin))})
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(js)), chunkJSON})
	out.Write(js)
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(bin)), chunkBIN})
	out.Write(bin)
	return out.Bytes()
}

// textured is a rigged, three-level chest with a base color image, a metallic-roughness image
// shared with the occlusion slot (or none), and data after the images that must survive.
type textured struct {
	doc      map[string]any
	bin      []byte
	baseData []byte
	ormData  []byte
	tail     []byte
}

func chest(t testing.TB, withORM bool) textured {
	var c binary_
	geometry := bytes.Repeat([]byte{1, 2, 3, 4}, 30)
	gOff, gLen := c.add(geometry)
	baseData := pngOf(t, 40, 30, 1)
	bOff, bLen := c.add(baseData)
	var ormData []byte
	var oOff, oLen int
	if withORM {
		ormData = pngOf(t, 20, 20, 2)
		oOff, oLen = c.add(ormData)
	}
	tail := bytes.Repeat([]byte{9, 8, 7, 6}, 25)
	tOff, tLen := c.add(tail)

	doc := lods()
	doc["skins"] = []any{map[string]any{"joints": []int{0}, "extras": map[string]any{"forja_skeleton": "generic"}}}
	doc["nodes"].([]any)[1].(map[string]any)["skin"] = 0
	for i, m := range doc["meshes"].([]any) {
		for _, p := range m.(map[string]any)["primitives"].([]any) {
			p.(map[string]any)["material"] = 0
		}
		_ = i
	}
	views := []any{
		map[string]any{"buffer": 0, "byteOffset": gOff, "byteLength": gLen},
		map[string]any{"buffer": 0, "byteOffset": bOff, "byteLength": bLen},
	}
	images := []any{map[string]any{"bufferView": 1, "mimeType": "image/png", "name": "base"}}
	textures := []any{map[string]any{"source": 0, "sampler": 0}}
	pbr := map[string]any{"baseColorTexture": map[string]any{"index": 0, "texCoord": 1}, "roughnessFactor": 0.8, "metallicFactor": 0}
	material := map[string]any{"pbrMetallicRoughness": pbr, "extras": map[string]any{"keep": "me"}}
	if withORM {
		views = append(views, map[string]any{"buffer": 0, "byteOffset": oOff, "byteLength": oLen})
		images = append(images, map[string]any{"bufferView": 2, "mimeType": "image/png", "name": "orm"})
		textures = append(textures, map[string]any{"source": 1})
		pbr["metallicRoughnessTexture"] = map[string]any{"index": 1}
		material["occlusionTexture"] = map[string]any{"index": 1}
	}
	views = append(views, map[string]any{"buffer": 0, "byteOffset": tOff, "byteLength": tLen})
	doc["bufferViews"] = views
	doc["images"], doc["textures"] = images, textures
	doc["samplers"] = []any{map[string]any{"wrapS": 33071}}
	doc["materials"] = []any{material}
	doc["buffers"] = []any{map[string]any{"byteLength": len(c.b)}}
	// Accessors read the geometry and the data after the images.
	acc := doc["accessors"].([]any)
	acc[0] = map[string]any{"count": 1200, "bufferView": 0}
	acc[1] = map[string]any{"count": 6000, "bufferView": len(views) - 1}
	return textured{doc, c.b, baseData, ormData, tail}
}

func (c textured) glb(t testing.TB) []byte { return glbOf(t, c.doc, c.bin) }

// tree reads a JSON member of the file's document as plain values, to compare meaning.
func tree(t testing.TB, f *File, key string) any {
	t.Helper()
	var v any
	if raw, ok := f.doc[key]; ok {
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
	}
	return v
}

func mustParse(t testing.TB, data []byte) *File {
	t.Helper()
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// checkUntouched asserts what a change of images may not do: geometry, nodes, LODs, skins,
// meshes and accessors read the same, and every other bufferView holds the same bytes, aligned.
func checkUntouched(t *testing.T, before, after *File, changedViews ...int) {
	t.Helper()
	for _, key := range []string{"asset", "scene", "scenes", "nodes", "skins", "meshes", "accessors", "samplers", "extensionsRequired", "extensionsUsed"} {
		if !reflect.DeepEqual(tree(t, before, key), tree(t, after, key)) {
			t.Errorf("%s changed:\n%v\n%v", key, tree(t, before, key), tree(t, after, key))
		}
	}
	bv, _ := before.list("bufferViews")
	av, _ := after.list("bufferViews")
	changed := map[int]bool{}
	for _, c := range changedViews {
		changed[c] = true
	}
	for i := range bv {
		if changed[i] {
			continue
		}
		bb, bo, bl, _ := place(bv[i])
		ab, ao, al, _ := place(av[i])
		if bb != ab || bl != al {
			t.Errorf("view %d: buffer or length changed", i)
			continue
		}
		if bb == 0 {
			if !bytes.Equal(before.bin[bo:bo+bl], after.bin[ao:ao+al]) {
				t.Errorf("view %d: its bytes changed", i)
			}
			if ao%4 != 0 {
				t.Errorf("view %d starts at %d, not aligned", i, ao)
			}
		}
	}
	buffers, _ := after.list("buffers")
	if n, _ := get[int](buffers[0], "byteLength"); n > len(after.bin) || len(after.bin)-n >= 4 {
		t.Errorf("buffer length %d, binary chunk %d", n, len(after.bin))
	}
}

func imageBytes(t *testing.T, f *File, i int) []byte {
	t.Helper()
	data, kind, err := f.Image(i)
	if err != nil || kind != "png" {
		t.Fatalf("image %d: %q, %v", i, kind, err)
	}
	return data
}

func TestReplacingAnImageKeepsTheRestOfTheFile(t *testing.T) {
	for name, size := range map[string][2]int{"bigger": {37, 41}, "smaller": {5, 6}, "same": {20, 20}} {
		t.Run(name, func(t *testing.T) {
			c := chest(t, true)
			before := mustParse(t, c.glb(t))
			f := mustParse(t, c.glb(t))
			replacement := pngOf(t, size[0], size[1], 99)
			if err := f.SetMetallicRoughnessMap([]int{0}, replacement); err != nil {
				t.Fatal(err)
			}
			data, err := f.Bytes()
			if err != nil {
				t.Fatal(err)
			}
			after := mustParse(t, data)
			// View 2 is the metallic-roughness image; the others did not change.
			checkUntouched(t, before, after, 2)
			if got := imageBytes(t, after, 1); !bytes.Equal(got, replacement) {
				t.Error("the replaced image is not what was given")
			}
			if got := imageBytes(t, after, 0); !bytes.Equal(got, c.baseData) {
				t.Error("the base color changed")
			}
			// Occlusion and metallic-roughness still share the one texture.
			mats, _ := after.Materials()
			if mats[0].MetallicRoughness == nil || mats[0].MetallicRoughness.Image != 1 || mats[0].Roughness != 1 || mats[0].Metallic != 1 {
				t.Errorf("material %+v", mats[0])
			}
			if occ := tree(t, after, "materials").([]any)[0].(map[string]any)["occlusionTexture"]; occ == nil {
				t.Error("the occlusion texture went away")
			}
			if extras := tree(t, after, "materials").([]any)[0].(map[string]any)["extras"]; !reflect.DeepEqual(extras, map[string]any{"keep": "me"}) {
				t.Errorf("material extras %v", extras)
			}
			// Written again and read again, nothing moves.
			again, err := mustParse(t, data).Bytes()
			if err != nil || !bytes.Equal(again, data) {
				t.Errorf("a second round trip changed the file (%v)", err)
			}
			if st, err := after.Stats(); err != nil || st.Triangles != 2300 || st.LODs != 2 {
				t.Errorf("stats %+v, %v", st, err)
			}
		})
	}
}

func TestAddingAMetallicRoughnessMap(t *testing.T) {
	c := chest(t, false)
	before := mustParse(t, c.glb(t))
	f := mustParse(t, c.glb(t))
	added := pngOf(t, 16, 16, 7)
	if err := f.SetMetallicRoughnessMap([]int{0}, added); err != nil {
		t.Fatal(err)
	}
	data, _ := f.Bytes()
	after := mustParse(t, data)
	checkUntouched(t, before, after)

	mats, _ := after.Materials()
	mr := mats[0].MetallicRoughness
	if mr == nil || mr.Image != 1 || mr.Texture != 1 {
		t.Fatalf("material %+v", mats[0])
	}
	if got := imageBytes(t, after, 1); !bytes.Equal(got, added) {
		t.Error("the added image is not what was given")
	}
	mat := tree(t, after, "materials").([]any)[0].(map[string]any)
	if _, has := mat["occlusionTexture"]; has {
		t.Error("an occlusion texture appeared")
	}
	info := mat["pbrMetallicRoughness"].(map[string]any)["metallicRoughnessTexture"].(map[string]any)
	// Read through the base color's coordinates, with its sampler.
	if info["index"] != 1.0 || info["texCoord"] != 1.0 {
		t.Errorf("texture info %v", info)
	}
	if tex := tree(t, after, "textures").([]any)[1].(map[string]any); tex["sampler"] != 0.0 || tex["source"] != 1.0 {
		t.Errorf("texture %v", tex)
	}
	// The new image sits after everything else, aligned.
	views, _ := after.list("bufferViews")
	if off, _ := get[int](views[len(views)-1], "byteOffset"); off%4 != 0 || off < len(before.bin) {
		t.Errorf("new view at %d", off)
	}
}

func TestSeveralMaterialsOfOneTextureShareTheMap(t *testing.T) {
	c := chest(t, false)
	second := map[string]any{"pbrMetallicRoughness": map[string]any{"baseColorTexture": map[string]any{"index": 0}}}
	c.doc["materials"] = append(c.doc["materials"].([]any), second)
	f := mustParse(t, c.glb(t))
	if err := f.SetMetallicRoughnessMap([]int{0, 1}, pngOf(t, 8, 8, 3)); err != nil {
		t.Fatal(err)
	}
	mats, _ := f.Materials()
	if mats[0].MetallicRoughness == nil || mats[1].MetallicRoughness == nil || *mats[0].MetallicRoughness != *mats[1].MetallicRoughness {
		t.Fatalf("materials %+v %+v", mats[0], mats[1])
	}
	if len(tree(t, f, "images").([]any)) != 2 {
		t.Errorf("images %v", tree(t, f, "images"))
	}
}

// meshoptChest is how gltfpack writes a compressed file: the geometry views read a fallback
// buffer that holds no data, and an extension says where the compressed bytes are.
func meshoptChest(t testing.TB) (map[string]any, []byte, int) {
	var c binary_
	packed := bytes.Repeat([]byte{0xAA, 0xBB}, 40)
	pOff, pLen := c.add(packed)
	data := pngOf(t, 30, 30, 5)
	iOff, iLen := c.add(data)
	more := bytes.Repeat([]byte{0xCC}, 52)
	mOff, mLen := c.add(more)
	doc := lods()
	meshoptView := func(off, length int) map[string]any {
		return map[string]any{
			"buffer": 1, "byteOffset": 0, "byteLength": length * 4,
			"extensions": map[string]any{meshopt: map[string]any{
				"buffer": 0, "byteOffset": off, "byteLength": length, "byteStride": 12, "mode": "ATTRIBUTES", "count": length / 2,
			}},
		}
	}
	doc["bufferViews"] = []any{
		meshoptView(pOff, pLen),
		map[string]any{"buffer": 0, "byteOffset": iOff, "byteLength": iLen},
		meshoptView(mOff, mLen),
	}
	doc["buffers"] = []any{
		map[string]any{"byteLength": len(c.b)},
		map[string]any{"byteLength": 1000, "extensions": map[string]any{meshopt: map[string]any{"fallback": true}}},
	}
	doc["extensionsUsed"] = []string{meshopt}
	doc["extensionsRequired"] = []string{meshopt}
	doc["images"] = []any{map[string]any{"bufferView": 1, "mimeType": "image/png"}}
	doc["textures"] = []any{map[string]any{"source": 0}}
	doc["materials"] = []any{map[string]any{"pbrMetallicRoughness": map[string]any{"baseColorTexture": map[string]any{"index": 0}}}}
	for _, m := range doc["meshes"].([]any) {
		for _, p := range m.(map[string]any)["primitives"].([]any) {
			p.(map[string]any)["material"] = 0
		}
	}
	return doc, c.b, len(more)
}

func TestMeshoptFilesSurviveTheirImageChanging(t *testing.T) {
	doc, bin, _ := meshoptChest(t)
	for name, size := range map[string]int{"a bigger base color": 50, "a smaller one": 5} {
		t.Run(name, func(t *testing.T) {
			// Added: appended after the compressed views.
			f := mustParse(t, glbOf(t, doc, bin))
			before := mustParse(t, glbOf(t, doc, bin))
			if err := f.SetMetallicRoughnessMap([]int{0}, pngOf(t, size, size, 1)); err != nil {
				t.Fatal(err)
			}
			data, _ := f.Bytes()
			after := mustParse(t, data)
			checkUntouched(t, before, after)
			// Replaced in place: the compressed view after the image moves with it.
			if err := after.SetMetallicRoughnessMap([]int{0}, pngOf(t, size+9, size+9, 2)); err != nil {
				t.Fatal(err)
			}
			second, _ := after.Bytes()
			final := mustParse(t, second)
			checkUntouched(t, before, final, 3)
		})
	}
	// The file still reads, and still says it needs the extension.
	f := mustParse(t, glbOf(t, doc, bin))
	if err := f.SetMetallicRoughnessMap([]int{0}, pngOf(t, 40, 40, 1)); err != nil {
		t.Fatal(err)
	}
	data, _ := f.Bytes()
	g := mustParse(t, data)
	if st, err := g.Stats(); err != nil || st.Triangles != 2300 {
		t.Fatalf("stats %+v, %v", st, err)
	}
	if !strings.Contains(string(g.doc["extensionsRequired"]), meshopt) {
		t.Error("the required extension is gone")
	}
}

func TestAnImageBeforeCompressedDataMovesTheDataWithIt(t *testing.T) {
	// The metallic-roughness image sits between two compressed views; growing it moves the second.
	var c binary_
	packed := bytes.Repeat([]byte{0xAA}, 40)
	pOff, pLen := c.add(packed)
	base := pngOf(t, 30, 30, 5)
	bOff, bLen := c.add(base)
	orm := pngOf(t, 10, 10, 6)
	oOff, oLen := c.add(orm)
	more := bytes.Repeat([]byte{0xCC}, 52)
	mOff, mLen := c.add(more)
	doc, _, _ := meshoptChest(t)
	view := func(off, length int) map[string]any {
		return map[string]any{
			"buffer": 1, "byteLength": length * 2,
			"extensions": map[string]any{meshopt: map[string]any{"buffer": 0, "byteOffset": off, "byteLength": length, "byteStride": 4, "mode": "ATTRIBUTES", "count": length / 4}},
		}
	}
	doc["bufferViews"] = []any{
		view(pOff, pLen),
		map[string]any{"buffer": 0, "byteOffset": bOff, "byteLength": bLen},
		map[string]any{"buffer": 0, "byteOffset": oOff, "byteLength": oLen},
		view(mOff, mLen),
	}
	doc["buffers"].([]any)[0] = map[string]any{"byteLength": len(c.b)}
	doc["images"] = []any{map[string]any{"bufferView": 1, "mimeType": "image/png"}, map[string]any{"bufferView": 2, "mimeType": "image/png"}}
	doc["textures"] = []any{map[string]any{"source": 0}, map[string]any{"source": 1}}
	doc["materials"].([]any)[0].(map[string]any)["pbrMetallicRoughness"].(map[string]any)["metallicRoughnessTexture"] = map[string]any{"index": 1}
	original := glbOf(t, doc, c.b)
	for _, side := range []int{3, 90} {
		f := mustParse(t, original)
		before := mustParse(t, original)
		next := pngOf(t, side, side, 8)
		if err := f.SetMetallicRoughnessMap([]int{0}, next); err != nil {
			t.Fatal(err)
		}
		data, _ := f.Bytes()
		after := mustParse(t, data)
		checkUntouched(t, before, after, 2)
		if got := imageBytes(t, after, 1); !bytes.Equal(got, next) {
			t.Errorf("side %d: image differs", side)
		}
		views, _ := after.list("bufferViews")
		_, off, length, _ := place(views[3])
		if !bytes.Equal(after.bin[off:off+length], more) {
			t.Errorf("side %d: the compressed view moved without its bytes", side)
		}
		if (off-mOff)%4 != 0 {
			t.Errorf("side %d: shifted by %d", side, off-mOff)
		}
	}
}

func TestAViewTwoThingsReadIsNeverOverwritten(t *testing.T) {
	// Two images read one view: replacing one adds a view instead of changing the other's.
	c := chest(t, true)
	c.doc["images"] = append(c.doc["images"].([]any), map[string]any{"bufferView": 2, "mimeType": "image/png"})
	f := mustParse(t, c.glb(t))
	replacement := pngOf(t, 12, 12, 4)
	if err := f.SetMetallicRoughnessMap([]int{0}, replacement); err != nil {
		t.Fatal(err)
	}
	data, _ := f.Bytes()
	after := mustParse(t, data)
	if got := imageBytes(t, after, 2); !bytes.Equal(got, c.ormData) {
		t.Error("the image that shared the view changed")
	}
	if got := imageBytes(t, after, 1); !bytes.Equal(got, replacement) {
		t.Error("the replacement was not written")
	}
}

func TestImagesThatAreNotPNGOrJPEGAreRefused(t *testing.T) {
	c := chest(t, false)
	ktx := append([]byte("\xABKTX 20\xBB\r\n\x1a\n"), make([]byte, 40)...)
	var b binary_
	b.b = c.bin
	off, n := b.add(ktx)
	views := c.doc["bufferViews"].([]any)
	c.doc["bufferViews"] = append(views, map[string]any{"buffer": 0, "byteOffset": off, "byteLength": n})
	c.doc["images"] = append(c.doc["images"].([]any), map[string]any{"bufferView": len(views), "mimeType": "image/ktx2"})
	f := mustParse(t, glbOf(t, c.doc, b.b))
	_, _, err := f.Image(1)
	var format *ImageFormatError
	if !errors.As(err, &format) || format.Format != "KTX2" || !strings.Contains(err.Error(), "PNG and JPEG") {
		t.Fatalf("err %v", err)
	}
	// A map that has a KTX2 copy beside its PNG would keep showing the copy.
	c = chest(t, true)
	c.doc["textures"].([]any)[1] = map[string]any{"source": 1, "extensions": map[string]any{"KHR_texture_basisu": map[string]any{"source": 0}}}
	f = mustParse(t, c.glb(t))
	if err := f.SetMetallicRoughnessMap([]int{0}, pngOf(t, 4, 4, 1)); !errors.As(err, &format) {
		t.Fatalf("err %v", err)
	}
	// A texture that has only the extension's source names that image.
	c = chest(t, false)
	c.doc["textures"].([]any)[0] = map[string]any{"extensions": map[string]any{"EXT_texture_webp": map[string]any{"source": 0}}}
	f = mustParse(t, c.glb(t))
	mats, _ := f.Materials()
	if mats[0].BaseColor == nil || mats[0].BaseColor.Image != 0 {
		t.Fatalf("material %+v", mats[0])
	}
}

func TestImagesInDataURIsAreRead(t *testing.T) {
	c := chest(t, false)
	data := pngOf(t, 6, 6, 1)
	c.doc["images"] = []any{map[string]any{"uri": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}}
	f := mustParse(t, c.glb(t))
	if got := imageBytes(t, f, 0); !bytes.Equal(got, data) {
		t.Error("data URI image differs")
	}
	// Replacing it moves it into the binary chunk.
	if err := f.SetMetallicRoughnessMap([]int{0}, pngOf(t, 4, 4, 2)); err != nil {
		t.Fatal(err)
	}
	c.doc["images"] = []any{map[string]any{"uri": "textures/base.png"}}
	if _, _, err := mustParse(t, c.glb(t)).Image(0); err == nil || !strings.Contains(err.Error(), "separate file") {
		t.Errorf("err %v", err)
	}
}

func TestMaterialsReportTextureFactorsAndUse(t *testing.T) {
	c := chest(t, false)
	c.doc["materials"] = append(c.doc["materials"].([]any), map[string]any{}, map[string]any{
		"pbrMetallicRoughness": map[string]any{"baseColorFactor": []float64{1, 0, 0, 1}, "roughnessFactor": 0.25},
	})
	f := mustParse(t, c.glb(t))
	mats, err := f.Materials()
	if err != nil || len(mats) != 3 {
		t.Fatalf("%v %v", mats, err)
	}
	if !mats[0].Used || mats[0].BaseColor == nil || mats[0].Roughness != 0.8 || mats[0].Metallic != 0 {
		t.Errorf("0: %+v", mats[0])
	}
	// glTF defaults both factors to 1.
	if mats[1].Used || mats[1].Roughness != 1 || mats[1].Metallic != 1 || mats[1].BaseColor != nil {
		t.Errorf("1: %+v", mats[1])
	}
	if mats[2].Roughness != 0.25 || mats[2].Metallic != 1 {
		t.Errorf("2: %+v", mats[2])
	}
}

func TestParseKeepsExtraChunksAndRefusesBrokenFiles(t *testing.T) {
	c := chest(t, false)
	raw := c.glb(t)
	raw = append(raw, 4, 0, 0, 0, 0x78, 0x56, 0x34, 0x12, 1, 2, 3, 4)
	binary.LittleEndian.PutUint32(raw[8:], uint32(len(raw)))
	f := mustParse(t, raw)
	out, err := f.Bytes()
	if err != nil || len(f.more) != 1 || !bytes.HasSuffix(out, []byte{4, 0, 0, 0, 0x78, 0x56, 0x34, 0x12, 1, 2, 3, 4}) {
		t.Fatalf("extra chunk lost (%v)", err)
	}
	for name, data := range map[string][]byte{
		"empty": nil, "text": []byte("not a glb at all, really not"), "cut": raw[:40],
	} {
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Parse(append([]byte("glTF"), make([]byte, 30)...)); err == nil {
		t.Error("version 0 accepted")
	}
}

func TestCeilTo4(t *testing.T) {
	for in, want := range map[int]int{0: 0, 1: 4, 4: 4, 5: 8, -1: 0, -3: 0, -4: -4, -5: -4, -8: -8, -9: -8} {
		if got := ceilTo4(in); got != want {
			t.Errorf("ceilTo4(%d) = %d, want %d", in, got, want)
		}
	}
}
