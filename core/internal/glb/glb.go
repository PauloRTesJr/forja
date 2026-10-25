// Package glb reads what an asset is made of from a binary glTF file's JSON chunk, without
// decoding its buffers.
package glb

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
)

// Stats are counted over the full-detail level only, as the desktop viewer counts them.
type Stats struct {
	Triangles int `json:"triangles"`
	Vertices  int `json:"vertices"`
	Materials int `json:"materials"`
	// LODs is the highest level of detail below the full one, from node names ending _LOD<n>.
	LODs int `json:"lods"`
	// Bones counts the joints of the skin on the full-detail level; 0 for a mesh with no rig.
	Bones int `json:"bones,omitempty"`
	// Skeleton is SkeletonHumanoid or SkeletonGeneric for a rigged asset.
	Skeleton string `json:"skeleton,omitempty"`
}

const (
	SkeletonHumanoid = "humanoid"
	SkeletonGeneric  = "generic"
)

// HumanoidBones are the bones a skeleton needs to be a humanoid, named as Unity's
// HumanBodyBones: the names the engine gives a humanoid rig, which game engines retarget to.
var HumanoidBones = []string{
	"Hips", "Spine", "Head",
	"LeftUpperArm", "LeftLowerArm", "LeftHand", "RightUpperArm", "RightLowerArm", "RightHand",
	"LeftUpperLeg", "LeftLowerLeg", "LeftFoot", "RightUpperLeg", "RightLowerLeg", "RightFoot",
}

// Map is the stats as an asset stores them. Only a rigged asset has bones and a skeleton.
func (s Stats) Map() map[string]any {
	m := map[string]any{"triangles": s.Triangles, "vertices": s.Vertices, "materials": s.Materials, "lods": s.LODs}
	if s.Bones > 0 {
		m["bones"], m["skeleton"] = s.Bones, s.Skeleton
	}
	return m
}

type document struct {
	Scene  *int `json:"scene"`
	Scenes []struct {
		Nodes []int `json:"nodes"`
	} `json:"scenes"`
	Nodes []struct {
		Name     string `json:"name"`
		Mesh     *int   `json:"mesh"`
		Skin     *int   `json:"skin"`
		Children []int  `json:"children"`
	} `json:"nodes"`
	Skins []struct {
		Joints []int `json:"joints"`
		// glTF allows any JSON as extras; only an object with forja_skeleton says anything.
		Extras json.RawMessage `json:"extras"`
	} `json:"skins"`
	Meshes []struct {
		Primitives []struct {
			Attributes map[string]int `json:"attributes"`
			Indices    *int           `json:"indices"`
			Material   *int           `json:"material"`
		} `json:"primitives"`
	} `json:"meshes"`
	Accessors []struct {
		Count int `json:"count"`
	} `json:"accessors"`
}

var lodName = regexp.MustCompile(`_LOD(\d+)$`)

func lodLevel(name string) int {
	m := lodName.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// ReadFile reads the stats of the GLB at path.
func ReadFile(path string) (Stats, error) {
	f, err := os.Open(path)
	if err != nil {
		return Stats{}, err
	}
	defer f.Close()
	return Read(f)
}

// Read reads the stats of a GLB, consuming only its header and JSON chunk.
func Read(r io.Reader) (Stats, error) {
	var head [20]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return Stats{}, fmt.Errorf("not a GLB: %w", err)
	}
	if string(head[0:4]) != "glTF" {
		return Stats{}, errors.New("not a GLB: bad magic")
	}
	if v := binary.LittleEndian.Uint32(head[4:8]); v != 2 {
		return Stats{}, fmt.Errorf("GLB version %d: want 2", v)
	}
	size := binary.LittleEndian.Uint32(head[12:16])
	if string(head[16:20]) != "JSON" {
		return Stats{}, errors.New("not a GLB: the first chunk is not JSON")
	}
	if size > 64<<20 {
		return Stats{}, fmt.Errorf("GLB JSON chunk of %d bytes is too large", size)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return Stats{}, fmt.Errorf("reading the GLB JSON chunk: %w", err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Stats{}, fmt.Errorf("parsing the GLB JSON chunk: %w", err)
	}
	return doc.stats(), nil
}

func (d *document) count(accessor int) int {
	if accessor < 0 || accessor >= len(d.Accessors) {
		return 0
	}
	return d.Accessors[accessor].Count
}

func (d *document) stats() Stats {
	var roots []int
	switch {
	case d.Scene != nil && *d.Scene < len(d.Scenes):
		roots = d.Scenes[*d.Scene].Nodes
	case len(d.Scenes) > 0:
		roots = d.Scenes[0].Nodes
	default:
		// No scene: every node no other node lists as a child.
		child := map[int]bool{}
		for _, n := range d.Nodes {
			for _, c := range n.Children {
				child[c] = true
			}
		}
		for i := range d.Nodes {
			if !child[i] {
				roots = append(roots, i)
			}
		}
	}
	var st Stats
	materials := map[int]bool{}
	skin := -1
	seen := map[int]bool{} // a malformed file may list a node twice or in a cycle
	var walk func(i int)
	walk = func(i int) {
		if i < 0 || i >= len(d.Nodes) || seen[i] {
			return
		}
		seen[i] = true
		n := d.Nodes[i]
		level := lodLevel(n.Name)
		st.LODs = max(st.LODs, level)
		if n.Mesh != nil && n.Skin != nil && level == 0 && *n.Skin >= 0 && *n.Skin < len(d.Skins) &&
			(skin < 0 || len(d.Skins[*n.Skin].Joints) > len(d.Skins[skin].Joints)) {
			skin = *n.Skin
		}
		if n.Mesh != nil && *n.Mesh >= 0 && *n.Mesh < len(d.Meshes) && level == 0 {
			for _, p := range d.Meshes[*n.Mesh].Primitives {
				pos, ok := p.Attributes["POSITION"]
				if !ok {
					continue
				}
				verts := d.count(pos)
				st.Vertices += verts
				if p.Indices != nil {
					st.Triangles += d.count(*p.Indices) / 3
				} else {
					st.Triangles += verts / 3
				}
				if p.Material != nil {
					materials[*p.Material] = true
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	st.Materials = len(materials)
	if skin >= 0 && len(d.Skins[skin].Joints) > 0 {
		st.Bones = len(d.Skins[skin].Joints)
		st.Skeleton = d.skeleton(skin)
	}
	return st
}

// skeleton is what the rig says it is, else what its bone names show: gltfpack and other
// tools may drop extras, never node names.
func (d *document) skeleton(skin int) string {
	var extras struct {
		Skeleton string `json:"forja_skeleton"`
	}
	if json.Unmarshal(d.Skins[skin].Extras, &extras) == nil &&
		(extras.Skeleton == SkeletonHumanoid || extras.Skeleton == SkeletonGeneric) {
		return extras.Skeleton
	}
	names := map[string]bool{}
	for _, j := range d.Skins[skin].Joints {
		if j >= 0 && j < len(d.Nodes) {
			names[d.Nodes[j].Name] = true
		}
	}
	for _, b := range HumanoidBones {
		if !names[b] {
			return SkeletonGeneric
		}
	}
	return SkeletonHumanoid
}
