package glb

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	chunkJSON = 0x4E4F534A
	chunkBIN  = 0x004E4942
)

// File is a whole GLB held in memory: its JSON document and its binary chunk. It reads the
// images of an asset's materials and writes a file that differs from the input only in them.
// Geometry stays as it is (compressed or not): images live in bufferViews of their own, and
// everything else the document says is carried over as written.
type File struct {
	doc map[string]json.RawMessage
	bin []byte
	// more are the chunks after the binary one, kept as they came.
	more []chunk
}

type chunk struct {
	kind uint32
	data []byte
}

// ImageFormatError is an image Forja cannot edit: only PNG and JPEG textures can be changed.
type ImageFormatError struct{ Format string }

func (e *ImageFormatError) Error() string {
	return "this asset's texture is stored as " + e.Format + ", and Forja edits PNG and JPEG textures only"
}

// Load reads the GLB at path.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse reads a GLB. It keeps no reference to data.
func Parse(data []byte) (*File, error) {
	if len(data) < 20 || string(data[0:4]) != "glTF" {
		return nil, errors.New("not a GLB: bad magic")
	}
	if v := binary.LittleEndian.Uint32(data[4:8]); v != 2 {
		return nil, fmt.Errorf("GLB version %d: want 2", v)
	}
	if n := int(binary.LittleEndian.Uint32(data[8:12])); n >= 20 && n < len(data) {
		data = data[:n]
	}
	f := &File{}
	for pos := 12; pos < len(data); {
		if pos+8 > len(data) {
			return nil, errors.New("not a GLB: a chunk is cut short")
		}
		size := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		kind := binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		pos += 8
		if size < 0 || pos+size > len(data) {
			return nil, errors.New("not a GLB: a chunk is cut short")
		}
		body := data[pos : pos+size]
		pos += size
		switch {
		case f.doc == nil:
			if kind != chunkJSON {
				return nil, errors.New("not a GLB: the first chunk is not JSON")
			}
			if err := json.Unmarshal(body, &f.doc); err != nil {
				return nil, fmt.Errorf("parsing the GLB JSON chunk: %w", err)
			}
		case kind == chunkBIN && f.bin == nil && len(f.more) == 0:
			f.bin = append([]byte(nil), body...)
		default:
			f.more = append(f.more, chunk{kind, append([]byte(nil), body...)})
		}
	}
	if f.doc == nil {
		return nil, errors.New("not a GLB: it has no JSON chunk")
	}
	return f, nil
}

// Bytes writes the GLB: the JSON chunk padded with spaces and the binary chunk with zeros,
// both to a multiple of four bytes.
func (f *File) Bytes() ([]byte, error) {
	var js bytes.Buffer
	enc := json.NewEncoder(&js)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(f.doc); err != nil {
		return nil, err
	}
	doc := bytes.TrimRight(js.Bytes(), "\n")
	chunks := []chunk{{chunkJSON, pad(doc, ' ')}}
	if f.bin != nil {
		chunks = append(chunks, chunk{chunkBIN, pad(f.bin, 0)})
	}
	for _, c := range f.more {
		chunks = append(chunks, chunk{c.kind, pad(c.data, 0)})
	}
	total := 12
	for _, c := range chunks {
		total += 8 + len(c.data)
	}
	out := make([]byte, 0, total)
	out = append(out, "glTF"...)
	out = binary.LittleEndian.AppendUint32(out, 2)
	out = binary.LittleEndian.AppendUint32(out, uint32(total))
	for _, c := range chunks {
		out = binary.LittleEndian.AppendUint32(out, uint32(len(c.data)))
		out = binary.LittleEndian.AppendUint32(out, c.kind)
		out = append(out, c.data...)
	}
	return out, nil
}

func pad(b []byte, with byte) []byte {
	n := (4 - len(b)%4) % 4
	if n == 0 {
		return b
	}
	out := make([]byte, len(b)+n)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = with
	}
	return out
}

// Stats counts what the file holds, as Read does.
func (f *File) Stats() (Stats, error) {
	data, err := f.Bytes()
	if err != nil {
		return Stats{}, err
	}
	return Read(bytes.NewReader(data))
}

// object is one entry of a glTF array, with every member it has: members this package does
// not know (extensions, extras) pass through untouched.
type object = map[string]json.RawMessage

func (f *File) list(name string) ([]object, error) {
	raw, ok := f.doc[name]
	if !ok {
		return nil, nil
	}
	var out []object
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("the GLB's %s are damaged: %w", name, err)
	}
	return out, nil
}

func (f *File) setList(name string, list []object) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	f.doc[name] = raw
	return nil
}

