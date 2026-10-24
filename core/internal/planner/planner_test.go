package planner

import (
	"errors"
	"slices"
	"strings"
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
	p = p.Active()
	if err != nil {
		t.Fatal(err)
	}
	pl, err := Build(p, map[string]string{presets.InputImage: img}, 42, fp)
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
		"lowpoly", "paint", "bake", "lods", "export")
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
			"shape", "cleanup", "lowpoly", "paint", "bake", "lods", "export")
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
	if _, err := Build(all["mobile-prop"].Active(), map[string]string{presets.InputImage: img}, 42, broken); err == nil {
		t.Fatal("expected an error")
	}
}

// Keys recorded before jobs could carry views: a job without them must hit the same cache.
func TestKeysWithoutViewsAreTheOnesCachedBefore(t *testing.T) {
	want := map[string]string{
		"bg": "stage:f1bf59b117a99b0d619662a05bf179ba", "frame": "stage:6610a7fdfe70ef593770199c261bcfdd",
		"shape": "stage:c2feed8117db82eb65a582be2babdf98", "cleanup": "stage:b2a99c42d680dd704549b049cd3f2905",
		"lowpoly": "stage:6304b8e48c9c09f9349c5c5a8abed897", "paint": "stage:a32432f3b6dddd906de5f3dacd42b2d1",
		"bake": "stage:6822c2b65ef5d81e936f1383c578d914", "lods": "stage:2c5805f2a063807a3a51c729738b9946",
		"export": "stage:975438e15178795e82c216bd0dc926e2",
	}
	for _, s := range plan(t, nil, same).Stages {
		if s.Key != want[s.ID] {
			t.Errorf("stage %s: key %s, was %s", s.ID, s.Key, want[s.ID])
		}
	}
}

const left = "blake3:0000000000000000000000000000000000000000000000000000000000000002"

// viewPlan plans mobile-prop with a left view for the stages whose type is in readers.
func viewPlan(t *testing.T, view string, readers ...string) *Plan {
	t.Helper()
	all, _ := presets.Load()
	reads := func(s presets.Stage) []string {
		if slices.Contains(readers, s.Use) {
			return []string{"front", "left", "back"}
		}
		return nil
	}
	p := all["mobile-prop"].Active().WithViews([]string{"left"}, reads)
	pl, err := Build(p, map[string]string{presets.InputImage: img, presets.InputView("left"): view}, 42, same)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}

func TestASideViewIsPreparedLikeTheFrontAndReachesItsReaders(t *testing.T) {
	pl := viewPlan(t, left, "shape", "texture")
	var ids []string
	for _, s := range pl.Stages {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ","); got != "bg,frame,bg-left,frame-left,shape,cleanup,lowpoly,paint,bake,lods,export" {
		t.Fatalf("stages %s", got)
	}
	byID := map[string]Stage{}
	for _, s := range pl.Stages {
		byID[s.ID] = s
	}
	if s := byID["bg-left"]; strings.Join(s.Inputs, ",") != presets.InputView("left") || s.Use != "background-removal" {
		t.Fatalf("bg-left %+v", s)
	}
	if s := byID["shape"]; strings.Join(s.Inputs, ",") != "frame,frame-left" || strings.Join(s.Views, ",") != "front,left" {
		t.Fatalf("shape %+v", s)
	}
	if s := byID["paint"]; strings.Join(s.Inputs, ",") != "lowpoly,frame,frame-left" || strings.Join(s.Views, ",") != ",front,left" {
		t.Fatalf("paint %+v", s)
	}
	// The front's own stages keep their keys; a different side view rekeys only what reads it.
	front := plan(t, nil, same)
	if byID["bg"].Key != front.Stages[0].Key || byID["frame"].Key != front.Stages[1].Key {
		t.Fatal("the front's preparation was rekeyed")
	}
	other := viewPlan(t, "blake3:0000000000000000000000000000000000000000000000000000000000000003", "shape", "texture")
	for i, s := range pl.Stages {
		if s.ID == "bg" || s.ID == "frame" {
			if s.Key != other.Stages[i].Key {
				t.Errorf("%s rekeyed by another side view", s.ID)
			}
		} else if s.Key == other.Stages[i].Key {
			t.Errorf("%s kept its key with another side view", s.ID)
		}
	}
}

func TestViewsNoStageReadsChangeNothing(t *testing.T) {
	all, _ := presets.Load()
	p := all["mobile-prop"].Active()
	if p.WithViews([]string{"left"}, func(presets.Stage) []string { return nil }) != p {
		t.Fatal("a preset whose models read one image was rewritten")
	}
}
