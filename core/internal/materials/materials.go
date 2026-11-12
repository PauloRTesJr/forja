// Package materials lets a person say how rough and how metallic each material of an asset
// is. Local painters make base color only, so the GLB ships one roughness and one metallic
// for its whole surface. Here the materials are found as the few dominant colors of the base
// color texture, the person sets two numbers for each, and the numbers are written into the
// asset's metallic-roughness map. Nothing is guessed on the person's behalf.
package materials

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"math"
	"sort"

	// The decoders of the textures a GLB may hold.
	_ "image/jpeg"

	"forja/internal/glb"
)

// ErrNoTexture is an asset whose materials have no base color texture to tell apart.
var ErrNoTexture = errors.New("this asset has no base color texture, so it has no materials to tell apart")

// ErrPainted is an asset whose model painted its roughness and metallic: setting them would
// replace the model's work.
var ErrPainted = errors.New("this asset's roughness and metallic were painted by its model; setting them replaces that work")

// InputError is a request that names a material or a value that does not exist.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Choice is the roughness and metallic a person set for one material.
type Choice struct {
	Index     int     `json:"index"`
	Roughness float64 `json:"roughness"`
	Metallic  float64 `json:"metallic"`
}

// Set changes a material's values; a field left nil keeps the value it has.
type Set struct {
	Index     int
	Roughness *float64
	Metallic  *float64
}

// Material is one material found in an asset, with the values it has now.
type Material struct {
	Index int `json:"index"`
	// Texture is the base color texture it was found in: an asset may have several, and each
	// is split on its own.
	Texture int `json:"texture"`
	// Color is the material's color as #rrggbb.
	Color string `json:"color"`
	// Share is the part of its texture, from 0 to 1, that has this color.
	Share     float64 `json:"share"`
	Roughness float64 `json:"roughness"`
	Metallic  float64 `json:"metallic"`
}

// surface is one base color image and the glTF materials that draw with it.
type surface struct {
	image    int
	group    []int
	clusters []Cluster
	// first is the index of the surface's first material in the Analysis.
	first   int
	painted bool
	// existing is the metallic-roughness map the group already has, decoded, or nil.
	existing image.Image
}

// Analysis is the materials of one GLB and what Edit needs to change them.
type Analysis struct {
	Materials []Material
	// Painted says some texture's roughness and metallic vary on their own: its model painted
	// them. Edit refuses to replace them unless asked to.
	Painted bool

	file     *glb.File
	surfaces []surface
	stored   map[int]bool
}

// Analyze finds the materials of f. stored are the values set earlier through Edit: a map
// that varies is a model's painting only when no value was ever set for its materials.
func Analyze(f *glb.File, stored []Choice) (*Analysis, error) {
	mats, err := f.Materials()
	if err != nil {
		return nil, err
	}
	a := &Analysis{file: f, stored: map[int]bool{}}
	byImage := map[int]int{}
	for _, m := range mats {
		if !m.Used || m.BaseColor == nil {
			continue
		}
		s, ok := byImage[m.BaseColor.Image]
		if !ok {
			s = len(a.surfaces)
			byImage[m.BaseColor.Image] = s
			a.surfaces = append(a.surfaces, surface{image: m.BaseColor.Image})
		}
		a.surfaces[s].group = append(a.surfaces[s].group, m.Index)
	}
	if len(a.surfaces) == 0 {
		return nil, ErrNoTexture
	}
	values := map[int]Choice{}
	for _, c := range stored {
		values[c.Index] = c
		a.stored[c.Index] = true
	}
	for s := range a.surfaces {
		sf := &a.surfaces[s]
		data, _, err := f.Image(sf.image)
		if err != nil {
			return nil, err
		}
		base, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("a texture cannot be read: %w", err)
		}
		sf.clusters = Find(base)
		sf.first = len(a.Materials)

		// What the surface looks like now: the factors of its first material, scaled by the
		// map when it has one.
		owner := mats[sf.group[0]]
		rough, metal := owner.Roughness, owner.Metallic
		variesNow := false
		for _, g := range sf.group {
			mr := mats[g].MetallicRoughness
			if mr == nil {
				continue
			}
			data, _, err := f.Image(mr.Image)
			if err != nil {
				return nil, err
			}
			sf.existing, _, err = image.Decode(bytes.NewReader(data))
			if err != nil {
				return nil, fmt.Errorf("a texture cannot be read: %w", err)
			}
			spread, mapRough, mapMetal := variation(sf.existing)
			variesNow = spread > 2
			rough, metal = mats[g].Roughness*mapRough, mats[g].Metallic*mapMetal
			break
		}
		hasStored := false
		for i := range sf.clusters {
			if a.stored[sf.first+i] {
				hasStored = true
			}
		}
		sf.painted = variesNow && !hasStored
		a.Painted = a.Painted || sf.painted
		for i, c := range sf.clusters {
			m := Material{Index: sf.first + i, Texture: s, Color: c.Hex(), Share: c.Share, Roughness: round(rough), Metallic: round(metal)}
			if v, ok := values[m.Index]; ok {
				m.Roughness, m.Metallic = v.Roughness, v.Metallic
			}
			a.Materials = append(a.Materials, m)
		}
	}
	return a, nil
}

