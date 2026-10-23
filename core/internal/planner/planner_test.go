package planner

import (
	"errors"
	"testing"

	"forja/internal/presets"
)

const img = "blake3:0000000000000000000000000000000000000000000000000000000000000001"

// same reports one fixed fingerprint for every stage, like a provider that never changes.
func same(presets.Stage) (Fingerprint, error) { return Fingerprint{Adapter: "1"}, nil }

func plan(t *testing.T, overrides map[string]any, fp Fingerprints) *Plan {
	t.Helper()
	all, _ := presets.Load()
	p, err := all["mobile-prop"].WithOverrides(overrides)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := Build(p, img, 42, fp)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

// changed reports whether each stage's key differs between two plans of the same preset.
func changed(a, b *Plan) map[string]bool {
	out := map[string]bool{}
	for i, s := range a.Stages {
		out[s.ID] = s.Key != b.Stages[i].Key
	}
	return out
}

func expectChanged(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	wanted := map[string]bool{}
	for _, id := range want {
		wanted[id] = true
	}
	for id, c := range got {
		if c != wanted[id] {
			t.Errorf("stage %s: key changed = %v, want %v", id, c, wanted[id])
		}
	}
}

func TestTextureOnlyChangeKeepsShapeKeys(t *testing.T) {
	a := plan(t, nil, same)
	b := plan(t, map[string]any{"paint.resolution": 2048}, same)
	expectChanged(t, changed(a, b), "paint", "bake", "lods", "export")
}

func TestKeysAreDeterministic(t *testing.T) {
	expectChanged(t, changed(plan(t, nil, same), plan(t, nil, same)))
}

func TestNewAdapterVersionRekeysThatStageAndWhatFollows(t *testing.T) {
	bumped := func(s presets.Stage) (Fingerprint, error) {
		if s.Use == "mesh.decimate" {
			return Fingerprint{Adapter: "2"}, nil
		}
		return same(s)
	}
	expectChanged(t, changed(plan(t, nil, same), plan(t, nil, bumped)),
		"lowpoly", "uv", "paint", "bake", "lods", "export")
}

func TestNewWeightsOrUpstreamRekeyTheModelStage(t *testing.T) {
	for _, fp := range []Fingerprint{{Adapter: "1", Weights: "rev2"}, {Adapter: "1", Upstream: "sha2"}} {
		other := func(s presets.Stage) (Fingerprint, error) {
			if s.ID == "shape" {
				return fp, nil
			}
			return same(s)
		}
		expectChanged(t, changed(plan(t, nil, same), plan(t, nil, other)),
			"shape", "cleanup", "lowpoly", "uv", "paint", "bake", "lods", "export")
	}
}

func TestAStageThatCannotBeFingerprintedFailsTheBuild(t *testing.T) {
	all, _ := presets.Load()
	broken := func(s presets.Stage) (Fingerprint, error) {
		if s.ID == "paint" {
			return Fingerprint{}, errors.New("provider local has no model stub-paint")
		}
		return same(s)
	}
	if _, err := Build(all["mobile-prop"], img, 42, broken); err == nil {
		t.Fatal("expected an error")
	}
}
