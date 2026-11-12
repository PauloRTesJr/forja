package planner

import (
	"slices"
	"strings"
	"testing"

	"forja/internal/presets"
)

const img = "blake3:0000000000000000000000000000000000000000000000000000000000000001"

func plan(t *testing.T, overrides map[string]any) *Plan {
	t.Helper()
	all, _ := presets.Load()
	p, err := all["mobile-prop"].WithOverrides(overrides)
	p = p.Active()
	if err != nil {
		t.Fatal(err)
	}
	pl, err := Build(p, map[string]string{presets.InputImage: img}, 42)
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
	a := plan(t, nil)
	b := plan(t, map[string]any{"paint.resolution": 2048})
	expectChanged(t, changed(a, b), "paint", "bake", "lods", "export")
}

func TestADenserMeshAddsOneStageAndRekeysWhatReadsIt(t *testing.T) {
	off, on := plan(t, nil), plan(t, map[string]any{"dense.enabled": true})
	ids := func(p *Plan) string {
		var out []string
		for _, s := range p.Stages {
			out = append(out, s.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(off); got != "bg,frame,shape,cleanup,lowpoly,paint,bake,lods,export" {
		t.Fatalf("off: %s", got)
	}
	if got := ids(on); got != "bg,frame,shape,cleanup,lowpoly,dense,paint,bake,lods,export" {
		t.Fatalf("on: %s", got)
	}
	keys := func(p *Plan) map[string]string {
		out := map[string]string{}
		for _, s := range p.Stages {
			out[s.ID] = s.Key
		}
		return out
	}
	a, b := keys(off), keys(on)
	for _, id := range []string{"bg", "frame", "shape", "cleanup", "lowpoly"} {
		if a[id] != b[id] {
			t.Errorf("%s was rekeyed by the denser mesh", id)
		}
	}
	for _, id := range []string{"paint", "bake", "lods", "export"} {
		if a[id] == b[id] {
			t.Errorf("%s kept its key although it now works on another mesh", id)
		}
	}
	// What the low-poly and the high-poly are stays in the bake's inputs, in order.
	bake := on.Stages[on.Index("bake")]
	if got := strings.Join(bake.Inputs, ","); got != "paint,cleanup,lowpoly" {
		t.Errorf("bake reads %s", got)
	}
	if got := strings.Join(on.Stages[on.Index("paint")].Inputs, ","); got != "dense,frame" {
		t.Errorf("paint reads %s", got)
	}
}

func TestKeysAreDeterministic(t *testing.T) {
	expectChanged(t, changed(plan(t, nil), plan(t, nil)))
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
	pl, err := Build(p, map[string]string{presets.InputImage: img, presets.InputView("left"): view}, 42)
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
	front := plan(t, nil)
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
