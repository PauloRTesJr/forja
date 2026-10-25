// Package provider defines the one interface every inference target implements.
//
// The job runner depends only on this interface: a stage moves from the local GPU to a LAN
// box, a rented GPU or a paid API by changing configuration, never code.
package provider

import (
	"context"
	"io"
	"slices"
	"sync"

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

// Installer is implemented by providers that manage models on their own machine (engines).
// Callers type-assert for it; a managed API has nothing to install.
type Installer interface {
	// Checks says what the models need on the provider's machine, and what it needs to run
	// built-in stages of the given types itself (Blender for quad remesh).
	Checks(ctx context.Context, models, stages []string) (*engineapi.Checks, error)
	Fix(ctx context.Context, check string) (*engineapi.Task, error)
	Install(ctx context.Context, model string, req engineapi.InstallRequest) (*engineapi.Task, error)
	Remove(ctx context.Context, model string) (*engineapi.Task, error)
	CancelTask(ctx context.Context, id string) error
	// FollowTask sends the task's state each time it changes, until it ends or ctx does.
	FollowTask(ctx context.Context, id string, states chan<- engineapi.Task) error
}

// Registry holds configured providers by id. Providers come and go at runtime, as the user
// adds and removes them.
type Registry struct {
	mu    sync.RWMutex
	byID  map[string]Provider
	order []string
}

func NewRegistry() *Registry { return &Registry{byID: map[string]Provider{}} }

func (r *Registry) Add(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[p.ID()]; !ok {
		r.order = append(r.order, p.ID())
	}
	r.byID[p.ID()] = p
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
	r.order = slices.DeleteFunc(r.order, func(x string) bool { return x == id })
}

func (r *Registry) Get(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	return p, ok
}

func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.byID[id])
	}
	return out
}

// Hub is implemented by providers with a catalog to browse and add models from (fal.ai).
type Hub interface {
	Search(ctx context.Context, query, cursor string) (*engineapi.CatalogPage, error)
	// Check describes one model before it is added; Problem is set when Forja cannot call it.
	Check(ctx context.Context, model string) (engineapi.CatalogModel, error)
	// Thumbnail fetches a catalog picture with its media type; it refuses URLs off the hub's
	// own image hosts.
	Thumbnail(ctx context.Context, url string) (io.ReadCloser, string, error)
}

// Sleeper is implemented by providers that bill for waking up (serverless endpoints). While one
// is asleep, the catalog describes it from its last answer instead of asking, which would
// start a worker; keying a job still asks.
type Sleeper interface {
	Asleep() bool
}
