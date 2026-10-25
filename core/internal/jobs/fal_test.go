package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forja/internal/library"
	"forja/internal/provider/fal"
)

// fakeFalQueue answers fal's catalog, pricing and queue for one image-to-3D endpoint priced
// per generation, as fal.ai does for Hunyuan3D or TRELLIS 2.
func fakeFalQueue(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	file := map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"},
		"content_type": map[string]any{"type": "string"}}}
	ref := func(n string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + n} }
	api := map[string]any{
		"paths": map[string]any{
			"/x/oneshot": map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{
				"application/json": map[string]any{"schema": ref("In")}}}}},
			"/x/oneshot/requests/{request_id}": map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{
				"content": map[string]any{"application/json": map[string]any{"schema": ref("Out")}}}}}},
		},
		"components": map[string]any{"schemas": map[string]any{
			"In": map[string]any{"type": "object", "required": []any{"image_url"},
				"properties": map[string]any{"image_url": map[string]any{"type": "string"}, "seed": map[string]any{"type": "integer"}}},
			"Out":  map[string]any{"type": "object", "properties": map[string]any{"model_glb": ref("File")}},
			"File": file,
		}},
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"endpoint_id": "x/oneshot",
				"metadata": map[string]any{"display_name": "One Shot", "license_type": "commercial"}, "openapi": api}}})
		case r.URL.Path == "/v1/models/pricing":
			_ = json.NewEncoder(w).Encode(map[string]any{"prices": []any{map[string]any{"endpoint_id": "x/oneshot",
				"unit_price": 0.3, "unit": "generation", "currency": "USD"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/x/oneshot":
			base := srv.URL + "/x/requests/r1"
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "r1", "status_url": base + "/status",
				"response_url": base, "cancel_url": base + "/cancel"})
		case r.URL.Path == "/x/requests/r1/status/stream":
			for _, s := range []string{`{"status":"IN_QUEUE","queue_position":0}`, `{"status":"IN_PROGRESS"}`, `{"status":"COMPLETED"}`} {
				fmt.Fprintf(w, "data: %s\n\n", s)
			}
		case r.URL.Path == "/x/requests/r1":
			_ = json.NewEncoder(w).Encode(map[string]any{"model_glb": map[string]any{"url": srv.URL + "/files/out.glb"}})
		case r.URL.Path == "/files/out.glb":
			_, _ = w.Write([]byte("glTF\x02\x00\x00\x00 textured mesh"))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A job pinned to a hosted model on fal.ai, end to end through the real provider: the shape
// comes back textured, the texture stage is skipped, and what it cost is recorded.
func TestAJobOnFalRunsTheHostedModelAndRecordsItsCost(t *testing.T) {
	r, fp, _, st, img := setup(t)
	fp.set(func(f *fakeProvider) { f.pngs = true })
	srv := fakeFalQueue(t)
	p := fal.New("fal", "fal-test-key", []string{"x/oneshot"}, st)
	p.API, p.Queue = srv.URL, srv.URL
	r.providers.Add(p)
	// Pinning the model is enough, with the hub in routing order as connecting it puts it: no
	// free provider has the model, so its hub runs it.
	const routing = "[routing]\npriority = [\"local\", \"fal\"]\n[models.stages]\nshape = \"x/oneshot\"\n"
	project := withProject(t, routing+"[routing.budget_usd]\nper_job = 0.5\n")
	j, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageIDs(j); strings.Contains(got, "paint") || providers(j)["shape"] != "fal" {
		t.Fatalf("stages %s routes %v", got, providers(j))
	}
	got := wait(t, r, j.ID)
	if got.Status != library.Succeeded {
		t.Fatalf("%s: %s", got.Status, got.Error)
	}
	if spent, _ := r.db.SpentOn(context.Background(), j.ID); spent != 0.3 {
		t.Fatalf("recorded %v", spent)
	}
	// A limit below the price refuses the next one before anything is sent.
	tight := withProject(t, routing+"[routing.budget_usd]\nper_job = 0.2\n")
	if _, err := r.Submit(context.Background(), SubmitRequest{Input: img, Preset: "mobile-prop", Project: tight}); err == nil ||
		!strings.Contains(err.Error(), "per image") {
		t.Fatalf("over the limit: %v", err)
	}
}
