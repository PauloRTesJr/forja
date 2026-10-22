package planner

import (
	"testing"

	"forja/internal/presets"
)

const img = "blake3:0000000000000000000000000000000000000000000000000000000000000001"

func plan(t *testing.T, overrides map[string]any) *Plan {
	t.Helper()
	all, _ := presets.Load()
	p, err := all["mobile-prop"].WithOverrides(overrides)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := Build(p, img, 42)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestTextureOnlyChangeKeepsShapeKeys(t *testing.T) {
	a := plan(t, nil)
	b := plan(t, map[string]any{"paint.resolution": 2048})
	for _, id := range []string{"bg", "frame", "shape", "cleanup", "lowpoly", "uv"} {
		if a.Stages[a.Index(id)].Key != b.Stages[b.Index(id)].Key {
			t.Errorf("%s key changed on a texture-only edit", id)
		}
	}
	for _, id := range []string{"paint", "lods", "export"} {
		if a.Stages[a.Index(id)].Key == b.Stages[b.Index(id)].Key {
			t.Errorf("%s key did not change", id)
		}
	}
}

func TestKeysAreDeterministic(t *testing.T) {
	a, b := plan(t, nil), plan(t, nil)
	for i := range a.Stages {
		if a.Stages[i].Key != b.Stages[i].Key {
			t.Fatal("non-deterministic key")
		}
	}
}
