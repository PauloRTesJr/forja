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
