// Package providers builds the providers configured in [[providers]], with their secrets.
package providers

import (
	"fmt"
	"path/filepath"
	"strings"

	"forja/internal/config"
	"forja/internal/paths"
	"forja/internal/provider"
	"forja/internal/provider/comfyui"
	"forja/internal/provider/engine"
	"forja/internal/provider/fal"
	"forja/internal/provider/runpod"
	"forja/internal/secrets"
	"forja/internal/store"
)

// Modal is the vendor of a Modal gateway (docker/modal/app.py): kind = "serverless".
const Modal = "modal"

// SecretName is the keychain entry a provider's token or key is stored under.
func SecretName(id string) string { return "provider-" + id }

// VolumeSecretName holds a RunPod network volume's S3 key, as "<access key>:<secret>".
func VolumeSecretName(id string) string { return SecretName(id) + "-volume" }

// Build makes a provider from its config. A missing secret is not an error: the provider
// then answers 401s, which its health shows.
func Build(pc config.Provider, st *store.Store) (provider.Provider, error) {
	key := ""
	if pc.Secret != "" {
		key, _ = secrets.Get(pc.Secret)
	}
	return BuildWithKey(pc, st, key)
}

// BuildWithKey makes a provider with a key that is not stored yet, to try it first.
func BuildWithKey(pc config.Provider, st *store.Store, key string) (provider.Provider, error) {
	switch provider.Kind(pc.Kind) {
	case provider.KindEngine:
		p := engine.New(pc.ID, provider.KindEngine, pc.Paid, false, engine.Static(pc.URL, key), st)
		p.Pin(pc.Fingerprint)
		p.Price(pc.PriceUSDPerSecond)
		return p, nil
	case provider.KindAPI:
		if pc.Vendor == fal.Vendor {
			return fal.New(pc.ID, key, pc.Models, st), nil
		}
		return nil, fmt.Errorf("model hub %q is not supported", pc.Vendor)
	case provider.KindServerless:
		switch pc.Vendor {
		case runpod.Vendor:
			p := runpod.New(pc.ID, pc.EndpointID, key, pc.PriceUSDPerSecond, volume(pc), st)
			if pc.URL != "" {
				p.API = strings.TrimRight(pc.URL, "/") // a gateway that speaks RunPod's API, or its local test server
			}
			return p, nil
		case Modal:
			// The gateway speaks the engine's API; GPU seconds are what it bills.
			p := engine.New(pc.ID, provider.KindServerless, true, false, engine.Static(pc.URL, key), st)
			p.Price(pc.PriceUSDPerSecond)
			return p, nil
		}
		return nil, fmt.Errorf("serverless vendor %q is not supported", pc.Vendor)
	case provider.KindComfyUI:
		return comfyui.New(pc.ID, pc.URL, key, WorkflowPaths(pc.Workflows), pc.Paid, pc.PriceUSDPerSecond, st), nil
	}
	return nil, fmt.Errorf("provider kind %q is not implemented yet", pc.Kind)
}

func volume(pc config.Provider) *runpod.Volume {
	if pc.Volume == "" {
		return nil
	}
	pair, _ := secrets.Get(VolumeSecretName(pc.ID))
	access, secret, _ := strings.Cut(pair, ":")
	return &runpod.Volume{ID: pc.Volume, Datacenter: pc.Region, AccessKey: access, Secret: secret}
}

// WorkflowPaths resolves workflow files, which config names relative to the config folder.
func WorkflowPaths(workflows map[string]string) map[string]string {
	out := make(map[string]string, len(workflows))
	for use, p := range workflows {
		if !filepath.IsAbs(p) {
			p = filepath.Join(paths.ConfigDir(), filepath.FromSlash(p))
		}
		out[use] = p
	}
	return out
}

