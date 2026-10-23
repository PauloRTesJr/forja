// Package providers builds the providers configured in [[providers]], with their secrets.
package providers

import (
	"fmt"

	"forja/internal/config"
	"forja/internal/provider"
	"forja/internal/provider/engine"
	"forja/internal/provider/fal"
	"forja/internal/secrets"
	"forja/internal/store"
)

// SecretName is the keychain entry a provider's token or key is stored under.
func SecretName(id string) string { return "provider-" + id }

// Build makes a provider from its config. A missing secret is not an error: the provider
// then answers 401s, which its health shows.
func Build(pc config.Provider, st *store.Store) (provider.Provider, error) {
	key := ""
	if pc.Secret != "" {
		key, _ = secrets.Get(pc.Secret)
	}
	switch provider.Kind(pc.Kind) {
	case provider.KindEngine:
		p := engine.New(pc.ID, provider.KindEngine, pc.Paid, false, engine.Static(pc.URL, key), st)
		p.Pin(pc.Fingerprint)
		return p, nil
	case provider.KindAPI:
		if pc.Vendor == fal.Vendor {
			return fal.New(pc.ID, key, pc.Models, st), nil
		}
		return nil, fmt.Errorf("model hub %q is not supported", pc.Vendor)
	}
	return nil, fmt.Errorf("provider kind %q is not implemented yet", pc.Kind)
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
	Type        string `json:"type"` // text | url | secret
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
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
	}
}
