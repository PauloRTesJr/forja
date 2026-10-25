package export

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forja/internal/glb"
)

// riggedBones is a humanoid as the engine's rig stage names it: Y up, facing +Z, its left at
// +X, in metres. The last bone is one the mapper could not name.
var riggedBones = []struct {
	name, parent string
	head         [3]float32
}{
	{"Hips", "", [3]float32{0, 1, 0}}, {"Spine", "Hips", [3]float32{0, 1.1, 0}},
	{"Chest", "Spine", [3]float32{0, 1.3, 0}}, {"Neck", "Chest", [3]float32{0, 1.5, 0}},
	{"Head", "Neck", [3]float32{0, 1.6, 0}},
	{"LeftUpperArm", "Chest", [3]float32{0.18, 1.45, 0}}, {"LeftLowerArm", "LeftUpperArm", [3]float32{0.45, 1.45, 0}},
	{"LeftHand", "LeftLowerArm", [3]float32{0.7, 1.45, 0}},
	{"RightUpperArm", "Chest", [3]float32{-0.18, 1.45, 0}}, {"RightLowerArm", "RightUpperArm", [3]float32{-0.45, 1.45, 0}},
	{"RightHand", "RightLowerArm", [3]float32{-0.7, 1.45, 0}},
	{"LeftUpperLeg", "Hips", [3]float32{0.1, 0.95, 0}}, {"LeftLowerLeg", "LeftUpperLeg", [3]float32{0.1, 0.5, 0}},
	{"LeftFoot", "LeftLowerLeg", [3]float32{0.1, 0.08, 0}},
	{"RightUpperLeg", "Hips", [3]float32{-0.1, 0.95, 0}}, {"RightLowerLeg", "RightUpperLeg", [3]float32{-0.1, 0.5, 0}},
	{"RightFoot", "RightLowerLeg", [3]float32{-0.1, 0.08, 0}},
	{"Bone_17", "Head", [3]float32{0, 1.8, 0}},
}

