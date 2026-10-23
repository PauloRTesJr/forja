package fal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/store"
)

// schema builds an OpenAPI document shaped like fal's: a POST taking input, a result GET.
func schema(endpoint string, input, output map[string]any) map[string]any {
	file := map[string]any{"type": "object", "properties": map[string]any{
		"url": map[string]any{"type": "string"}, "content_type": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
	}}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	return map[string]any{
		"paths": map[string]any{
			"/" + endpoint: map[string]any{"post": map[string]any{
				"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": ref("In")}}},
			}},
			"/" + endpoint + "/requests/{request_id}": map[string]any{"get": map[string]any{
				"responses": map[string]any{"200": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": ref("Out")}}}},
			}},
		},
		"components": map[string]any{"schemas": map[string]any{"In": input, "Out": output, "File": file}},
	}
}

func props(kv ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

var (
	str     = map[string]any{"type": "string"}
	boolean = map[string]any{"type": "boolean", "default": false}
	seed    = map[string]any{"anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "null"}}}
	fileRef = map[string]any{"$ref": "#/components/schemas/File"}
)

func TestSchemasMapOntoStages(t *testing.T) {
	oneShot := MapSchema("a/one", schema("a/one",
		map[string]any{"type": "object", "required": []any{"image_url"}, "properties": props("image_url", str, "seed", seed, "should_texture", boolean)},
		map[string]any{"type": "object", "properties": props("model_glb", fileRef, "thumbnail", map[string]any{"anyOf": []any{fileRef, map[string]any{"type": "null"}}})}))
	if oneShot.Problem != "" || oneShot.Image != "image_url" || oneShot.Output != "model_glb" || oneShot.Seed != "seed" ||
		oneShot.Fixed["should_texture"] != true || strings.Join(oneShot.Tasks, ",") != "shape,texture" {
		t.Fatalf("one shot: %+v", oneShot)
	}
	retexture := MapSchema("a/re", schema("a/re",
		map[string]any{"type": "object", "required": []any{"image_url", "mesh_url"}, "properties": props("image_url", str, "mesh_url", str)},
		map[string]any{"type": "object", "properties": props("model_glb", fileRef)}))
	if retexture.Mesh != "mesh_url" || strings.Join(retexture.Tasks, ",") != "texture" {
		t.Fatalf("retexture: %+v", retexture)
	}
	for name, s := range map[string]Spec{
		"prompt": MapSchema("a/p", schema("a/p", map[string]any{"type": "object", "required": []any{"image_url", "prompt"},
			"properties": props("image_url", str, "prompt", str)}, map[string]any{"type": "object", "properties": props("model_glb", fileRef)})),
		"no mesh": MapSchema("a/n", schema("a/n", map[string]any{"type": "object", "properties": props("image_url", str)},
			map[string]any{"type": "object", "properties": props("video", fileRef)})),
	} {
		if s.Problem == "" {
			t.Errorf("%s: mapped a model Forja cannot call", name)
		}
	}
}

const key = "fal-test-key"

type fakeFal struct {
	*httptest.Server
	mu       sync.Mutex
	body     map[string]any
	keyLeaks []string
}

func newFake(t *testing.T) *fakeFal {
	f := &fakeFal{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFal) serve(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(r.URL.Path, "/files/") {
		if auth != "" {
			f.mu.Lock()
			f.keyLeaks = append(f.keyLeaks, r.URL.Path)
			f.mu.Unlock()
		}
		_, _ = w.Write([]byte("glTF\x02\x00\x00\x00 fake mesh"))
		return
	}
	if auth != "Key "+key {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
		return
	}
	switch {
	case r.URL.Path == "/v1/models/pricing":
		var prices []map[string]any
		for _, id := range r.URL.Query()["endpoint_id"] {
			prices = append(prices, map[string]any{"endpoint_id": id, "unit_price": 0.4, "unit": "generation", "currency": "USD"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"prices": prices})
	case r.URL.Path == "/v1/models":
		ids := r.URL.Query()["endpoint_id"]
		if len(ids) == 0 {
			ids = []string{"fal-ai/fake-3d", "fal-ai/fake-video"}
		}
		var models []map[string]any
		for _, id := range ids {
			out := props("model_glb", fileRef)
			if id == "fal-ai/fake-video" {
				out = props("video", fileRef)
			}
			models = append(models, map[string]any{"endpoint_id": id, "metadata": map[string]any{"display_name": "Fake " + id, "license_type": "commercial"},
				"openapi": schema(id, map[string]any{"type": "object", "required": []any{"image_url"},
					"properties": props("image_url", str, "seed", seed, "texture", boolean)}, map[string]any{"type": "object", "properties": out})})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models, "has_more": false})
	case r.Method == http.MethodPost && r.URL.Path == "/fal-ai/fake-3d":
		f.mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&f.body)
		f.mu.Unlock()
		base := f.URL + "/fal-ai/fake/requests/r1"
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "r1", "status_url": base + "/status",
			"response_url": base, "cancel_url": base + "/cancel"})
	case r.URL.Path == "/fal-ai/fake/requests/r1/status/stream":
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{`{"status":"IN_QUEUE","queue_position":2}`, `{"status":"IN_PROGRESS"}`, `{"status":"COMPLETED"}`} {
			fmt.Fprintf(w, "data: %s\n\n", s)
		}
	case r.URL.Path == "/fal-ai/fake/requests/r1":
		_ = json.NewEncoder(w).Encode(map[string]any{"model_glb": map[string]any{"url": f.URL + "/files/out.glb"}})
	default:
		w.WriteHeader(404)
	}
}

