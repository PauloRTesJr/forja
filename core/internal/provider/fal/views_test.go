package fal

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/store"
)

func TestMultiViewSchemasMapEachViewToItsField(t *testing.T) {
	glb := map[string]any{"type": "object", "properties": props("model_glb", fileRef)}
	perView := MapSchema("a/mv", schema("a/mv", map[string]any{"type": "object",
		"required":   []any{"front_image_url", "left_image_url", "back_image_url"},
		"properties": props("front_image_url", str, "back_image_url", str, "left_image_url", str, "right_image_url", str),
	}, glb))
	if perView.Problem != "" || perView.Image != "front_image_url" || perView.Views["right"] != "right_image_url" ||
		strings.Join(perView.ViewsRequired, ",") != "left,back" {
		t.Fatalf("per view: %+v", perView)
	}
	list := MapSchema("a/list", schema("a/list", map[string]any{"type": "object", "required": []any{"image_urls"},
		"properties": props("image_urls", map[string]any{"type": "array", "items": str})}, glb))
	if list.Problem != "" || !list.ImageList || len(list.ViewsRequired) != 0 {
		t.Fatalf("list: %+v", list)
	}
	e := &entry{Spec: list, Name: "list"}
	if m := e.model(); strings.Join(m.Views, ",") != "front,left,back,right" {
		t.Fatalf("a list takes every view: %v", m.Views)
	}
	e = &entry{Spec: perView, Name: "per view"}
	if m := e.model(); strings.Join(m.Views, ",") != "front,left,back,right" || strings.Join(m.ViewsRequired, ",") != "left,back" {
		t.Fatalf("per view model %v %v", m.Views, m.ViewsRequired)
	}
}

func TestARunSendsEachViewToItsField(t *testing.T) {
	f := newFake(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("fal", key, []string{"fal-ai/fake-mv"}, st)
	p.API, p.Queue = f.URL, f.URL
	ctx := context.Background()
	models, err := p.Models(ctx)
	if err != nil || len(models) != 1 || strings.Join(models[0].ViewsRequired, ",") != "back" {
		t.Fatalf("models %+v %v", models, err)
	}
	front, _ := st.PutBytes([]byte("\x89PNG front"))
	back, _ := st.PutBytes([]byte("\x89PNG back"))
	shape := engineapi.StageSpec{ID: "shape", Use: "shape", Model: "fal-ai/fake-mv", Inputs: []string{front}}
	run, err := p.Run(ctx, engineapi.RunRequest{Stages: []engineapi.StageSpec{shape}}, make(chan engineapi.Event, 16))
	if err != nil || run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "needs the back view") {
		t.Fatalf("without its back view: %+v %v", run, err)
	}
	shape.Inputs, shape.Views = []string{front, back}, []string{"front", "back"}
	run, err = p.Run(ctx, engineapi.RunRequest{Stages: []engineapi.StageSpec{shape}}, make(chan engineapi.Event, 16))
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("run %+v %v", run, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	frontURI, _ := f.body["front_image_url"].(string)
	backURI, _ := f.body["back_image_url"].(string)
	if !strings.HasPrefix(frontURI, "data:image/png;base64,") || !strings.HasPrefix(backURI, "data:image/png;base64,") ||
		frontURI == backURI || f.body["left_image_url"] != nil {
		t.Fatalf("body %v", f.body)
	}
}