// round keeps values to the thousandth, the most 8 bits can tell apart.
func round(v float64) float64 { return math.Round(math.Min(1, math.Max(0, v))*1000) / 1000 }

// Edit applies the values in sets and returns the new GLB, with the values every material
// of the textures it touched now has (and those set earlier on the others). A texture whose
// roughness and metallic were painted by its model is left alone unless overwrite is set.
func (a *Analysis) Edit(sets []Set, overwrite bool) ([]byte, []Choice, error) {
	if len(sets) == 0 {
		return nil, nil, &InputError{"name the materials to change"}
	}
	values := make([]Value, len(a.Materials))
	for i, m := range a.Materials {
		values[i] = Value{m.Roughness, m.Metallic}
	}
	touched := map[int]bool{}
	seen := map[int]bool{}
	for _, s := range sets {
		if s.Index < 0 || s.Index >= len(a.Materials) {
			return nil, nil, &InputError{fmt.Sprintf("this asset has no material %d: it has %d, numbered from 0", s.Index, len(a.Materials))}
		}
		if seen[s.Index] {
			return nil, nil, &InputError{fmt.Sprintf("material %d is named twice", s.Index)}
		}
		seen[s.Index] = true
		if s.Roughness == nil && s.Metallic == nil {
			return nil, nil, &InputError{fmt.Sprintf("material %d: give a roughness, a metallic or both", s.Index)}
		}
		for name, v := range map[string]*float64{"roughness": s.Roughness, "metallic": s.Metallic} {
			if v != nil && (math.IsNaN(*v) || *v < 0 || *v > 1) {
				return nil, nil, &InputError{fmt.Sprintf("material %d: %s %v is outside 0 to 1", s.Index, name, *v)}
			}
		}
		if s.Roughness != nil {
			values[s.Index].Roughness = *s.Roughness
		}
		if s.Metallic != nil {
			values[s.Index].Metallic = *s.Metallic
		}
		touched[a.Materials[s.Index].Texture] = true
	}
	for s := range touched {
		if a.surfaces[s].painted && !overwrite {
			return nil, nil, ErrPainted
		}
	}
	var order []int
	for s := range touched {
		order = append(order, s)
	}
	sort.Ints(order)
	for _, s := range order {
		sf := a.surfaces[s]
		data, _, err := a.file.Image(sf.image)
		if err != nil {
			return nil, nil, err
		}
		base, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, nil, fmt.Errorf("a texture cannot be read: %w", err)
		}
		painted := Paint(base, sf.existing, sf.clusters, values[sf.first:sf.first+len(sf.clusters)])
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, painted); err != nil {
			return nil, nil, err
		}
		if err := a.file.SetMetallicRoughnessMap(sf.group, buf.Bytes()); err != nil {
			return nil, nil, err
		}
	}
	out, err := a.file.Bytes()
	if err != nil {
		return nil, nil, err
	}
	// The analysis now describes the new file.
	var choices []Choice
	a.Painted = false
	for i := range a.Materials {
		if touched[a.Materials[i].Texture] || a.stored[i] {
			choices = append(choices, Choice{i, values[i].Roughness, values[i].Metallic})
			a.stored[i] = true
		}
		a.Materials[i].Roughness, a.Materials[i].Metallic = round(values[i].Roughness), round(values[i].Metallic)
	}
	for s := range a.surfaces {
		if touched[s] {
			a.surfaces[s].painted = false
		}
		a.Painted = a.Painted || a.surfaces[s].painted
	}
	return out, choices, nil
}
