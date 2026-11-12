package cli

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"forja/internal/api"
	"forja/internal/glb/glbtest"
	"forja/internal/library"
)

func texturedAsset(t *testing.T, h *harness, id string, orm image.Image) {
	t.Helper()
	ref, err := h.store.PutBytes(glbtest.Textured(t, glbtest.Stripes(120, 60), orm))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.db.SaveAsset(context.Background(), &library.Asset{ID: id, JobID: "j" + id, Name: id, Preset: "draft",
		GLB: ref, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestAssetsMaterialsListAndSetRoughnessAndMetallic(t *testing.T) {
	h := newHarness(t)
	texturedAsset(t, h, "crate", nil)

	out := h.ok("assets", "materials", "crate")
	if !strings.Contains(out, "INDEX") || !strings.Contains(out, "ROUGHNESS") || !strings.Contains(out, "#d4af37") ||
		!strings.Contains(out, "40%") || !strings.Contains(out, "20%") {
		t.Fatalf("materials:\n%s", out)
	}
	var view api.AssetMaterials
	h.okJSON(&view, "assets", "materials", "crate")
	if len(view.Materials) != 3 || view.Materials[0].Roughness != 0.8 || view.Painted {
		t.Fatalf("%+v", view)
	}

	h.okJSON(&view, "assets", "materials", "crate", "--set", "2.roughness=0.25", "--set", "2.metallic=1", "--set", "0.metallic=0.5")
	if view.Materials[2].Roughness != 0.25 || view.Materials[2].Metallic != 1 || view.Materials[0].Metallic != 0.5 ||
		view.Materials[0].Roughness != 0.8 {
		t.Fatalf("after set: %+v", view.Materials)
	}
	// Set again, only one value: the other stays.
	if out := h.ok("assets", "materials", "crate", "--set", "2.roughness=0.6"); !strings.Contains(out, "0.6") {
		t.Fatalf("set printed:\n%s", out)
	}
	h.okJSON(&view, "assets", "materials", "crate")
	if view.Materials[2].Roughness != 0.6 || view.Materials[2].Metallic != 1 {
		t.Fatalf("read back: %+v", view.Materials)
	}

	for name, args := range map[string][]string{
		"unknown material": {"--set", "9.roughness=0.5"},
		"out of range":     {"--set", "0.roughness=2"},
		"no dot":           {"--set", "roughness=0.5"},
		"unknown field":    {"--set", "0.shine=0.5"},
		"not a number":     {"--set", "0.roughness=high"},
		"overwrite alone":  {"--overwrite"},
	} {
		if r := h.cli("", append([]string{"assets", "materials", "crate"}, args...)...); r.code == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	if r := h.cli("", "assets", "materials", "nope"); r.code == 0 || !strings.Contains(r.err, "no asset") {
		t.Errorf("unknown asset: exit %d, %s", r.code, r.err)
	}
}

func TestAssetsMaterialsLeaveAModelsPaintingUnlessOverwritten(t *testing.T) {
	h := newHarness(t)
	orm := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			orm.SetRGBA(x, y, color.RGBA{255, uint8(30 + x*5), uint8(y * 6), 255})
		}
	}
	texturedAsset(t, h, "painted", orm)
	if out := h.ok("assets", "materials", "painted"); !strings.Contains(out, "painted by its model") {
		t.Fatalf("materials:\n%s", out)
	}
	r := h.cli("", "assets", "materials", "painted", "--set", "0.roughness=0.5")
	if r.code == 0 || !strings.Contains(r.err, "--overwrite") {
		t.Fatalf("exit %d, %s", r.code, r.err)
	}
	var view api.AssetMaterials
	h.okJSON(&view, "assets", "materials", "painted", "--set", "0.roughness=0.5", "--overwrite")
	if view.Painted || view.Materials[0].Roughness != 0.5 {
		t.Fatalf("%+v", view)
	}
}
