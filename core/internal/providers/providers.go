// Package providers builds the providers configured in [[providers]], with their secrets.
package providers

import (
	"fmt"

	"forja/internal/config"
	"forja/internal/provider"
	"forja/internal/provider/engine"
	"forja/internal/secrets"
	"forja/internal/store"
)

// SecretName is the keychain entry a provider's token or key is stored under.
func SecretName(id string) string { return "provider-" + id }

// Build makes a provider from its config. A missing secret is not an error: the provider
// then answers 401s, which its health shows.
func Build(pc config.Provider, st *store.Store) (provider.Provider, error) {
	switch provider.Kind(pc.Kind) {
	case provider.KindEngine:
		token := ""
		if pc.Secret != "" {
			token, _ = secrets.Get(pc.Secret)
		}
		p := engine.New(pc.ID, provider.KindEngine, pc.Paid, false, engine.Static(pc.URL, token), st)
		p.Pin(pc.Fingerprint)
		return p, nil
	}
	return nil, fmt.Errorf("provider kind %q is not implemented yet", pc.Kind)
}

// Kind describes a provider kind the app can add: how it is set up, field by field.
type Kind struct {
	Kind        string  `json:"kind"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Pairing     bool    `json:"pairing"`
	Fields      []Field `json:"fields"`
}

type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"` // text | url | secret
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
}

// Kinds lists what the core can actually add today; cloud GPUs and 3D APIs join with their
// providers (M4).
func Kinds() []Kind {
	return []Kind{{
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
	}}
}
