package glb

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// build makes a GLB from a glTF document, with a binary chunk the reader must not need.
func build(t testing.TB, doc any) []byte {
	t.Helper()
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	bin := make([]byte, 64)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint32{0x46546C67, 2, uint32(12 + 8 + len(js) + 8 + len(bin))})
	_ = binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(js)), 0x4E4F534A})
	b.Write(js)
	_ = binary.Write(&b, binary.LittleEndian, [2]uint32{uint32(len(bin)), 0x004E4942})
	b.Write(bin)
	return b.Bytes()
}

// lods is how the engine's mesh.lods stage writes a chest: one node per level of detail.
func lods() map[string]any {
	prim := func(pos, idx, mat int) map[string]any {
		return map[string]any{"attributes": map[string]int{"POSITION": pos, "NORMAL": pos}, "indices": idx, "material": mat}
	}
	return map[string]any{
		"asset": map[string]string{"version": "2.0"},
		"scene": 0,
		"scenes": []any{
			map[string]any{"nodes": []int{0}},
		},
		"nodes": []any{
			map[string]any{"name": "chest", "children": []int{1, 2, 3}},
			map[string]any{"name": "chest_LOD0", "mesh": 0},
			map[string]any{"name": "chest_LOD1", "mesh": 1},
			map[string]any{"name": "chest_LOD2", "mesh": 2},
		},
		"meshes": []any{
			map[string]any{"primitives": []any{prim(0, 1, 0), prim(2, 3, 1)}},
			map[string]any{"primitives": []any{prim(4, 5, 0)}},
			map[string]any{"primitives": []any{prim(6, 7, 0)}},
		},
		"accessors": []any{
			map[string]int{"count": 1200}, map[string]int{"count": 6000},
			map[string]int{"count": 300}, map[string]int{"count": 900},
			map[string]int{"count": 500}, map[string]int{"count": 1500},
			map[string]int{"count": 100}, map[string]int{"count": 300},
		},
	}
}

func TestStatsCountTheFullDetailLevelAndTheDeepestLOD(t *testing.T) {
	got, err := Read(bytes.NewReader(build(t, lods())))
	if err != nil {
		t.Fatal(err)
	}
	want := Stats{Triangles: 2000 + 300, Vertices: 1200 + 300, Materials: 2, LODs: 2}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// rigged is how the engine writes a rigged character: the joints at the scene root beside the
// LOD meshes, every LOD skinned by the one skin.
func rigged(names []string, extras any) map[string]any {
	doc := lods()
	nodes := []any{
		map[string]any{"name": "hero_LOD0", "mesh": 0, "skin": 0},
		map[string]any{"name": "hero_LOD1", "mesh": 1, "skin": 0},
	}
	joints := []int{}
	for i, n := range names {
		node := map[string]any{"name": n, "translation": []float64{0, float64(i) * 0.1, 0}}
		if i+1 < len(names) {
			node["children"] = []int{len(nodes) + 1}
		}
		joints = append(joints, len(nodes))
		nodes = append(nodes, node)
	}
	doc["nodes"] = nodes
	doc["scenes"] = []any{map[string]any{"nodes": []int{0, 1, 2}}}
	skin := map[string]any{"joints": joints, "inverseBindMatrices": 0, "skeleton": 2}
	if extras != nil {
		skin["extras"] = extras
	}
	doc["skins"] = []any{skin}
	return doc
}

func TestStatsOfARiggedAsset(t *testing.T) {
	humanoid := append([]string(nil), HumanoidBones...)
	humanoid = append(humanoid, "Chest", "Neck", "Bone_17")
	for name, tc := range map[string]struct {
		names  []string
		extras any
		want   string
	}{
		"says it is humanoid":     {humanoid, map[string]any{"forja_skeleton": "humanoid"}, SkeletonHumanoid},
		"humanoid by bone names":  {humanoid, nil, SkeletonHumanoid},
		"says it is generic":      {humanoid, map[string]any{"forja_skeleton": "generic"}, SkeletonGeneric},
		"a tail and no arms":      {[]string{"Root", "Bone_1", "Bone_2"}, nil, SkeletonGeneric},
		"extras that are no help": {[]string{"Root", "Bone_1"}, "a string", SkeletonGeneric},
	} {
		got, err := Read(bytes.NewReader(build(t, rigged(tc.names, tc.extras))))
		if err != nil {
			t.Fatal(name, err)
		}
		if got.Bones != len(tc.names) || got.Skeleton != tc.want || got.LODs != 1 || got.Triangles != 2300 {
			t.Errorf("%s: got %+v, want %d bones, a %s skeleton", name, got, len(tc.names), tc.want)
		}
		m := got.Map()
		if m["bones"] != len(tc.names) || m["skeleton"] != tc.want {
			t.Errorf("%s: stats %v", name, m)
		}
	}
}

func TestUnriggedStatsHaveNoBones(t *testing.T) {
	got, err := Read(bytes.NewReader(build(t, lods())))
	if err != nil {
		t.Fatal(err)
	}
	m := got.Map()
	if _, ok := m["bones"]; ok || len(m) != 4 {
		t.Fatalf("stats %v", m)
	}
	// A skin only the lower levels use, or one that names no joints, is no rig of the asset.
	doc := rigged([]string{"Root", "Bone_1"}, nil)
	doc["nodes"].([]any)[0].(map[string]any)["skin"] = nil
	if got, _ := Read(bytes.NewReader(build(t, doc))); got.Bones != 0 || got.Skeleton != "" {
		t.Fatalf("got %+v", got)
	}
	doc = rigged(nil, nil)
	if got, _ := Read(bytes.NewReader(build(t, doc))); got.Bones != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestStatsWithoutIndicesOrScenes(t *testing.T) {
	doc := map[string]any{
		"asset": map[string]string{"version": "2.0"},
		"nodes": []any{
			map[string]any{"name": "a", "mesh": 0, "children": []int{1}},
			map[string]any{"name": "b", "mesh": 0},
			// Not reachable from a root: a child of nothing is a root, so it counts.
			map[string]any{"name": "c", "mesh": 0},
		},
		"meshes":    []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0}}}}},
		"accessors": []any{map[string]int{"count": 9}},
	}
	got, err := Read(bytes.NewReader(build(t, doc)))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Stats{Triangles: 9, Vertices: 27}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestMalformedFilesAreRefusedOrSurvived(t *testing.T) {
	for name, raw := range map[string][]byte{
		"empty":     nil,
		"obj file":  []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"),
		"truncated": build(t, lods())[:30],
	} {
		if _, err := Read(bytes.NewReader(raw)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Out-of-range indices and a cycle are counted as far as they make sense.
	cyclic := map[string]any{
		"scenes":    []any{map[string]any{"nodes": []int{0, 7}}},
		"nodes":     []any{map[string]any{"name": "x", "mesh": 3, "children": []int{0, 1}}, map[string]any{"mesh": 0}},
		"meshes":    []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 5}, "indices": 9}}}},
		"accessors": []any{},
	}
	if got, err := Read(bytes.NewReader(build(t, cyclic))); err != nil || got != (Stats{}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chest.glb")
	if err := os.WriteFile(path, build(t, lods()), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || got.Triangles != 2300 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := ReadFile(path + ".missing"); err == nil || !strings.Contains(err.Error(), "chest.glb.missing") {
		t.Fatalf("err %v", err)
	}
}

func BenchmarkReadFile(b *testing.B) {
	path := filepath.Join(b.TempDir(), "chest.glb")
	if err := os.WriteFile(path, build(b, lods()), 0o644); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ReadFile(path); err != nil {
			b.Fatal(err)
		}
	}
}