func newProvider(t *testing.T, f *fakeFal, k string) (*Provider, *store.Store) {
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("fal", k, []string{"fal-ai/fake-3d"}, st)
	p.API, p.Queue = f.URL, f.URL
	return p, st
}

func TestARunSendsTheImageInlineAndStoresTheMesh(t *testing.T) {
	f := newFake(t)
	p, st := newProvider(t, f, key)
	ctx := context.Background()
	if err := p.Health(ctx); err != nil {
		t.Fatal(err)
	}
	models, err := p.Models(ctx)
	if err != nil || len(models) != 1 || models[0].Price == nil || models[0].Price.Amount != 0.4 || !models[0].Ready() {
		t.Fatalf("models %+v %v", models, err)
	}
	img, _ := st.PutBytes([]byte("\x89PNG fake image"))
	events := make(chan engineapi.Event, 16)
	run, err := p.Run(ctx, engineapi.RunRequest{Seed: 7, Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape", Model: "fal-ai/fake-3d", Inputs: []string{img}}}}, events)
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("run %+v %v", run, err)
	}
	out := run.Stages["shape"].Outputs[0].Hash
	if !st.Has(out) {
		t.Fatal("the mesh is not in the store")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if uri, _ := f.body["image_url"].(string); !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("image sent as %q", uri)
	}
	if f.body["seed"] != float64(7) || f.body["texture"] != true {
		t.Fatalf("body %v", f.body)
	}
	if len(f.keyLeaks) > 0 {
		t.Fatalf("the API key went to the file host: %v", f.keyLeaks)
	}
}

func TestABadKeyAndAnUnusableModelSaySo(t *testing.T) {
	f := newFake(t)
	p, _ := newProvider(t, f, "wrong")
	if err := p.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the API key") {
		t.Fatalf("health: %v", err)
	}
	p, _ = newProvider(t, f, key)
	page, err := p.Search(context.Background(), "", "")
	if err != nil || len(page.Models) != 2 {
		t.Fatalf("search %+v %v", page, err)
	}
	for _, m := range page.Models {
		if m.ID == "fal-ai/fake-video" && m.Problem == "" {
			t.Fatal("a model without a mesh output looks usable")
		}
		if m.ID == "fal-ai/fake-3d" && (!m.Added || m.Problem != "") {
			t.Fatalf("fake-3d: %+v", m)
		}
	}
}
