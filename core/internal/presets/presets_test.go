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

func TestHeroCharacterRigsBeforeTheLODs(t *testing.T) {
	all, _ := Load()
	hero := all["hero-character"]
	reads := func(p *Preset, id string) []string {
		for _, s := range p.Stages {
			if s.ID == id {
				return s.Inputs
			}
		}
		t.Fatalf("no stage %s in %s", id, p.ID)
		return nil
	}
	off := hero.Active()
	for _, s := range off.Stages {
		if s.Use == "rig" {
			t.Fatal("rig is on by default")
		}
	}
	if got := reads(off, "lods"); len(got) != 1 || got[0] != "bake" {
		t.Fatalf("with rig off, lods reads %v", got)
	}
	on, err := hero.WithOverrides(map[string]any{"rig.enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	active := on.Active()
	if got := reads(active, "rig"); len(got) != 1 || got[0] != "bake" {
		t.Fatalf("rig reads %v", got)
	}
	if got := reads(active, "lods"); len(got) != 1 || got[0] != "rig" {
		t.Fatalf("with rig on, lods reads %v", got)
	}
	// The other presets are props and pieces: nothing to rig.
	for id, p := range all {
		for _, s := range p.Stages {
			if s.Use == "rig" && id != "hero-character" {
				t.Errorf("%s has a rig stage", id)
			}
		}
	}
}

func TestAQuadRemeshStandsInForDecimationWithItsBudget(t *testing.T) {
	all, _ := Load()
	for _, id := range []string{"environment-piece", "hero-character"} {
		p := all[id]
		stage := func(p *Preset, id string) *Stage {
			for i := range p.Stages {
				if p.Stages[i].ID == id {
					return &p.Stages[i]
				}
			}
			return nil
		}
		off := p.Active()
		if stage(off, "remesh") != nil || stage(off, "uv") != nil || stage(off, "paint").Inputs[0] != "lowpoly" {
			t.Fatalf("%s: Blender stages run by default: %+v", id, off.Stages)
		}
		on, err := p.WithOverrides(map[string]any{"remesh.enabled": true, "uv.enabled": true, "lowpoly.target_tris": 9000})
		if err != nil {
			t.Fatal(err)
		}
		a := on.Active()
		if stage(a, "lowpoly") != nil {
			t.Fatalf("%s: decimation still runs beside the remesh", id)
		}
		if got := stage(a, "remesh").Params["target_tris"]; got != 9000 {
			t.Fatalf("%s: remesh budget %v, want the decimation's", id, got)
		}
		if stage(a, "uv").Inputs[0] != "remesh" || stage(a, "paint").Inputs[0] != "uv" {
			t.Fatalf("%s: uv reads %v, paint reads %v", id, stage(a, "uv").Inputs, stage(a, "paint").Inputs)
		}
		if stage(p, "remesh").Params["target_tris"] != nil {
			t.Fatalf("%s: the preset itself changed", id)
		}
	}
}

func TestAStageCanOnlyReplaceALaterOneThatAlwaysRuns(t *testing.T) {
	for _, raw := range []string{
		"id: x\nstages:\n  - {id: a, use: shape}\n  - {id: b, use: mesh.remesh, optional: {label: B, replaces: a}}\n  - {id: c, use: export}\n",
		"id: x\nstages:\n  - {id: b, use: mesh.remesh, optional: {label: B, replaces: nope}}\n  - {id: c, use: export}\n",
	} {
		if _, err := Parse([]byte(raw), "t"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestOptionalStagesNeedALabel(t *testing.T) {
	_, err := Parse([]byte("id: x\nstages:\n  - {id: a, use: shape, optional: {default: true}}\n  - {id: b, use: export}\n"), "t")
	if err == nil {
		t.Fatal("unlabelled optional stage accepted")
	}
}
