package api

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"
	"time"

	"forja/internal/library"
)

// glbOf wraps a glTF document's JSON in a GLB.
func glbOf(js string) []byte {
	for len(js)%4 != 0 {
		js += " "
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [5]uint32{0x46546C67, 2, uint32(20 + len(js)), uint32(len(js)), 0x4E4F534A})
	b.WriteString(js)
	return b.Bytes()
}

func TestAssetsMadeBeforeTheCoreReadStatsGetThemOnce(t *testing.T) {
	ts, s, _ := newServer(t)
	ctx := context.Background()
	mesh, _ := s.Store.PutBytes(glbOf(`{"asset": {"version": "2.0"}, "scenes": [{"nodes": [0]}],
		"nodes": [{"name": "crate_LOD0", "mesh": 0}], "meshes": [{"primitives": [{"attributes": {"POSITION": 0}}]}],
		"accessors": [{"count": 36}]}`))
	broken, _ := s.Store.PutBytes([]byte("not a GLB"))
	for _, a := range []*library.Asset{
		{ID: "old", JobID: "j1", Name: "crate", Preset: "draft", GLB: mesh, CreatedAt: time.Now().Add(-time.Hour)},
		{ID: "broken", JobID: "j2", Name: "broken", Preset: "draft", GLB: broken, CreatedAt: time.Now().Add(-time.Minute)},
	} {
		if err := s.DB.SaveAsset(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	var list []library.Asset
	if code := call(t, ts, "GET", "/v1/assets", nil, &list); code != 200 || len(list) != 2 {
		t.Fatalf("status %d, %d assets", code, len(list))
	}
	if list[1].Stats["triangles"] != 12.0 || list[1].Stats["vertices"] != 36.0 {
		t.Fatalf("stats %v", list[1].Stats)
	}
	// One the core cannot read is kept, and saved with empty stats so it is tried only once.
	if list[0].ID != "broken" || len(list[0].Stats) != 0 {
		t.Fatalf("broken %+v", list[0])
	}
	for id, want := range map[string]int{"old": 4, "broken": 0} {
		a, err := s.DB.Asset(ctx, id)
		if err != nil || a.Stats == nil || len(a.Stats) != want {
			t.Fatalf("%s saved with %v (%v)", id, a.Stats, err)
		}
	}
	var one library.Asset
	if code := call(t, ts, "GET", "/v1/assets/old", nil, &one); code != 200 || one.Stats["triangles"] != 12.0 {
		t.Fatalf("status %d, stats %v", code, one.Stats)
	}
	if code := call(t, ts, "PATCH", "/v1/assets/old", map[string]any{"stats": map[string]any{"triangles": 1}}, nil); code != 405 {
		t.Fatalf("PATCH answered %d: the stats come from the GLB now", code)
	}
}