// Kind describes a provider kind the app can add: how it is set up, field by field.
type Kind struct {
	Kind        string  `json:"kind"`
	Vendor      string  `json:"vendor,omitempty"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Pairing     bool    `json:"pairing"`
	Paid        bool    `json:"paid"`
	KeyURL      string  `json:"key_url,omitempty"`
	Fields      []Field `json:"fields"`
}

type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // text | url | secret | number | workflows
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	// Optional fields may stay empty; Unit labels a number ("USD per second").
	Optional bool   `json:"optional,omitempty"`
	Unit     string `json:"unit,omitempty"`
}

// Kinds lists what the core can add today.
func Kinds() []Kind {
	return []Kind{
		{
			Kind:        string(provider.KindEngine),
			Label:       "Another computer running Forja",
			Description: "A PC with a GPU on your network, or a rented GPU running forja-engine.",
			Pairing:     true,
			Fields: []Field{
				{Key: "url", Label: "Address", Type: "url", Placeholder: "192.168.1.20 or studio.local",
					Help: "Shown on the other computer under Settings → Share this computer's GPU."},
				{Key: "token", Label: "Access token", Type: "secret",
					Help: "Only needed without a pairing code. Stored in the keychain, never in a file."},
			},
		},
		{
			Kind:        string(provider.KindAPI),
			Vendor:      fal.Vendor,
			Label:       "fal.ai",
			Description: "Hosted 3D models (Hunyuan3D 3.1, TRELLIS 2, Tripo, Meshy, Rodin and more) with one API key. Pay per generation; your reference image is sent to fal.ai.",
			Paid:        true,
			KeyURL:      "https://fal.ai/dashboard/keys",
			Fields: []Field{
				{Key: "token", Label: "API key", Type: "secret", Placeholder: "fal key",
					Help: "Create one in your fal.ai dashboard. Stored in the keychain, never in a file."},
			},
		},
		{
			Kind:        string(provider.KindServerless),
			Vendor:      runpod.Vendor,
			Label:       "RunPod serverless",
			Description: "Forja's engine image on a RunPod serverless endpoint: it scales to zero and bills each second of GPU time. Your images and meshes are sent to RunPod.",
			Paid:        true,
			KeyURL:      "https://console.runpod.io/user/settings",
			Fields: []Field{
				{Key: "endpoint_id", Label: "Endpoint ID", Type: "text", Placeholder: "abc123xyz789",
					Help: "On the endpoint's page in the RunPod console. Its workers run the forja-engine RunPod image."},
				{Key: "token", Label: "API key", Type: "secret",
					Help: "A RunPod API key. Stored in the keychain, never in a file."},
				{Key: "price_usd_per_second", Label: "GPU price", Type: "number", Unit: "USD per second", Placeholder: "0.00031",
					Optional: true, Help: "What a worker's GPU costs, from RunPod's pricing page; costs are added up from it."},
				{Key: "volume", Label: "Network volume ID", Type: "text", Optional: true,
					Help: "With the volume's S3 key, files move through the volume by hash instead of inside each job, which RunPod caps at 10 MB."},
				{Key: "region", Label: "Volume datacenter", Type: "text", Placeholder: "EU-RO-1", Optional: true},
				{Key: "volume_key_id", Label: "S3 access key", Type: "text", Placeholder: "user_…", Optional: true},
				{Key: "volume_secret", Label: "S3 secret", Type: "secret", Optional: true,
					Help: "From Settings → S3 API Keys in the RunPod console. Stored in the keychain."},
			},
		},
		{
			Kind:        string(provider.KindEngine),
			Vendor:      runpod.Vendor,
			Label:       "RunPod pod",
			Description: "A rented GPU pod running the forja-engine image, reached at its public address with the token you gave it. Billed by the hour while it runs.",
			Paid:        true,
			Fields: []Field{
				{Key: "url", Label: "Address", Type: "url", Placeholder: "203.0.113.7:40123",
					Help: "The pod's public IP and the TCP port mapped to 7861."},
				{Key: "token", Label: "Access token", Type: "secret",
					Help: "The pod's FORJA_ENGINE_TOKEN. Stored in the keychain, never in a file."},
				{Key: "price_usd_per_hour", Label: "Pod price", Type: "number", Unit: "USD per hour", Placeholder: "0.69", Optional: true},
			},
		},
		{
			Kind:        string(provider.KindServerless),
			Vendor:      Modal,
			Label:       "Modal",
			Description: "Forja's engine on Modal: a small gateway answers at once and wakes a GPU only to run stages, billed per second of GPU time. Your images and meshes are sent to Modal.",
			Paid:        true,
			Fields: []Field{
				{Key: "url", Label: "Gateway address", Type: "url", Placeholder: "https://you--forja-engine-gateway.modal.run",
					Help: "What `modal deploy docker/modal/app.py` prints for the gateway."},
				{Key: "token", Label: "Access token", Type: "secret",
					Help: "The FORJA_ENGINE_TOKEN in the app's Modal secret. Stored in the keychain, never in a file."},
				{Key: "price_usd_per_second", Label: "GPU price", Type: "number", Unit: "USD per second", Placeholder: "0.000542",
					Optional: true, Help: "What the app's GPU costs, from Modal's pricing page."},
			},
		},
		{
			Kind:        string(provider.KindComfyUI),
			Label:       "ComfyUI",
			Description: "Runs stages as your own ComfyUI workflows, wherever ComfyUI runs. Export each with Export (API) and put {{image}}, {{mesh}}, {{seed}} or {{param:name}} where Forja's inputs go.",
			Fields: []Field{
				{Key: "url", Label: "Address", Type: "url", Placeholder: "http://192.168.1.30:8188"},
				{Key: "workflows", Label: "Workflows", Type: "workflows",
					Help: "One API-format workflow per stage it does: background removal, shape or texture. Forja keeps a copy."},
				{Key: "token", Label: "Access token", Type: "secret", Optional: true,
					Help: "Only for a ComfyUI behind a proxy that asks for one, at its https address (plain http only on this computer)."},
			},
		},
	}
}
