package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/installs"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/provider"
	"forja/internal/store"
)

// engine fakes a local engine that installs models instantly.
type engine struct {
	provider.Provider
	mu        sync.Mutex
	installed map[string]bool
	tasks     map[string]engineapi.Task
}

var gpu = "cuda:0"

func (e *engine) ID() string          { return jobs.LocalProvider }
func (e *engine) Kind() provider.Kind { return provider.KindLocal }
func (e *engine) Paid() bool          { return false }
func (e *engine) Info(context.Context) (*engineapi.Info, error) {
	return &engineapi.Info{Stages: map[string]string{"framing": "1", "mesh.cleanup": "1", "mesh.decimate": "1",
		"texture.match": "1", "bake": "1", "mesh.lods": "1", "export": "1"},
		System: map[string]any{"devices": []any{map[string]any{"id": "cuda:0", "name": "Test GPU"}}}}, nil
}

func (e *engine) Models(context.Context) ([]engineapi.Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	model := func(id, task string, rank int, fit string, lic engineapi.License) engineapi.Model {
		m := engineapi.Model{ID: id, Name: id, Tasks: []string{task}, Rank: rank, Fit: fit, DiskGB: 1, License: lic,
			Status: "not-installed", RunsOn: &gpu}
		if e.installed[id] {
			m.Status, m.Device = "ready", &gpu
		}
		return m
	}
	mit := engineapi.License{SPDX: "MIT", Commercial: true}
	restricted := engineapi.License{SPDX: "LicenseRef-Test", Commercial: true, Restrictions: []string{"not in the EU"}}
	return []engineapi.Model{
		model("bg", "background-removal", 10, "fits", mit),
		model("shape", "shape", 10, "fits", mit),
		model("shape-big", "shape", 20, "too-big", mit),
		model("paint", "texture", 10, "offload", restricted),
	}, nil
}

func (e *engine) Install(_ context.Context, model string, _ engineapi.InstallRequest) (*engineapi.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.installed[model] = true
	t := engineapi.Task{ID: "t-" + model, Kind: "install", Target: model, Status: "running", Version: 1}
	e.tasks[t.ID] = t
	return &t, nil
}

func (e *engine) FollowTask(_ context.Context, id string, states chan<- engineapi.Task) error {
	e.mu.Lock()
	t := e.tasks[id]
	e.mu.Unlock()
	t.Status, t.Version = "succeeded", 2
	states <- t
	return nil
}

func (e *engine) Checks(context.Context, []string) (*engineapi.Checks, error) {
	return &engineapi.Checks{}, nil
}
func (e *engine) Fix(context.Context, string) (*engineapi.Task, error)    { return nil, nil }
func (e *engine) Remove(context.Context, string) (*engineapi.Task, error) { return nil, nil }
func (e *engine) CancelTask(context.Context, string) error                { return nil }

func server(t *testing.T) (*httptest.Server, *engine, *events.Hub) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "config"))
	st, _ := store.Open(filepath.Join(dir, "store"))
	db, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	eng := &engine{installed: map[string]bool{}, tasks: map[string]engineapi.Task{}}
	reg := provider.NewRegistry()
	reg.Add(eng)
	hub := events.NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cat := catalog.New(reg, db, nil, func(id string) { hub.Publish(EvModelsChanged, id) })
	cfg := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	s := &Server{
		DB: db, Store: st, Hub: hub, Providers: reg, Catalog: cat, Log: log, Config: cfg,
		Runner:   jobs.NewRunner(db, st, hub, reg, cat, cfg, log, "test", 2),
		Installs: installs.New(reg, cat, hub, log),
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, eng, hub
}

func call(t *testing.T, ts *httptest.Server, method, path string, body any, out any) int {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, ts.URL+path, r)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestSetupRecommendsWhatThisMachineCanRun(t *testing.T) {
	ts, _, _ := server(t)
	var got setupView
	if code := call(t, ts, "GET", "/v1/setup", nil, &got); code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Ready || len(got.Install) != 3 || got.Install[1] != "shape" {
		t.Fatalf("got %+v", got)
	}
	for _, st := range got.Stages {
		if st.Status != "install" {
			t.Fatalf("stage %s: %s", st.Use, st.Status)
		}
	}
}

func TestRestrictedLicensesMustBeAcceptedOnce(t *testing.T) {
	ts, eng, hub := server(t)
	updates, stop := hub.Subscribe()
	defer stop()
	var prob map[string]any
	if code := call(t, ts, "POST", "/v1/models/paint/install", map[string]any{}, &prob); code != 409 || prob["license"] == nil {
		t.Fatalf("status %d, body %v", code, prob)
	}
	var entry installs.Entry
	if code := call(t, ts, "POST", "/v1/models/paint/install", map[string]any{"accept_license": true}, &entry); code != 202 {
		t.Fatalf("status %d", code)
	}
	if entry.Provider != jobs.LocalProvider || entry.Target != "paint" {
		t.Fatalf("entry %+v", entry)
	}
	// The tracker relays the engine's states and refreshes the models when the task ends.
	deadline := time.After(5 * time.Second)
	for done := false; !done; {
		select {
		case ev := <-updates:
			done = ev.Type == EvModelsChanged
		case <-deadline:
			t.Fatal("no models.changed after the install")
		}
	}
	if !eng.installed["paint"] {
		t.Fatal("not installed")
	}
	// Accepted once: a repair does not ask again.
	if code := call(t, ts, "POST", "/v1/models/paint/install", map[string]any{}, nil); code != 202 {
		t.Fatalf("second install: status %d", code)
	}
	var models []modelView
	call(t, ts, "GET", "/v1/models", nil, &models)
	for _, m := range models {
		if m.ID == "paint" && (m.AcceptedAt == nil || len(m.Uses) != 1 || m.Uses[0].Use != "texture") {
			t.Fatalf("paint: %+v", m)
		}
	}
}

func TestPinningAModelWritesTheUserConfig(t *testing.T) {
	ts, eng, _ := server(t)
	eng.installed["shape"], eng.installed["shape-big"] = true, true
	if code := call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": "shape"}, nil); code != 204 {
		t.Fatalf("status %d", code)
	}
	cfg, _ := config.Load("", nil)
	if cfg.Models.Stages["shape"] != "shape" {
		t.Fatalf("stages %v", cfg.Models.Stages)
	}
	var models []modelView
	call(t, ts, "GET", "/v1/models?fresh=1", nil, &models)
	for _, m := range models {
		if m.ID == "shape" && (len(m.Uses) != 1 || !m.Uses[0].Pinned) {
			t.Fatalf("shape: %+v", m.Uses)
		}
		if m.ID == "shape-big" && len(m.Uses) != 0 {
			t.Fatalf("shape-big outranks it but is not pinned: %+v", m.Uses)
		}
	}
	call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": ""}, nil)
	if cfg, _ = config.Load("", nil); cfg.Models.Stages["shape"] != "" {
		t.Fatal("unpinning left the key")
	}
}
