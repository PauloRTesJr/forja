package catalog

import (
	"context"
	"errors"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/provider"
)

type memStore map[string][]byte

func (m memStore) SaveCatalog(_ context.Context, id string, e []byte) error { m[id] = e; return nil }
func (m memStore) Catalogs(context.Context) (map[string][]byte, error)      { return m, nil }
func (m memStore) ForgetCatalog(_ context.Context, id string) error         { delete(m, id); return nil }

// fake answers Info and Models, counting how often it is asked.
type fake struct {
	provider.Provider
	asked   int
	down    bool
	models  []engineapi.Model
	blender bool
}

func (f *fake) ID() string { return "local" }
func (f *fake) Info(context.Context) (*engineapi.Info, error) {
	f.asked++
	if f.down {
		return nil, errors.New("connection refused")
	}
	if f.blender {
		return &engineapi.Info{Stages: map[string]string{"bake": "1", "mesh.remesh": "1+blender-5.2.1"}}, nil
	}
	return &engineapi.Info{Stages: map[string]string{"bake": "1"},
		Unavailable: map[string]string{"mesh.remesh": "Blender was not found"}}, nil
}

func TestABuiltInStageThatBecomesRunnableIsAChange(t *testing.T) {
	f := &fake{models: []engineapi.Model{{ID: "triposg"}}}
	reg := provider.NewRegistry()
	reg.Add(f)
	changes := 0
	c := New(reg, memStore{}, nil, func(string) { changes++ })
	_, _ = c.Ask(context.Background(), "local")
	_, _ = c.Ask(context.Background(), "local")
	if changes != 1 {
		t.Fatalf("the same answer twice made %d changes", changes)
	}
	f.blender = true // Blender was installed: the models are the same, the stages are not.
	_, _ = c.Ask(context.Background(), "local")
	if changes != 2 {
		t.Fatalf("installing Blender made %d changes in all", changes)
	}
}
func (f *fake) Models(context.Context) ([]engineapi.Model, error) { return f.models, nil }

func TestAnswersSurviveARestart(t *testing.T) {
	store := memStore{}
	f := &fake{models: []engineapi.Model{{ID: "triposg"}}}
	reg := provider.NewRegistry()
	reg.Add(f)
	changes := 0
	c := New(reg, store, nil, func(string) { changes++ })
	if _, err := c.Ask(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("changes = %d", changes)
	}

	idle := true
	restarted := New(reg, store, func(string) bool { return idle }, nil)
	e, err := restarted.Get(context.Background(), "local")
	if err != nil || len(e.Models) != 1 || f.asked != 1 {
		t.Fatalf("idle provider: models %v, asked %d, err %v", e, f.asked, err)
	}
	idle = false
	f.down = true
	if e, err := restarted.Get(context.Background(), "local"); err != nil || e.Models[0].ID != "triposg" {
		t.Fatalf("a provider that cannot answer is described by its last answer: %v %v", e, err)
	}
	if f.asked != 2 {
		t.Fatalf("a running provider is asked: asked %d", f.asked)
	}
}

// Resolving the presets asks about each provider once per stage. A provider that could not
// answer was asked again every time: a computer that is switched off cost its connection
// timeout forty times over, and an engine that could not start was started as often.
func TestAProviderThatCannotAnswerIsNotAskedAgainAtOnce(t *testing.T) {
	f := &fake{down: true}
	reg := provider.NewRegistry()
	reg.Add(f)
	c := New(reg, memStore{}, nil, nil)
	for range 40 {
		if _, err := c.Get(context.Background(), "local"); err == nil {
			t.Fatal("a provider that is down answered")
		}
	}
	if f.asked != 1 {
		t.Fatalf("a provider that is down was asked %d times in a row, want once", f.asked)
	}
	// Asking outright (the Models screen's refresh, an engine that just came up) still asks,
	// and an answer clears the failure.
	f.down = false
	if _, err := c.Ask(context.Background(), "local"); err != nil || f.asked != 2 {
		t.Fatalf("asked %d, err %v", f.asked, err)
	}
	if e, err := c.Get(context.Background(), "local"); err != nil || e == nil {
		t.Fatalf("after it answered: %v %v", e, err)
	}
}

// A request the app dropped says nothing about the provider.
func TestACallerGivingUpIsNotTheProvidersFailure(t *testing.T) {
	f := &fake{down: true}
	reg := provider.NewRegistry()
	reg.Add(f)
	c := New(reg, memStore{}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = c.Get(ctx, "local")
	f.down = false
	if _, err := c.Get(context.Background(), "local"); err != nil {
		t.Fatalf("the next caller was answered with the dropped request's failure: %v", err)
	}
}
