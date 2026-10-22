// Package provider defines the one interface every inference target implements.
//
// The job runner depends only on this interface: a stage moves from the local GPU to a LAN
// box, a rented GPU or a paid API by changing configuration, never code.
package provider

import (
	"context"

	"forja/internal/engineapi"
)

type Kind string

const (
	KindLocal      Kind = "local"
	KindEngine     Kind = "engine"
	KindServerless Kind = "serverless"
	KindAPI        Kind = "api"
	KindComfyUI    Kind = "comfyui"
)

type Provider interface {
	ID() string
	Kind() Kind
	// Paid providers are never chosen as an automatic fallback unless allowed in config.
	Paid() bool
	Health(ctx context.Context) error
	Info(ctx context.Context) (*engineapi.Info, error)
	Models(ctx context.Context) ([]engineapi.Model, error)
	// SharesStore is true when the provider reads the core's object store directly,
	// so artifacts move by path and never over the network.
	SharesStore() bool
	HasArtifact(ctx context.Context, ref string) (bool, error)
	Upload(ctx context.Context, ref string) error
	Download(ctx context.Context, ref string) error
	// Run executes a stage graph, forwarding engine events until the run ends.
	Run(ctx context.Context, req engineapi.RunRequest, events chan<- engineapi.Event) (*engineapi.Run, error)
	Cancel(ctx context.Context, runID string) error
}

// Registry holds configured providers by id.
type Registry struct {
	byID  map[string]Provider
	order []string
}

func NewRegistry() *Registry { return &Registry{byID: map[string]Provider{}} }

func (r *Registry) Add(p Provider) {
	if _, ok := r.byID[p.ID()]; !ok {
		r.order = append(r.order, p.ID())
	}
	r.byID[p.ID()] = p
}

func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.byID[id]
	return p, ok
}

func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}
