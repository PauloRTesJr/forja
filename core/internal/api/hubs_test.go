package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"forja/internal/config"
	"forja/internal/provider/fal"
	"forja/internal/secrets"
)

// fakeFal answers fal's model and pricing APIs for any endpoint: an image-to-3D model, except
// "x/video", which returns no mesh.
func fakeFal(t *testing.T) {
	t.Helper()
	file := map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"},
		"content_type": map[string]any{"type": "string"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The catalog is public; everything else needs the key.
		if r.URL.Path != "/v1/models" && r.Header.Get("Authorization") != "Key fal-test" {
			w.WriteHeader(401)
			return
		}
		ids := r.URL.Query()["endpoint_id"]
		switch r.URL.Path {
		case "/v1/models/pricing":
			var prices []any
			for _, id := range ids {
				prices = append(prices, map[string]any{"endpoint_id": id, "unit_price": 0.3, "unit": "generation", "currency": "USD"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"prices": prices})
		case "/v1/models":
			var models []any
			for _, id := range ids {
				out := "model_glb"
				if id == "x/video" {
					out = "video"
				}
				models = append(models, map[string]any{"endpoint_id": id, "metadata": map[string]any{"display_name": id},
					"openapi": map[string]any{
						"paths": map[string]any{
							"/" + id: map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{
								"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/In"}}}}}},
							"/" + id + "/requests/{request_id}": map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{
								"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Out"}}}}}}},
						},
						"components": map[string]any{"schemas": map[string]any{
							"In":   map[string]any{"type": "object", "required": []any{"image_url"}, "properties": map[string]any{"image_url": map[string]any{"type": "string"}}},
							"Out":  map[string]any{"type": "object", "properties": map[string]any{out: map[string]any{"$ref": "#/components/schemas/File"}}},
							"File": file,
						}},
					}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	api, queue := fal.APIBase, fal.QueueBase
	fal.APIBase, fal.QueueBase = srv.URL, srv.URL
	t.Cleanup(func() { fal.APIBase, fal.QueueBase = api, queue })
}

func TestAHubConnectsWithAKeyAndItsModelsCanBePinned(t *testing.T) {
	secrets.InMemory()
	fakeFal(t)
	ts, eng, _ := server(t)
	eng.installed["bg"], eng.installed["shape"], eng.installed["paint"] = true, true, true

	if code := call(t, ts, "POST", "/v1/providers", map[string]string{"kind": "api", "vendor": "fal", "token": "wrong"}, nil); code != 422 {
		t.Fatalf("a wrong key: %d", code)
	}
	var added map[string]any
	if code := call(t, ts, "POST", "/v1/providers", map[string]string{"kind": "api", "vendor": "fal", "token": "fal-test"}, &added); code != 201 {
		t.Fatalf("connect: %d %v", code, added)
	}
	if raw, _ := os.ReadFile(config.UserFile()); strings.Contains(string(raw), "fal-test") {
		t.Fatal("the key is in config.toml")
	}

	var models []modelView
	call(t, ts, "GET", "/v1/models?all=1&fresh=1", nil, &models)
	hosted := map[string]modelView{}
	for _, m := range models {
		if m.Provider == "fal" {
			hosted[m.ID] = m
		}
	}
	trellis := hosted["fal-ai/trellis-2"]
	if len(hosted) != len(fal.Recommended) || !trellis.Recommended || !trellis.Paid || trellis.Price == nil || trellis.ProviderLabel != "fal.ai" {
		t.Fatalf("hosted models %+v", hosted)
	}
	// Paid models are never the automatic choice.
	if len(trellis.Uses) != 0 {
		t.Fatalf("trellis used automatically: %+v", trellis.Uses)
	}

	if code := call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": "fal-ai/trellis-2", "provider": "fal"}, nil); code != 204 {
		t.Fatalf("pin: %d", code)
	}
	var presets []presetView
	call(t, ts, "GET", "/v1/presets", nil, &presets)
	for _, p := range presets {
		if p.ID != "mobile-prop" {
			continue
		}
		for _, pick := range p.Plan {
			if pick.Stage == "shape" && (pick.Provider != "fal" || !pick.Paid) {
				t.Fatalf("shape %+v", pick)
			}
			if pick.Stage == "paint" && !pick.Skipped {
				t.Fatalf("paint should be done by the hosted shape model: %+v", pick)
			}
		}
	}

	if code := call(t, ts, "POST", "/v1/providers/fal/models", map[string]string{"model": "x/video"}, nil); code != 422 {
		t.Fatalf("adding a model without a mesh: %d", code)
	}
	if code := call(t, ts, "POST", "/v1/providers/fal/models", map[string]string{"model": "x/new-3d"}, nil); code != 204 {
		t.Fatalf("adding: %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/providers/fal/models?model=fal-ai/trellis-2", nil, nil); code != 204 {
		t.Fatalf("removing: %d", code)
	}
	cfg, _ := config.Load("", nil)
	if cfg.Models.Stages["shape"] != "" || cfg.Routing.Stages["shape"] != "" {
		t.Fatalf("removing the pinned model left the pin: %v %v", cfg.Models.Stages, cfg.Routing.Stages)
	}
	for _, pc := range cfg.Providers {
		if pc.ID == "fal" && (len(pc.Models) != len(fal.Recommended) || pc.Models[len(pc.Models)-1] != "x/new-3d") {
			t.Fatalf("models %v", pc.Models)
		}
	}
}
