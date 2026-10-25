package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forja/internal/config"
	"forja/internal/provider/runpod"
	"forja/internal/providers"
	"forja/internal/secrets"
)

func TestRentedGPUsAndAComfyUIAreAddedFromTheCommandLine(t *testing.T) {
	h := newHarness(t)
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case strings.HasPrefix(r.URL.Path, "/vol-fake/") && strings.Contains(auth, "Credential=user_fake/"):
			w.WriteHeader(404) // the volume answers, without that object
		case r.URL.Path == "/v2/ep-fake/health" && auth == "Bearer rp-fake-key":
			_, _ = io.WriteString(w, `{"jobs":{"inQueue":0},"workers":{"idle":2,"running":1}}`)
		case strings.HasPrefix(r.URL.Path, "/v1/artifacts/") && auth == "Bearer modal-fake-token":
			w.WriteHeader(404)
		case r.URL.Path == "/system_stats":
			_, _ = io.WriteString(w, `{"system":{"comfyui_version":"0.3.60"},"devices":[{"name":"cuda:0 Test GPU"}]}`)
		default:
			w.WriteHeader(401)
		}
	}))
	defer cloud.Close()
	api, vol := runpod.APIBase, runpod.VolumeEndpoint
	runpod.APIBase, runpod.VolumeEndpoint = cloud.URL+"/v2", func(string) string { return cloud.URL }
	defer func() { runpod.APIBase, runpod.VolumeEndpoint = api, vol }()

	if out := h.ok("providers", "add", "--help"); !strings.Contains(out, "runpod <endpoint-id>") {
		t.Fatalf("help:\n%s", out)
	}
	if r := h.cli("wrong\n", "providers", "test", "runpod", "ep-fake"); r.code == 0 || !strings.Contains(r.err, "API key") {
		t.Fatalf("a wrong key: %d %s", r.code, r.err)
	}
	if out := h.cli("rp-fake-key\n", "providers", "test", "runpod", "ep-fake"); out.code != 0 || !strings.Contains(out.out, "2 workers idle, 1 running") {
		t.Fatalf("test: %+v", out)
	}
	// What PowerShell pipes starts with a byte-order mark.
	if out := h.cli("\ufeffrp-fake-key\r\n", "providers", "test", "runpod", "ep-fake"); out.code != 0 {
		t.Fatalf("a key piped from PowerShell: %+v", out)
	}
	if r := h.cli("rp-fake-key\n", "providers", "add", "runpod", "ep-fake", "--volume", "vol-fake", "--datacenter", "EU-RO-1",
		"--volume-key-id", "user_fake"); r.code == 0 || !strings.Contains(r.err, "S3 secret") {
		t.Fatalf("a volume without its secret: %d %s", r.code, r.err)
	}
	out := h.cli("rp-fake-key\nrps_fake_secret\n", "providers", "add", "runpod", "ep-fake", "--price", "0.00031",
		"--volume", "vol-fake", "--datacenter", "EU-RO-1", "--volume-key-id", "user_fake")
	if out.code != 0 || !strings.Contains(out.out, "Added RunPod ep-fake as runpod-ep-fake") {
		t.Fatalf("add runpod: %+v", out)
	}
	if v, _ := secrets.Get(providers.VolumeSecretName("runpod-ep-fake")); v != "user_fake:rps_fake_secret" {
		t.Fatalf("volume key %q", v)
	}

	if r := h.cli("modal-fake-token\n", "providers", "add", "modal", cloud.URL, "--price", "0.000542"); r.code != 0 {
		t.Fatalf("add modal: %+v", r)
	}

	dir := t.TempDir()
	workflow := filepath.Join(dir, "bg.json")
	_ = os.WriteFile(workflow, []byte(`{"1": {"class_type": "LoadImage", "inputs": {"image": "{{image}}"}}}`), 0o644)
	if r := h.cli("", "providers", "add", "comfyui", cloud.URL, "--workflow", "bg"); r.code == 0 {
		t.Fatal("a workflow flag without a file was taken")
	}
	if r := h.cli("", "providers", "add", "comfyui", cloud.URL, "--label", "Studio ComfyUI",
		"--workflow", "background-removal="+workflow); r.code != 0 || !strings.Contains(r.out, "ComfyUI 0.3.60") {
		t.Fatalf("add comfyui: %+v", r)
	}

	ls := h.ok("providers", "ls")
	for _, want := range []string{"serverless runpod (paid)", "endpoint ep-fake", "serverless modal (paid)", "studio-comfyui", "comfyui"} {
		if !strings.Contains(ls, want) {
			t.Fatalf("providers ls lacks %q:\n%s", want, ls)
		}
	}
	cfg, _ := config.Load("", nil)
	if len(cfg.Providers) != 3 || cfg.Providers[2].Workflows["background-removal"] == "" {
		t.Fatalf("providers %+v", cfg.Providers)
	}
	if r := h.cli("", "providers", "add", "vastai", "x"); r.code == 0 || !strings.Contains(r.err, "unknown provider") {
		t.Fatalf("an unknown vendor: %+v", r)
	}
	h.ok("providers", "rm", "runpod-ep-fake", "modal", "studio-comfyui")
	if _, err := secrets.Get(providers.VolumeSecretName("runpod-ep-fake")); err == nil {
		t.Fatal("the volume key outlived its provider")
	}
}