// riggedGLB writes a character the way the engine does: the joints at the scene root beside
// two LOD meshes that share the skin. A small cube at each joint is weighted to it and its
// parent, so every bone deforms something.
func riggedGLB(t *testing.T, path string) {
	t.Helper()
	index := map[string]int{}
	for i, b := range riggedBones {
		index[b.name] = i
	}
	var bin bytes.Buffer
	var views, accessors []map[string]any
	add := func(data any, count int, ctype int, typ string, minmax ...[]float32) int {
		for bin.Len()%4 != 0 {
			bin.WriteByte(0)
		}
		start := bin.Len()
		_ = binary.Write(&bin, binary.LittleEndian, data)
		views = append(views, map[string]any{"buffer": 0, "byteOffset": start, "byteLength": bin.Len() - start})
		acc := map[string]any{"bufferView": len(views) - 1, "componentType": ctype, "count": count, "type": typ}
		if len(minmax) == 2 {
			acc["min"], acc["max"] = minmax[0], minmax[1]
		}
		accessors = append(accessors, acc)
		return len(accessors) - 1
	}
	cube := [8][3]float32{{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1}, {-1, -1, 1}, {1, -1, 1}, {1, 1, 1}, {-1, 1, 1}}
	faces := []uint32{0, 2, 1, 0, 3, 2, 4, 5, 6, 4, 6, 7, 0, 1, 5, 0, 5, 4, 2, 3, 7, 2, 7, 6, 1, 2, 6, 1, 6, 5, 0, 4, 7, 0, 7, 3}
	var meshes []map[string]any
	for lod, every := range []int{1, 2} {
		var pos [][3]float32
		var joints [][4]uint8
		var weights [][4]float32
		var idx []uint32
		lo, hi := []float32{9, 9, 9}, []float32{-9, -9, -9}
		for j, b := range riggedBones {
			if j%every != 0 {
				continue
			}
			base := uint32(len(pos))
			parent, w := j, [4]float32{1, 0, 0, 0}
			if b.parent != "" {
				parent, w = index[b.parent], [4]float32{0.7, 0.3, 0, 0}
			}
			for _, c := range cube {
				p := [3]float32{b.head[0] + 0.04*c[0], b.head[1] + 0.04*c[1], b.head[2] + 0.04*c[2]}
				for k := range 3 {
					lo[k], hi[k] = min(lo[k], p[k]), max(hi[k], p[k])
				}
				pos = append(pos, p)
				joints = append(joints, [4]uint8{uint8(j), uint8(parent), 0, 0})
				weights = append(weights, w)
			}
			for _, f := range faces {
				idx = append(idx, base+f)
			}
		}
		meshes = append(meshes, map[string]any{"name": "hero_LOD" + string(rune('0'+lod)), "primitives": []any{map[string]any{
			"attributes": map[string]int{
				"POSITION":  add(pos, len(pos), 5126, "VEC3", lo, hi),
				"JOINTS_0":  add(joints, len(joints), 5121, "VEC4"),
				"WEIGHTS_0": add(weights, len(weights), 5126, "VEC4"),
			},
			"indices": add(idx, len(idx), 5125, "SCALAR"), "material": 0,
		}}})
	}
	nodes := []map[string]any{{"name": "hero_LOD0", "mesh": 0, "skin": 0}, {"name": "hero_LOD1", "mesh": 1, "skin": 0}}
	first := len(nodes)
	var ibm [][16]float32
	var jointNodes []int
	for _, b := range riggedBones {
		local := b.head
		if b.parent != "" {
			ph := riggedBones[index[b.parent]].head
			local = [3]float32{b.head[0] - ph[0], b.head[1] - ph[1], b.head[2] - ph[2]}
		}
		nodes = append(nodes, map[string]any{"name": b.name, "translation": local})
		ibm = append(ibm, [16]float32{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, -b.head[0], -b.head[1], -b.head[2], 1})
		jointNodes = append(jointNodes, len(nodes)-1)
	}
	for j, b := range riggedBones {
		if b.parent != "" {
			p := nodes[first+index[b.parent]]
			children, _ := p["children"].([]int)
			p["children"] = append(children, first+j)
		}
	}
	doc := map[string]any{
		"asset": map[string]string{"version": "2.0"}, "scene": 0,
		"scenes": []any{map[string]any{"nodes": []int{0, 1, first}}},
		"nodes":  nodes, "meshes": meshes,
		"materials": []any{map[string]any{"name": "hero", "pbrMetallicRoughness": map[string]any{
			"baseColorFactor": []float32{0.8, 0.5, 0.3, 1}, "metallicFactor": 0, "roughnessFactor": 0.8}}},
		"skins": []any{map[string]any{"name": "hero_Skeleton", "joints": jointNodes, "skeleton": first,
			"inverseBindMatrices": add(ibm, len(ibm), 5126, "MAT4"), "extras": map[string]string{"forja_skeleton": "humanoid"}}},
	}
	for bin.Len()%4 != 0 {
		bin.WriteByte(0)
	}
	doc["accessors"], doc["bufferViews"] = accessors, views
	doc["buffers"] = []any{map[string]int{"byteLength": bin.Len()}}
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, [3]uint32{0x46546C67, 2, uint32(12 + 8 + len(js) + 8 + bin.Len())})
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(len(js)), 0x4E4F534A})
	out.Write(js)
	_ = binary.Write(&out, binary.LittleEndian, [2]uint32{uint32(bin.Len()), 0x004E4942})
	out.Write(bin.Bytes())
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// inspectRig prints, for a file Blender reads back, what a game engine would see of its rig.
const inspectRig = `
import json, sys, bpy
path = sys.argv[sys.argv.index("--") + 1]
bpy.ops.wm.read_homefile(use_empty=True)
if path.endswith(".blend"):
    bpy.ops.wm.open_mainfile(filepath=path)
elif path.endswith(".fbx"):
    bpy.ops.import_scene.fbx(filepath=path)
else:
    opts = bpy.ops.import_scene.gltf.get_rna_type().properties.keys()
    bpy.ops.import_scene.gltf(filepath=path, **({"disable_bone_shape": True} if "disable_bone_shape" in opts else {}))
bpy.context.view_layer.update()
out = []
for o in bpy.context.scene.objects:
    row = {"name": o.name, "type": o.type}
    if o.type == "ARMATURE":
        row["bones"] = sorted(b.name for b in o.data.bones)
        row["heads"] = {b.name: list(o.matrix_world @ b.head_local) for b in o.data.bones}
    if o.type == "MESH":
        row["armature"] = [m.object.name for m in o.modifiers if m.type == "ARMATURE" and m.object]
        groups = {g.index: g.name for g in o.vertex_groups}
        row["weighted"] = sorted({groups[g.group] for v in o.data.vertices for g in v.groups if g.weight > 0})
        zs = [(o.matrix_world @ v.co).z for v in o.data.vertices]
        row["top"] = max(zs) if zs else 0
    out.append(row)
print("FORJA_RIG " + json.dumps(out))
`

type inspected struct {
	Name     string                `json:"name"`
	Type     string                `json:"type"`
	Bones    []string              `json:"bones"`
	Heads    map[string][3]float64 `json:"heads"`
	Armature []string              `json:"armature"`
	Weighted []string              `json:"weighted"`
	Top      float64               `json:"top"`
}