func get[T any](o object, key string) (v T, ok bool) {
	raw, has := o[key]
	if !has || json.Unmarshal(raw, &v) != nil {
		return v, false
	}
	return v, true
}

func put(o object, key string, v any) {
	raw, _ := json.Marshal(v)
	o[key] = raw
}

// Texture is the image a material's texture reads.
type Texture struct {
	Texture int
	Image   int
}

// Material is what Forja needs to know of a glTF material.
type Material struct {
	Index int
	// Used says a mesh primitive draws with it.
	Used              bool
	BaseColor         *Texture
	MetallicRoughness *Texture
	// Roughness and Metallic are the material's factors, with glTF's default of 1.
	Roughness, Metallic float64
}

type textureInfo struct {
	Index int `json:"index"`
}

// Materials lists the file's materials.
func (f *File) Materials() ([]Material, error) {
	mats, err := f.list("materials")
	if err != nil {
		return nil, err
	}
	textures, err := f.list("textures")
	if err != nil {
		return nil, err
	}
	meshes, err := f.list("meshes")
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, m := range meshes {
		var prims []struct {
			Material *int `json:"material"`
		}
		if raw, ok := m["primitives"]; ok && json.Unmarshal(raw, &prims) == nil {
			for _, p := range prims {
				if p.Material != nil {
					used[*p.Material] = true
				}
			}
		}
	}
	// A texture's image is its source, else the source an extension gives it (KTX2, WebP).
	image := func(texture int) *Texture {
		if texture < 0 || texture >= len(textures) {
			return nil
		}
		if src, ok := get[int](textures[texture], "source"); ok {
			return &Texture{texture, src}
		}
		exts, _ := get[map[string]struct {
			Source *int `json:"source"`
		}](textures[texture], "extensions")
		for _, name := range sortedKeys(exts) {
			if exts[name].Source != nil {
				return &Texture{texture, *exts[name].Source}
			}
		}
		return nil
	}
	out := make([]Material, len(mats))
	for i, m := range mats {
		out[i] = Material{Index: i, Used: used[i], Roughness: 1, Metallic: 1}
		pbr, ok := get[struct {
			BaseColor *textureInfo `json:"baseColorTexture"`
			MR        *textureInfo `json:"metallicRoughnessTexture"`
			Roughness *float64     `json:"roughnessFactor"`
			Metallic  *float64     `json:"metallicFactor"`
		}](m, "pbrMetallicRoughness")
		if !ok {
			continue
		}
		if pbr.BaseColor != nil {
			out[i].BaseColor = image(pbr.BaseColor.Index)
		}
		if pbr.MR != nil {
			out[i].MetallicRoughness = image(pbr.MR.Index)
		}
		if pbr.Roughness != nil {
			out[i].Roughness = *pbr.Roughness
		}
		if pbr.Metallic != nil {
			out[i].Metallic = *pbr.Metallic
		}
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// Image returns the encoded bytes of image i and its format, "png" or "jpeg". Any other
// format is an *ImageFormatError.
func (f *File) Image(i int) ([]byte, string, error) {
	images, err := f.list("images")
	if err != nil {
		return nil, "", err
	}
	if i < 0 || i >= len(images) {
		return nil, "", fmt.Errorf("the GLB has no image %d", i)
	}
	var data []byte
	if uri, ok := get[string](images[i], "uri"); ok {
		rest, isData := strings.CutPrefix(uri, "data:")
		_, payload, found := strings.Cut(rest, ",")
		if !isData || !found {
			return nil, "", errors.New("a texture is a separate file next to the GLB, and Forja edits textures stored inside it only")
		}
		if data, err = base64.StdEncoding.DecodeString(payload); err != nil {
			return nil, "", fmt.Errorf("a texture is damaged: %w", err)
		}
	} else if bv, ok := get[int](images[i], "bufferView"); ok {
		views, err := f.list("bufferViews")
		if err != nil {
			return nil, "", err
		}
		start, length, err := f.viewRange(views, bv)
		if err != nil {
			return nil, "", err
		}
		data = f.bin[start : start+length]
	} else {
		return nil, "", errors.New("a texture has no data")
	}
	kind := sniff(data)
	if kind != "png" && kind != "jpeg" {
		return nil, "", &ImageFormatError{Format: kind}
	}
	return data, kind, nil
}

// sniff names an image format from its first bytes.
func sniff(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return "png"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return "jpeg"
	case bytes.HasPrefix(b, []byte("\xABKTX 20\xBB\r\n\x1a\n")):
		return "KTX2"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "WebP"
	}
	return "an unknown format"
}

const meshopt = "EXT_meshopt_compression"

// place says where a bufferView's bytes are in the binary chunk. A meshopt-compressed view
// keeps them where its extension says; the view's own range then lies in a virtual buffer.
func place(v object) (buffer, offset, length int, ok bool) {
	holder := v
	if exts, has := get[map[string]json.RawMessage](v, "extensions"); has {
		if raw, has := exts[meshopt]; has {
			var inner object
			if json.Unmarshal(raw, &inner) != nil {
				return 0, 0, 0, false
			}
			holder = inner
		}
	}
	buffer, _ = get[int](holder, "buffer")
	offset, _ = get[int](holder, "byteOffset")
	length, ok = get[int](holder, "byteLength")
	return buffer, offset, length, ok
}

// moveView sets where a view's bytes start in the binary chunk, as place reads it.
func moveView(v object, offset int) {
	if exts, has := get[map[string]json.RawMessage](v, "extensions"); has {
		if raw, has := exts[meshopt]; has {
			var inner object
			if json.Unmarshal(raw, &inner) == nil {
				put(inner, "byteOffset", offset)
				out, _ := json.Marshal(inner)
				exts[meshopt] = out
				put(v, "extensions", exts)
				return
			}
		}
	}
	put(v, "byteOffset", offset)
}

func (f *File) viewRange(views []object, i int) (start, length int, err error) {
	if i < 0 || i >= len(views) {
		return 0, 0, fmt.Errorf("the GLB has no bufferView %d", i)
	}
	buffer, offset, length, ok := place(views[i])
	if !ok || buffer != 0 || offset < 0 || length < 0 || offset+length > len(f.bin) {
		return 0, 0, errors.New("a texture lies outside the GLB's binary data")
	}
	return offset, length, nil
}

// SetMetallicRoughnessMap makes png the metallic-roughness map of the given materials, which
// share a base color image. Materials that had a map keep that map's image (its bytes are
// replaced) and the rest gain it; every one gets factors of 1, since the map now carries the
// values. A material without a map gains an image, a texture and the reference, read through
// the same texture coordinates as its base color. Everything else in the file stays as it
// was, offsets shifted where an image grew or shrank.
func (f *File) SetMetallicRoughnessMap(group []int, png []byte) error {
	mats, err := f.list("materials")
	if err != nil {
		return err
	}
	textures, err := f.list("textures")
	if err != nil {
		return err
	}
	images, err := f.list("images")
	if err != nil {
		return err
	}
	infos, err := f.Materials()
	if err != nil {
		return err
	}
	for _, m := range group {
		if m < 0 || m >= len(mats) {
			return fmt.Errorf("the GLB has no material %d", m)
		}
	}
	pbrs := make([]object, len(group))
	for i, m := range group {
		pbrs[i] = object{}
		if raw, ok := mats[m]["pbrMetallicRoughness"]; ok {
			if err := json.Unmarshal(raw, &pbrs[i]); err != nil {
				return fmt.Errorf("material %d is damaged: %w", m, err)
			}
		}
	}

	// The map to reuse: the first material's own.
	texture := -1
	for _, m := range group {
		if t := infos[m].MetallicRoughness; t != nil {
			texture = t.Texture
			break
		}
	}
	if texture >= 0 {
		// A texture with a KTX2 or WebP copy beside the PNG would keep showing the old one.
		if exts, ok := get[map[string]struct {
			Source *int `json:"source"`
		}](textures[texture], "extensions"); ok {
			for _, e := range exts {
				if e.Source != nil {
					return &ImageFormatError{Format: "KTX2 or WebP"}
				}
			}
		}
		source, ok := get[int](textures[texture], "source")
		if !ok || source < 0 || source >= len(images) {
			return errors.New("the metallic-roughness map has no image")
		}
		if err := f.replaceImage(images, source, png); err != nil {
			return err
		}
	} else {
		image := len(images)
		images = append(images, object{})
		if err := f.replaceImage(images, image, png); err != nil {
			return err
		}
		tex := object{}
		put(tex, "source", image)
		// Same wrapping and filtering as the base color, so the maps agree at the edges.
		if base := infos[group[0]].BaseColor; base != nil {
			if sampler, ok := get[int](textures[base.Texture], "sampler"); ok {
				put(tex, "sampler", sampler)
			}
		}
		texture = len(textures)
		textures = append(textures, tex)
		put(tex, "name", "forja_metallic_roughness")
	}
	for i, m := range group {
		info := object{}
		if base, ok := pbrs[i]["baseColorTexture"]; ok {
			_ = json.Unmarshal(base, &info)
		}
		put(info, "index", texture)
		put(pbrs[i], "metallicRoughnessTexture", info)
		put(pbrs[i], "roughnessFactor", 1)
		put(pbrs[i], "metallicFactor", 1)
		raw, _ := json.Marshal(pbrs[i])
		mats[m]["pbrMetallicRoughness"] = raw
	}
	if err := f.setList("materials", mats); err != nil {
		return err
	}
	if err := f.setList("textures", textures); err != nil {
		return err
	}
	return f.setList("images", images)
}

// replaceImage stores png as the data of images[i] (a new entry when it is empty), in its
// own bufferView of the binary chunk.
func (f *File) replaceImage(images []object, i int, png []byte) error {
	views, err := f.list("bufferViews")
	if err != nil {
		return err
	}
	var accessors []struct {
		BufferView *int `json:"bufferView"`
		Sparse     *struct {
			Indices struct {
				BufferView int `json:"bufferView"`
			} `json:"indices"`
			Values struct {
				BufferView int `json:"bufferView"`
			} `json:"values"`
		} `json:"sparse"`
	}
	if raw, ok := f.doc["accessors"]; ok {
		if err := json.Unmarshal(raw, &accessors); err != nil {
			return fmt.Errorf("the GLB's accessors are damaged: %w", err)
		}
	}
	buffers, err := f.list("buffers")
	if err != nil {
		return err
	}
	if len(buffers) > 0 {
		if _, external := get[string](buffers[0], "uri"); external {
			return errors.New("the GLB keeps its data in a separate file, and Forja edits GLBs that hold it all")
		}
	}

	old, hasOld := get[int](images[i], "bufferView")
	if hasOld && (old < 0 || old >= len(views)) {
		return fmt.Errorf("the GLB has no bufferView %d", old)
	}
	inPlace := hasOld
	if inPlace {
		// Only a view nothing else reads, and no other view overlaps, can change size.
		for j, other := range images {
			if bv, ok := get[int](other, "bufferView"); ok && bv == old && j != i {
				inPlace = false
			}
		}
		for _, a := range accessors {
			if (a.BufferView != nil && *a.BufferView == old) ||
				(a.Sparse != nil && (a.Sparse.Indices.BufferView == old || a.Sparse.Values.BufferView == old)) {
				inPlace = false
			}
		}
	}
	if inPlace {
		start, length, err := f.viewRange(views, old)
		if err != nil {
			return err
		}
		for j, v := range views {
			if b, off, n, ok := place(v); j != old && ok && b == 0 && off < start+length && off+n > start {
				inPlace = false
			}
		}
		if inPlace {
			delta := f.spliceImage(images, views, i, old, start, length, png)
			size, _ := get[int](buffers[0], "byteLength")
			return f.finishViews(views, buffers, size+delta)
		}
	}
	// Appended after the data already there; a view this replaces and drops is left unread.
	end := len(pad(f.bin, 0))
	f.bin = append(pad(f.bin, 0), png...)
	f.bin = pad(f.bin, 0)
	view := object{}
	put(view, "buffer", 0)
	put(view, "byteOffset", end)
	put(view, "byteLength", len(png))
	views = append(views, view)
	setImage(images[i], len(views)-1)
	return f.finishViews(views, buffers, len(f.bin))
}

// setImage points an image entry at a bufferView holding a PNG.
func setImage(img object, view int) {
	delete(img, "uri")
	put(img, "bufferView", view)
	put(img, "mimeType", "image/png")
}

// spliceImage writes png where the image's old bytes were. The room it takes is a multiple
// of four bytes more or less than before, so every view after it moves by that much and
// stays aligned.
func (f *File) spliceImage(images, views []object, i, view, start, length int, png []byte) (delta int) {
	delta = ceilTo4(len(png) - length)
	next := make([]byte, 0, len(f.bin)+delta)
	next = append(next, f.bin[:start]...)
	next = append(next, png...)
	next = append(next, make([]byte, length+delta-len(png))...)
	next = append(next, f.bin[start+length:]...)
	cut := start + length
	for j, v := range views {
		if b, off, _, ok := place(v); ok && b == 0 && j != view && off >= cut {
			moveView(v, off+delta)
		}
	}
	put(views[view], "byteLength", len(png))
	setImage(images[i], view)
	f.bin = next
	return delta
}

// ceilTo4 rounds n up to a multiple of four, toward positive infinity.
func ceilTo4(n int) int {
	if n >= 0 {
		return (n + 3) / 4 * 4
	}
	return -((-n) / 4 * 4)
}

// finishViews writes the views and the buffer's length back after a change.
func (f *File) finishViews(views, buffers []object, size int) error {
	if err := f.setList("bufferViews", views); err != nil {
		return err
	}
	if len(buffers) == 0 {
		buffers = []object{{}}
	}
	put(buffers[0], "byteLength", size)
	return f.setList("buffers", buffers)
}
