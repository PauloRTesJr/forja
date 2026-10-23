// Package catalog remembers what each provider runs: its built-in stage versions and its
// models. Presets are resolved against it, and the answers survive restarts, so the Generate
// screen knows what is installed without starting the local engine.
package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"forja/internal/engineapi"
	"forja/internal/provider"
)

// Entry is one provider's answer.
type Entry struct {
	Stages map[string]string `json:"stages"`
	Models []engineapi.Model `json:"models"`
	System map[string]any    `json:"system,omitempty"`
	At     time.Time         `json:"at"`
	// Live marks answers given in this session. Only those may key the stage cache
	// (docs/decisions.md, decision 11); remembered ones only describe what is installed.
	Live bool `json:"-"`
}

// Model returns the entry's model by id.
func (e *Entry) Model(id string) (engineapi.Model, bool) {
	for _, m := range e.Models {
		if m.ID == id {
			return m, true
		}
	}
	return engineapi.Model{}, false
}

// Store persists entries; the library database implements it.
type Store interface {
	SaveCatalog(ctx context.Context, provider string, entry []byte) error
	Catalogs(ctx context.Context) (map[string][]byte, error)
	ForgetCatalog(ctx context.Context, provider string) error
}

type Catalog struct {
	providers *provider.Registry
	store     Store
	// idle reports a provider that asking would start (the local engine while it is stopped).
	idle func(id string) bool
	// changed is told when a provider's models differ from what was remembered.
	changed func(id string)

	mu   sync.Mutex
	last map[string]*Entry
}

// fresh is how long an answer serves repeated questions without asking again.
const fresh = 5 * time.Second

func New(providers *provider.Registry, store Store, idle func(id string) bool, changed func(id string)) *Catalog {
	c := &Catalog{providers: providers, store: store, idle: idle, changed: changed, last: map[string]*Entry{}}
	if idle == nil {
		c.idle = func(string) bool { return false }
	}
	if changed == nil {
		c.changed = func(string) {}
	}
	if raw, err := store.Catalogs(context.Background()); err == nil {
		for id, b := range raw {
			var e Entry
			if json.Unmarshal(b, &e) == nil {
				c.last[id] = &e
			}
		}
	}
	return c
}

// Ask asks the provider now and remembers the answer.
func (c *Catalog) Ask(ctx context.Context, id string) (*Entry, error) {
	p, ok := c.providers.Get(id)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", id)
	}
	info, err := p.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("provider %s did not say what it runs: %w", id, err)
	}
	models, err := p.Models(ctx)
	if err != nil {
		return nil, fmt.Errorf("provider %s did not list its models: %w", id, err)
	}
	e := &Entry{Stages: info.Stages, Models: models, System: info.System, At: time.Now(), Live: true}
	c.mu.Lock()
	prev := c.last[id]
	c.last[id] = e
	c.mu.Unlock()
	if raw, err := json.Marshal(e); err == nil {
		_ = c.store.SaveCatalog(context.WithoutCancel(ctx), id, raw)
	}
	if prev == nil || !reflect.DeepEqual(prev.Models, e.Models) {
		c.changed(id)
	}
	return e, nil
}

// Get answers from memory when the provider is idle or was asked a moment ago, and asks it
// otherwise. A provider that cannot answer is described by its last answer, if any.
func (c *Catalog) Get(ctx context.Context, id string) (*Entry, error) {
	c.mu.Lock()
	last := c.last[id]
	c.mu.Unlock()
	if last != nil && (c.idle(id) || (last.Live && time.Since(last.At) < fresh)) {
		return last, nil
	}
	e, err := c.Ask(ctx, id)
	if err != nil && last != nil {
		return last, nil
	}
	return e, err
}

// Live returns this session's last answer, for keying stages while the provider is down.
func (c *Catalog) Live(id string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.last[id]
	return e, ok && e.Live
}

// Last returns the remembered answer, from this session or an earlier one.
func (c *Catalog) Last(id string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.last[id]
	return e, ok
}

// Forget drops a provider that was removed.
func (c *Catalog) Forget(id string) {
	c.mu.Lock()
	delete(c.last, id)
	c.mu.Unlock()
	_ = c.store.ForgetCatalog(context.Background(), id)
	c.changed(id)
}
