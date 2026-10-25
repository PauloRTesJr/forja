package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/paths"
	"forja/internal/provider/runpod"
	"forja/internal/providers"
	"forja/internal/secrets"
)

// fakeRunPodHealth answers RunPod's health check for endpoint ep-fake with key rp-fake-key, and
// a network volume's S3 API for any signed request from user_fake.
func fakeRunPodHealth(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/vol-fake/") {
			if !strings.Contains(r.Header.Get("Authorization"), "Credential=user_fake/") {
				w.WriteHeader(403)
				return
			}
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer rp-fake-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/v2/ep-fake/health" {
			w.WriteHeader(404)
			return
		}
		_, _ = io.WriteString(w, `{"jobs":{"completed":0,"failed":0,"inProgress":0,"inQueue":0,"retried":0},"workers":{"idle":0,"running":0}}`)
	}))
	t.Cleanup(srv.Close)
	api, vol := runpod.APIBase, runpod.VolumeEndpoint
	runpod.APIBase, runpod.VolumeEndpoint = srv.URL+"/v2", func(string) string { return srv.URL }
	t.Cleanup(func() { runpod.APIBase, runpod.VolumeEndpoint = api, vol })
}

func providerRows(t *testing.T, ts *httptest.Server) map[string]ProviderRow {
	t.Helper()
	var rows []ProviderRow
	call(t, ts, "GET", "/v1/providers", nil, &rows)
	out := map[string]ProviderRow{}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

func TestARunPodEndpointJoinsWithItsKeyAndVolume(t *testing.T) {
	secrets.InMemory()
	fakeRunPodHealth(t)
	ts, _, _ := server(t)
	body := map[string]any{"kind": "serverless", "vendor": "runpod", "endpoint_id": "ep-fake", "token": "rp-fake-key",
		"price_usd_per_second": 0.00031, "volume": "vol-fake", "region": "eu-ro-1", "volume_key_id": "user_fake",
		"volume_secret": "rps_fake_secret"}

	wrong := map[string]any{}
	for k, v := range body {
		wrong[k] = v
	}
	wrong["token"] = "wrong"
	if code := call(t, ts, "POST", "/v1/providers/test", wrong, nil); code != 422 {
		t.Fatalf("a wrong key tested %d", code)
	}
	wrong["token"], wrong["volume_key_id"] = "rp-fake-key", "user_other"
	if code := call(t, ts, "POST", "/v1/providers", wrong, nil); code != 422 {
		t.Fatalf("a wrong S3 key added %d", code)
	}
	var probe ProbeView
	if code := call(t, ts, "POST", "/v1/providers/test", body, &probe); code != 200 || !strings.Contains(probe.Detail, "0 workers idle") {
		t.Fatalf("test: %d %+v", code, probe)
	}
	var added AddedProvider
	if code := call(t, ts, "POST", "/v1/providers", body, &added); code != 201 {
		t.Fatalf("add: %d", code)
	}
	raw, _ := os.ReadFile(config.UserFile())
	for _, leak := range []string{"rp-fake-key", "rps_fake_secret", "user_fake"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("config.toml holds %s:\n%s", leak, raw)
		}
	}
	cfg, err := config.Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.Providers[0]
	if pc.Kind != "serverless" || pc.Vendor != "runpod" || pc.EndpointID != "ep-fake" || pc.PriceUSDPerSecond != 0.00031 ||
		pc.Volume != "vol-fake" || pc.Region != "EU-RO-1" || !pc.Paid {
		t.Fatalf("saved %+v", pc)
	}
	if !strings.Contains(strings.Join(cfg.Routing.Priority, ","), added.ID) {
		t.Fatalf("priority %v", cfg.Routing.Priority)
	}
	if v, _ := secrets.Get(providers.VolumeSecretName(added.ID)); v != "user_fake:rps_fake_secret" {
		t.Fatalf("volume key %q", v)
	}
	row := providerRows(t, ts)[added.ID]
	if row.Kind != "serverless" || row.Vendor != "runpod" || row.EndpointID != "ep-fake" || !row.Paid || !row.Healthy ||
		row.PriceUSDPerSecond != 0.00031 || row.Key == nil || !row.Key.Stored {
		t.Fatalf("row %+v", row)
	}

	if code := call(t, ts, "PUT", "/v1/providers/"+added.ID+"/token", map[string]string{"token": "wrong"}, nil); code != 422 {
		t.Fatalf("a wrong new key: %d", code)
	}
	if code := call(t, ts, "PUT", "/v1/providers/"+added.ID+"/token", map[string]string{"token": "rp-fake-key"}, nil); code != 204 {
		t.Fatalf("the same key again: %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/providers/"+added.ID, nil, nil); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if _, err := secrets.Get(providers.VolumeSecretName(added.ID)); err == nil {
		t.Fatal("the volume key outlived the provider")
	}
}

func TestAModalGatewayJoinsWithItsToken(t *testing.T) {
	secrets.InMemory()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer modal-fake-token" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(404) // no such artifact: the token works
	}))
	defer gw.Close()
	ts, _, _ := server(t)
	body := map[string]any{"kind": "serverless", "vendor": "modal", "url": gw.URL + "/", "token": "nope"}
	if code := call(t, ts, "POST", "/v1/providers", body, nil); code != 422 {
		t.Fatalf("a wrong token: %d", code)
	}
	body["token"], body["price_usd_per_second"] = "modal-fake-token", 0.000542
	var added AddedProvider
	if code := call(t, ts, "POST", "/v1/providers", body, &added); code != 201 || added.Label != "Modal" {
		t.Fatalf("add: %d %+v", code, added)
	}
	row := providerRows(t, ts)[added.ID]
	if row.Kind != "serverless" || row.Vendor != "modal" || !row.Paid || row.URL != gw.URL {
		t.Fatalf("row %+v", row)
	}
	body["url"] = "http://203.0.113.7:8000"
	if code := call(t, ts, "POST", "/v1/providers/test", body, nil); code != 422 {
		t.Fatalf("plain http off this machine: %d", code)
	}
}

