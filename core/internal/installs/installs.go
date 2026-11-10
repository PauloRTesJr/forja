// Package installs follows model installs, removals and fixes running on providers and
// relays their progress to clients as core events, so the UI never polls.
package installs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"forja/internal/catalog"
	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/provider"
)

// EvUpdated carries an Entry each time a task's state changes.
const EvUpdated = "install.updated"

// ErrNoInstaller is a provider that has nothing to install (a managed API).
var ErrNoInstaller = errors.New("this provider does not manage models")

// ErrUnknownProvider is an id no provider has.
var ErrUnknownProvider = errors.New("no such provider")

// Entry is a task and the provider it runs on.
type Entry struct {
	Provider string `json:"provider"`
	engineapi.Task
}

type Tracker struct {
	providers *provider.Registry
	catalog   *catalog.Catalog
	hub       *events.Hub
	log       *slog.Logger
	// OnDone, when set, is called after a task of a provider ended and its final state was published.
	OnDone func(providerID string)

	mu      sync.Mutex
	entries map[string]*Entry // by provider + "/" + task id
}

const keep = 50

func New(providers *provider.Registry, cat *catalog.Catalog, hub *events.Hub, log *slog.Logger) *Tracker {
	return &Tracker{providers: providers, catalog: cat, hub: hub, log: log, entries: map[string]*Entry{}}
}

// Installer returns a provider's installer.
func (t *Tracker) Installer(id string) (provider.Installer, error) {
	p, ok := t.providers.Get(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownProvider, id)
	}
	inst, ok := p.(provider.Installer)
	if !ok {
		return nil, ErrNoInstaller
	}
	return inst, nil
}

// Start begins a task on a provider and follows it in the background. A provider that joins
// the request to a task already running returns that task, which is followed once.
func (t *Tracker) Start(providerID string, begin func(provider.Installer) (*engineapi.Task, error)) (Entry, error) {
	inst, err := t.Installer(providerID)
	if err != nil {
		return Entry{}, err
	}
	task, err := begin(inst)
	if err != nil {
		return Entry{}, err
	}
	t.mu.Lock()
	_, known := t.entries[key(providerID, task.ID)]
	t.mu.Unlock()
	e := t.update(providerID, *task)
	if !known {
		go t.follow(inst, providerID, task.ID)
	}
	return e, nil
}

func key(providerID, taskID string) string { return providerID + "/" + taskID }

// update records a task state, publishes it and returns what is stored.
func (t *Tracker) update(providerID string, task engineapi.Task) Entry {
	t.mu.Lock()
	k := key(providerID, task.ID)
	e, ok := t.entries[k]
	if !ok {
		e = &Entry{Provider: providerID}
		t.entries[k] = e
		t.prune()
	}
	if task.Version >= e.Version {
		e.Task = task
	}
	snap := *e
	t.mu.Unlock()
	t.hub.Publish(EvUpdated, snap)
	return snap
}

func (t *Tracker) follow(inst provider.Installer, providerID, taskID string) {
	t.mu.Lock()
	if e := t.entries[key(providerID, taskID)]; e != nil && e.Task.Done() {
		t.mu.Unlock()
		t.finished(providerID)
		return
	}
	t.mu.Unlock()
	states := make(chan engineapi.Task, 16)
	errc := make(chan error, 1)
	go func() {
		errc <- inst.FollowTask(context.Background(), taskID, states)
		close(states)
	}()
	refreshed := false
	for s := range states {
		// A client that sees the task end may submit a job at once, so the catalog must
		// already list the model as ready when the final state is published.
		if s.Done() && !refreshed {
			t.finished(providerID)
			refreshed = true
		}
		t.update(providerID, s)
	}
	if err := <-errc; err != nil {
		t.log.Warn("lost an install's progress", "provider", providerID, "task", taskID, "err", err)
		t.mu.Lock()
		e := t.entries[key(providerID, taskID)]
		var snap engineapi.Task
		if e != nil && !e.Task.Done() {
			snap = e.Task
		}
		t.mu.Unlock()
		if snap.ID != "" {
			msg := "lost contact with the provider: " + err.Error()
			snap.Status, snap.Error, snap.Version = "failed", &msg, snap.Version+1
			t.update(providerID, snap)
		}
	}
	if !refreshed {
		t.finished(providerID)
	}
	if t.OnDone != nil {
		t.OnDone(providerID)
	}
}

// finished refreshes what the provider runs, which publishes models.changed when it differs.
func (t *Tracker) finished(providerID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := t.catalog.Ask(ctx, providerID); err != nil {
		t.log.Warn("refreshing models after an install", "provider", providerID, "err", err)
	}
}

// Busy reports whether a task of the provider is still running.
func (t *Tracker) Busy(providerID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range t.entries {
		if e.Provider == providerID && !e.Task.Done() {
			return true
		}
	}
	return false
}

// Cancel asks the provider to stop a task.
func (t *Tracker) Cancel(ctx context.Context, providerID, taskID string) error {
	inst, err := t.Installer(providerID)
	if err != nil {
		return err
	}
	return inst.CancelTask(ctx, taskID)
}

// List returns tracked tasks, newest first.
func (t *Tracker) List() []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Entry, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (t *Tracker) prune() {
	if len(t.entries) <= keep {
		return
	}
	var done []*Entry
	for _, e := range t.entries {
		if e.Task.Done() {
			done = append(done, e)
		}
	}
	sort.Slice(done, func(i, j int) bool { return done[i].CreatedAt < done[j].CreatedAt })
	for _, e := range done[:max(0, len(t.entries)-keep)] {
		delete(t.entries, key(e.Provider, e.ID))
	}
}
