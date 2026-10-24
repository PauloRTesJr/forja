package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	"forja/internal/localengine"
	"forja/internal/provider"
	"forja/internal/secrets"
	"forja/internal/store"
)

// fakeEngine fakes a local engine that installs models instantly.
type fakeEngine struct {
	provider.Provider
	mu        sync.Mutex
	installed map[string]bool
	tasks     map[string]engineapi.Task
}

var gpu = "cuda:0"

func (e *fakeEngine) ID() string          { return jobs.LocalProvider }
func (e *fakeEngine) Kind() provider.Kind { return provider.KindLocal }
func (e *fakeEngine) Paid() bool          { return false }
func (e *fakeEngine) Info(context.Context) (*engineapi.Info, error) {
	return &engineapi.Info{Stages: map[string]string{"framing": "1", "mesh.cleanup": "1", "mesh.decimate": "1",
		"texture.match": "1", "bake": "1", "mesh.lods": "1", "export": "1"},
		System: map[string]any{"devices": []any{map[string]any{"id": "cuda:0", "name": "Test GPU"}}}}, nil
}

func (e *fakeEngine) Models(context.Context) ([]engineapi.Model, error) {
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

func (e *fakeEngine) Install(_ context.Context, model string, _ engineapi.InstallRequest) (*engineapi.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.installed[model] = true
	t := engineapi.Task{ID: "t-" + model, Kind: "install", Target: model, Status: "running", Version: 1}
	e.tasks[t.ID] = t
	return &t, nil
}

func (e *fakeEngine) FollowTask(_ context.Context, id string, states chan<- engineapi.Task) error {
	e.mu.Lock()
	t := e.tasks[id]
	e.mu.Unlock()
	t.Status, t.Version = "succeeded", 2
	states <- t
	return nil
}

func (e *fakeEngine) Checks(context.Context, []string) (*engineapi.Checks, error) {
	return &engineapi.Checks{}, nil
}
func (e *fakeEngine) Fix(context.Context, string) (*engineapi.Task, error)    { return nil, nil }
func (e *fakeEngine) Remove(context.Context, string) (*engineapi.Task, error) { return nil, nil }
func (e *fakeEngine) CancelTask(context.Context, string) error                { return nil }

func server(t *testing.T) (*httptest.Server, *fakeEngine, *events.Hub) {
	t.Helper()
	ts, s, eng := newServer(t)
	return ts, eng, s.Hub
}

func newServer(t *testing.T) (*httptest.Server, *Server, *fakeEngine) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "config"))
	st, _ := store.Open(filepath.Join(dir, "store"))
	db, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	eng := &fakeEngine{installed: map[string]bool{}, tasks: map[string]engineapi.Task{}}
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
		Engine:   localengine.New(func() (*config.Loaded, error) { return config.Load("", nil) }, st.Root, dir, log),
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, s, eng
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
	var got SetupView
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
	var models []ModelView
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
	var models []ModelView
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

func TestSettingsSayWhereEachValueComesFromAndSaveToTheUserFile(t *testing.T) {
	ts, _, _ := server(t)
	t.Setenv("FORJA_ENGINE__FIT_MODE", "quality-first")
	var view SettingsView
	call(t, ts, "GET", "/v1/settings", nil, &view)
	by := map[string]SettingView{}
	for _, s := range view.Settings {
		by[s.Key] = s
	}
	if by["engine.fit_mode"].Source != "env" || by["engine.idle_unload_minutes"].Source != "default" {
		t.Fatalf("sources: %+v", by)
	}
	if code := call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": map[string]any{
		"engine.idle_unload_minutes": 5, "models.stubs": true,
	}}, &view); code != 200 {
		t.Fatalf("status %d", code)
	}
	cfg, _ := config.Load("", nil)
	if cfg.Engine.IdleUnloadMinutes != 5 || !cfg.Models.Stubs {
		t.Fatalf("not saved: %+v %+v", cfg.Engine, cfg.Models)
	}
	for _, bad := range []map[string]any{
		{"engine.idle_unload_minutes": 1.5},
		{"routing.policy": "whatever"},
		{"project.export_dir": "../outside"},
		{"project.default_preset": "no-such-preset"},
		{"engine.command": "rm"},
	} {
		if code := call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": bad}, nil); code != 422 {
			t.Errorf("%v: status %d", bad, code)
		}
	}
	if cfg, _ := config.Load("", nil); cfg.Routing.Policy != "prefer-local" {
		t.Fatal("a refused change was kept")
	}
	// null removes the key, back to the default.
	call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": map[string]any{"engine.idle_unload_minutes": nil}}, nil)
	if cfg, _ := config.Load("", nil); cfg.Engine.IdleUnloadMinutes != 15 {
		t.Fatalf("got %d", cfg.Engine.IdleUnloadMinutes)
	}
}

func TestAddingAComputerByCodeStoresItsTokenInTheKeychainOnly(t *testing.T) {
	secrets.InMemory()
	ts, _, _ := server(t)
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/pair":
			_, _ = w.Write([]byte(`{"token":"tok-0123456789","name":"Studio PC"}`))
		case "/v1/info":
			_ = json.NewEncoder(w).Encode(engineapi.Info{APIVersion: engineapi.APIVersion, Version: "test"})
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer remote.Close()
	var added map[string]any
	if code := call(t, ts, "POST", "/v1/providers", map[string]string{"url": remote.URL, "code": "482913"}, &added); code != 201 {
		t.Fatalf("status %d: %v", code, added)
	}
	if added["id"] != "studio-pc" {
		t.Fatalf("added %v", added)
	}
	raw, _ := os.ReadFile(config.UserFile())
	if strings.Contains(string(raw), "tok-0123456789") || !strings.Contains(string(raw), `fingerprint = "`) {
		t.Fatalf("config.toml:\n%s", raw)
	}
	if v, err := secrets.Get("provider-studio-pc"); err != nil || v != "tok-0123456789" {
		t.Fatalf("keychain: %q %v", v, err)
	}
	cfg, _ := config.Load("", nil)
	if strings.Join(cfg.Routing.Priority, ",") != "local,studio-pc" {
		t.Fatalf("priority %v", cfg.Routing.Priority)
	}
	var rows []ProviderRow
	call(t, ts, "GET", "/v1/providers", nil, &rows)
	if len(rows) != 2 || rows[1].Key == nil || rows[1].Key.Hint != "…6789" || !rows[1].Healthy {
		t.Fatalf("rows %+v", rows)
	}
	if code := call(t, ts, "DELETE", "/v1/providers/studio-pc", nil, nil); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if _, err := secrets.Get("provider-studio-pc"); err == nil {
		t.Fatal("the token outlived its provider")
	}
	if cfg, _ := config.Load("", nil); len(cfg.Providers) != 0 || strings.Join(cfg.Routing.Priority, ",") != "local" {
		t.Fatalf("left behind: %+v %v", cfg.Providers, cfg.Routing.Priority)
	}
}
