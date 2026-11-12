// Package glbtest builds small textured GLBs for tests.
package glbtest

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// PNG encodes img.
func PNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Stripes is a texture of three vertical bands: wood (40%), iron (40%) and gold (20%).
func Stripes(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cols := []color.RGBA{{139, 90, 43, 255}, {96, 98, 104, 255}, {212, 175, 55, 255}}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, cols[min(2, x*10/w/4)])
		}
	}
	return img
}

// Textured is a one-triangle GLB whose only material has base as its base color texture
// and, when orm is not nil, orm as a metallic-roughness map shared with the occlusion slot.
// The material's factors are 0.8 roughness and 0 metallic, as the engine writes them.
func Textured(t testing.TB, base, orm image.Image) []byte {
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
	images := []any{map[string]any{"bufferView": add(PNG(t, base)), "mimeType": "image/png"}}
	textures := []any{map[string]any{"source": 0}}
	pbr := map[string]any{"baseColorTexture": map[string]any{"index": 0}, "roughnessFactor": 0.8, "metallicFactor": 0}
	mat := map[string]any{"pbrMetallicRoughness": pbr}
	if orm != nil {
		images = append(images, map[string]any{"bufferView": add(PNG(t, orm)), "mimeType": "image/png"})
		textures = append(textures, map[string]any{"source": 1})
		pbr["metallicRoughnessTexture"] = map[string]any{"index": 1}
		mat["occlusionTexture"] = map[string]any{"index": 1}
	}
	add([]byte{9, 9, 9, 9})
	doc := map[string]any{
		"asset": map[string]string{"version": "2.0"}, "scene": 0, "scenes": []any{map[string]any{"nodes": []int{0}}},
		"nodes":     []any{map[string]any{"name": "crate", "mesh": 0}},
		"meshes":    []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0}, "material": 0}}}},
		"accessors": []any{map[string]any{"count": 3, "bufferView": 0}},
		"materials": []any{mat}, "textures": textures, "images": images,
		"bufferViews": views, "buffers": []any{map[string]any{"byteLength": len(bin)}},
	}
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
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(js)), 0x4E4F534A})
	out.Write(js)
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(bin)), 0x004E4942})
	out.Write(bin)
	return out.Bytes()
}