func TestAComfyUIJoinsWithItsWorkflows(t *testing.T) {
	secrets.InMemory()
	comfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/system_stats" {
			w.WriteHeader(404)
			return
		}
		_, _ = io.WriteString(w, `{"system":{"comfyui_version":"0.3.60"},"devices":[{"name":"cuda:0 Test GPU","type":"cuda"}]}`)
	}))
	defer comfy.Close()
	ts, _, _ := server(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "my shape.json")
	_ = os.WriteFile(good, []byte(`{"1": {"class_type": "LoadImage", "inputs": {"image": "{{image}}"}}}`), 0o644)
	editor := filepath.Join(dir, "editor.json")
	_ = os.WriteFile(editor, []byte(`{"nodes": [], "links": []}`), 0o644)

	body := map[string]any{"kind": "comfyui", "url": comfy.URL, "workflows": map[string]string{"shape": editor}}
	var prob map[string]any
	if code := call(t, ts, "POST", "/v1/providers", body, &prob); code != 422 || !strings.Contains(prob["detail"].(string), "Export (API)") {
		t.Fatalf("an editor-format workflow: %d %v", code, prob)
	}
	body["workflows"] = map[string]string{"shape": good}
	var probe ProbeView
	if code := call(t, ts, "POST", "/v1/providers/test", body, &probe); code != 200 || probe.GPU != "cuda:0 Test GPU" {
		t.Fatalf("test %d %+v", code, probe)
	}
	var added AddedProvider
	if code := call(t, ts, "POST", "/v1/providers", body, &added); code != 201 {
		t.Fatalf("add %d", code)
	}
	kept := filepath.Join(paths.ConfigDir(), "comfyui", added.ID, "shape.json")
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the workflow was not kept: %v", err)
	}
	cfg, _ := config.Load("", nil)
	if pc := cfg.Providers[0]; pc.Kind != "comfyui" || pc.Workflows["shape"] != "comfyui/"+added.ID+"/shape.json" || pc.Paid {
		t.Fatalf("saved %+v", pc)
	}
	var models []ModelView
	call(t, ts, "GET", "/v1/models?all=1&fresh=1", nil, &models)
	found := false
	for _, m := range models {
		if m.Provider == added.ID && m.ID == "comfyui-shape" {
			found = true
		}
	}
	if !found {
		raw, _ := json.Marshal(models)
		t.Fatalf("the workflow is not a model: %s", raw)
	}
	if code := call(t, ts, "DELETE", "/v1/providers/"+added.ID, nil, nil); code != 204 {
		t.Fatalf("remove %d", code)
	}
	if _, err := os.Stat(kept); err == nil {
		t.Fatal("the kept workflow outlived the provider")
	}
}

func TestARunPodPodIsAPaidEngine(t *testing.T) {
	secrets.InMemory()
	pod := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer pod-fake-token" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(engineapi.Info{Version: "0.1.0", APIVersion: engineapi.APIVersion})
	}))
	defer pod.Close()
	ts, _, _ := server(t)
	body := map[string]any{"kind": "engine", "vendor": "runpod", "url": pod.URL, "token": "pod-fake-token", "price_usd_per_hour": 0.72}
	var added AddedProvider
	if code := call(t, ts, "POST", "/v1/providers", body, &added); code != 201 || added.Probe == nil || added.Probe.Fingerprint == "" {
		t.Fatalf("add %d %+v", code, added)
	}
	cfg, _ := config.Load("", nil)
	if pc := cfg.Providers[0]; !pc.Paid || pc.Vendor != "runpod" || pc.PriceUSDPerSecond != 0.0002 || pc.Fingerprint == "" {
		t.Fatalf("saved %+v", pc)
	}
}
