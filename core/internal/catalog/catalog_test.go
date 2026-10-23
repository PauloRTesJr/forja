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
	asked  int
	down   bool
	models []engineapi.Model
}

func (f *fake) ID() string { return "local" }
func (f *fake) Info(context.Context) (*engineapi.Info, error) {
	f.asked++
	if f.down {
		return nil, errors.New("connection refused")
	}
	return &engineapi.Info{Stages: map[string]string{"bake": "1"}}, nil
}
func (f *fake) Models(context.Context) ([]engineapi.Model, error) { return f.models, nil }

func TestAnswersSurviveARestartButDoNotKeyStages(t *testing.T) {
	store := memStore{}
	f := &fake{models: []engineapi.Model{{ID: "triposg"}}}
	reg := provider.NewRegistry()
	reg.Add(f)
	changes := 0
	c := New(reg, store, nil, func(string) { changes++ })
	if _, err := c.Ask(context.Background(), "local"); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Live("local"); !ok || changes != 1 {
		t.Fatalf("live = %v, changes = %d", ok, changes)
	}

	idle := true
	restarted := New(reg, store, func(string) bool { return idle }, nil)
	if _, ok := restarted.Live("local"); ok {
		t.Fatal("an answer from an earlier session counts as live")
	}
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
