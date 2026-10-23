package presets

import "testing"

func TestBuiltinsParse(t *testing.T) {
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	mp := all["mobile-prop"]
	if mp == nil {
		t.Fatal("mobile-prop missing")
	}
	if got := mp.Stages[1].Inputs; len(got) != 1 || got[0] != "bg" {
		t.Fatalf("implicit input = %v", got)
	}
}

func TestOverrides(t *testing.T) {
	all, _ := Load()
	p, err := all["mobile-prop"].WithOverrides(map[string]any{"lowpoly.target_tris": 3000, "shape.model": "triposg"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Stages[4].Params["target_tris"] != 3000 || p.Stages[2].Model != "triposg" {
		t.Fatal("override not applied")
	}
	if all["mobile-prop"].Stages[4].Params["target_tris"] != 5000 {
		t.Fatal("original mutated")
	}
	if _, err := all["mobile-prop"].WithOverrides(map[string]any{"nope.x": 1}); err == nil {
		t.Fatal("unknown stage accepted")
	}
}

func TestRejectsForwardReferences(t *testing.T) {
	_, err := Parse([]byte("id: x\nstages:\n  - {id: a, use: shape, inputs: [b]}\n  - {id: b, use: export}\n"), "t")
	if err == nil {
		t.Fatal("forward reference accepted")
	}
}

func TestBuiltinsNameNoModels(t *testing.T) {
	all, _ := Load()
	for id, p := range all {
		for _, s := range p.Stages {
			if s.Model != "" {
				t.Errorf("built-in %s pins model %s on stage %s; models are picked at submit", id, s.Model, s.ID)
			}
		}
	}
}

func TestOptionalStagesSwitchAndRewire(t *testing.T) {
	all, _ := Load()
	mp := all["mobile-prop"]
	ids := func(p *Preset) (out []string) {
		for _, s := range p.Stages {
			out = append(out, s.ID)
		}
		return out
	}
	bakeInputs := func(p *Preset) []string {
		for _, s := range p.Stages {
			if s.ID == "bake" {
				return s.Inputs
			}
		}
		return nil
	}
	off := mp.Active()
	if got := bakeInputs(off); got[0] != "paint" || got[1] != "cleanup" {
		t.Fatalf("with match off, bake reads %v", got)
	}
	for _, id := range ids(off) {
		if id == "match" {
			t.Fatal("match is on by default")
		}
	}
	on, err := mp.WithOverrides(map[string]any{"match.enabled": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := bakeInputs(on.Active()); got[0] != "match" {
		t.Fatalf("with match on, bake reads %v", got)
	}
	if _, err := mp.WithOverrides(map[string]any{"paint.enabled": false}); err == nil {
		t.Fatal("switched off a stage that is not optional")
	}
}

func TestOptionalStagesNeedALabel(t *testing.T) {
	_, err := Parse([]byte("id: x\nstages:\n  - {id: a, use: shape, optional: {default: true}}\n  - {id: b, use: export}\n"), "t")
	if err == nil {
		t.Fatal("unlabelled optional stage accepted")
	}
}
