package providers

import (
	"path/filepath"
	"testing"

	"forja/internal/config"
	"forja/internal/provider"
	"forja/internal/provider/comfyui"
	"forja/internal/provider/engine"
	"forja/internal/provider/runpod"
)

func TestEachKindBuildsItsProvider(t *testing.T) {
	t.Setenv("FORJA_CONFIG_DIR", t.TempDir())
	for _, tc := range []struct {
		pc   config.Provider
		kind provider.Kind
		paid bool
	}{
		{config.Provider{ID: "pod", Kind: "engine", Vendor: "runpod", URL: "https://203.0.113.7:7861", Paid: true}, provider.KindEngine, true},
		{config.Provider{ID: "rp", Kind: "serverless", Vendor: "runpod", EndpointID: "ep"}, provider.KindServerless, true},
		{config.Provider{ID: "modal", Kind: "serverless", Vendor: "modal", URL: "https://x.modal.run"}, provider.KindServerless, true},
		{config.Provider{ID: "comfy", Kind: "comfyui", URL: "http://192.168.1.30:8188"}, provider.KindComfyUI, false},
	} {
		p, err := BuildWithKey(tc.pc, nil, "fake-key")
		if err != nil {
			t.Fatalf("%s: %v", tc.pc.ID, err)
		}
		if p.Kind() != tc.kind || p.Paid() != tc.paid || p.ID() != tc.pc.ID {
			t.Fatalf("%s: kind %s paid %v", tc.pc.ID, p.Kind(), p.Paid())
		}
	}
	if _, ok := mustBuild(t, config.Provider{ID: "m", Kind: "serverless", Vendor: "modal"}).(*engine.Provider); !ok {
		t.Fatal("a Modal gateway is reached as an engine")
	}
	if _, ok := mustBuild(t, config.Provider{ID: "c", Kind: "comfyui"}).(*comfyui.Provider); !ok {
		t.Fatal("not a ComfyUI provider")
	}
	if _, err := BuildWithKey(config.Provider{ID: "x", Kind: "serverless", Vendor: "vast"}, nil, ""); err == nil {
		t.Fatal("an unknown serverless vendor was built")
	}
}

func TestARunPodURLPointsItAtAnotherGateway(t *testing.T) {
	p := mustBuild(t, config.Provider{ID: "rp", Kind: "serverless", Vendor: "runpod", EndpointID: "ep", URL: "http://127.0.0.1:8000/v2/"})
	if got := p.(*runpod.Provider).URL(); got != "http://127.0.0.1:8000/v2/ep" {
		t.Fatalf("url %s", got)
	}
}

func TestWorkflowsLiveBesideTheConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", dir)
	abs := filepath.Join(dir, "elsewhere", "w.json")
	got := WorkflowPaths(map[string]string{"shape": "comfyui/c/shape.json", "texture": abs})
	if got["shape"] != filepath.Join(dir, "comfyui", "c", "shape.json") || got["texture"] != abs {
		t.Fatalf("paths %v", got)
	}
}

func mustBuild(t *testing.T, pc config.Provider) provider.Provider {
	t.Helper()
	p, err := BuildWithKey(pc, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