func inspect(t *testing.T, blender, path string) []inspected {
	t.Helper()
	out, err := exec.Command(blender, "--background", "--factory-startup", "--python-expr", inspectRig, "--", path).CombinedOutput()
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if line, ok := strings.CutPrefix(sc.Text(), "FORJA_RIG "); ok {
			var got []inspected
			if err := json.Unmarshal([]byte(line), &got); err != nil {
				t.Fatal(err)
			}
			return got
		}
	}
	t.Fatalf("Blender read nothing back from %s: %v\n%s", path, err, lastLines(string(out), 8))
	return nil
}

// A rigged asset reaches FBX, glTF and .blend as a skinned mesh with its armature, LOD names
// kept, standing as tall as it did; the formats without levels of detail still export. It
// needs a real Blender: FORJA_TEST_BLENDER is its path.
func TestRealBlenderCarriesTheArmature(t *testing.T) {
	path := os.Getenv("FORJA_TEST_BLENDER")
	if path == "" {
		t.Skip("set FORJA_TEST_BLENDER to a Blender 4.2+ to convert a rigged asset with it")
	}
	b, err := FindBlender(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "hero-object")
	riggedGLB(t, src)
	if st, err := glb.ReadFile(src); err != nil || st.Bones != len(riggedBones) || st.Skeleton != glb.SkeletonHumanoid || st.LODs != 1 {
		t.Fatalf("the test asset reads as %+v, %v", st, err)
	}
	var want []string
	for _, bone := range riggedBones {
		want = append(want, bone.name)
	}
	slices.Sort(want)
	for _, id := range []string{"fbx", "gltf", "blend", "obj", "usdz", "stl"} {
		f, _ := LookupFormat(id)
		out, err := ToFiles(context.Background(), b, f, []Item{{Asset: "a1", Name: "hero", GLB: src}}, filepath.Join(dir, id))
		if err != nil || out[0].Error != "" || len(out[0].Files) == 0 {
			t.Fatalf("%s: %+v, %v", id, out, err)
		}
		if id == "obj" {
			raw, _ := os.ReadFile(out[0].Files[0])
			var objects []string
			for _, line := range strings.Split(string(raw), "\n") {
				if name, ok := strings.CutPrefix(strings.TrimSpace(line), "o "); ok {
					objects = append(objects, name)
				}
			}
			if !slices.Equal(objects, []string{"hero_LOD0"}) {
				t.Fatalf("the OBJ holds %v, want the full detail alone", objects)
			}
		}
		if id != "fbx" && id != "gltf" && id != "blend" {
			continue
		}
		main := slices.IndexFunc(out[0].Files, func(p string) bool { return strings.EqualFold(filepath.Ext(p), f.Ext) })
		if main < 0 {
			t.Fatalf("%s: no %s among %v", id, f.Ext, out[0].Files)
		}
		got := inspect(t, b.Path, out[0].Files[main])
		var arm *inspected
		meshes := map[string]inspected{}
		for i, o := range got {
			switch o.Type {
			case "ARMATURE":
				arm = &got[i]
			case "MESH":
				meshes[o.Name] = o
			}
		}
		if arm == nil || !slices.Equal(arm.Bones, want) {
			t.Fatalf("%s: armature %+v, want bones %v (objects %+v)", id, arm, want, got)
		}
		lod0, ok := meshes["hero_LOD0"]
		if _, lod1 := meshes["hero_LOD1"]; !ok || !lod1 || len(meshes) != 2 {
			t.Fatalf("%s: meshes %v, want hero_LOD0 and hero_LOD1 only", id, got)
		}
		if !slices.Equal(lod0.Armature, []string{arm.Name}) || !slices.Equal(lod0.Weighted, want) {
			t.Fatalf("%s: LOD0 is deformed by %v with weights for %v", id, lod0.Armature, lod0.Weighted)
		}
		// Back in Blender, Z is up: the head stays 1.6 m above the ground and the left hand at +X.
		head, hand := arm.Heads["Head"], arm.Heads["LeftHand"]
		if math.Abs(head[2]-1.6) > 0.01 || math.Abs(hand[0]-0.7) > 0.01 || math.Abs(lod0.Top-1.84) > 0.01 {
			t.Fatalf("%s: head at %v, left hand at %v, top at %.3f", id, head, hand, lod0.Top)
		}
		t.Logf("%s: %d bones on %s, LOD0 deformed by %v, head at %.3v, top %.3f m", id, len(arm.Bones), arm.Name,
			lod0.Armature, head, lod0.Top)
	}
}
