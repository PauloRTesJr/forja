package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	"forja/internal/providers"
	"forja/internal/secrets"
	"forja/internal/store"
	"forja/internal/sweep"
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
		Unavailable: map[string]string{"mesh.remesh": "Blender was not found", "mesh.uv.smart": "Blender was not found"},
		System:      map[string]any{"devices": []any{map[string]any{"id": "cuda:0", "name": "Test GPU", "vram_bytes": float64(12 << 30)}}}}, nil
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
		model("rig-small", "rig", 5, "fits", mit),
		model("rig-big", "rig", 10, "too-big", mit),
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

func (e *fakeEngine) Checks(context.Context, []string, []string) (*engineapi.Checks, error) {
	return &engineapi.Checks{}, nil
}
func (e *fakeEngine) Fix(context.Context, string) (*engineapi.Task, error) { return nil, nil }
func (e *fakeEngine) Remove(_ context.Context, model string) (*engineapi.Task, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.installed, model)
	t := engineapi.Task{ID: "rm-" + model, Kind: "remove", Target: model, Status: "running", Version: 1}
	e.tasks[t.ID] = t
	return &t, nil
}
func (e *fakeEngine) CancelTask(context.Context, string) error { return nil }

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
	runner := jobs.NewRunner(db, st, hub, reg, cat, cfg, log, "test", 2)
	s := &Server{
		DB: db, Store: st, Hub: hub, Providers: reg, Catalog: cat, Log: log, Config: cfg, Runner: runner,
		Sweeper:  sweep.New(db, st, runner, log),
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
	call(t, ts, "GET", "/v1/providers?wait=1", nil, &rows)
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

func TestABlockedOptionalStageSaysWhatInstallsIt(t *testing.T) {
	ts, _, _ := server(t)
	var list []PresetView
	if code := call(t, ts, "GET", "/v1/presets?fresh=1", nil, &list); code != 200 {
		t.Fatalf("presets %d", code)
	}
	var hero, env *PresetView
	for i := range list {
		switch list[i].ID {
		case "hero-character":
			hero = &list[i]
		case "environment-piece":
			env = &list[i]
		}
	}
	// The rig model this computer can run, not the better-ranked one that needs more VRAM.
	if got := hero.Install["rig"]; got.Model != "rig-small" || got.Use != "rig" || hero.Unavailable["rig"] == "" {
		t.Fatalf("rig: %+v, %q", got, hero.Unavailable["rig"])
	}
	// No model makes a Blender stage run: the program does, which the checks name.
	if got := env.Install["remesh"]; !got.Software || got.Model != "" || got.Use != "mesh.remesh" {
		t.Fatalf("remesh: %+v", got)
	}
	if _, ok := env.Install["match"]; ok {
		t.Fatal("a stage that can run offers an install")
	}
}

// A stage type pinned to a model nobody has refused every preset, and the pin route took any
// string; removing the pinned model left the pin behind.
func TestAPinMustNameAModelAndGoesWhenItsModelIsRemoved(t *testing.T) {
	ts, eng, hub := server(t)
	eng.installed["shape"], eng.installed["bg"] = true, true
	for model, want := range map[string]string{
		"shpae": "no provider has a model shpae",
		"paint": "paint is not a shape model (it does texture)",
	} {
		var prob struct{ Detail string }
		if code := call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": model}, &prob); code != 422 ||
			!strings.Contains(prob.Detail, want) {
			t.Fatalf("pinning %s: %d %q", model, code, prob.Detail)
		}
	}
	if cfg, _ := config.Load("", nil); len(cfg.Models.Stages) != 0 {
		t.Fatalf("a refused pin was written: %v", cfg.Models.Stages)
	}
	// A model that is listed but not installed may be pinned; the presets then say which model
	// to install, where, instead of a generic "no shape model".
	if code := call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": "shape-big"}, nil); code != 204 {
		t.Fatalf("pinning a model that is not installed yet: %d", code)
	}
	var list []PresetView
	call(t, ts, "GET", "/v1/presets", nil, &list)
	for _, p := range list {
		if p.ID != "draft" {
			continue
		}
		var shape jobs.Pick
		for _, pick := range p.Plan {
			if pick.Use == "shape" {
				shape = pick
			}
		}
		if p.Ready || !shape.Missing || !shape.Pinned || shape.Model != "shape-big" || shape.InstallOn != jobs.LocalProvider ||
			shape.ModelName == "" {
			t.Fatalf("draft with its shape pinned to a model that is not installed: ready %v, %+v", p.Ready, shape)
		}
		if want := "Shape is set to " + shape.ModelName + ", which is not installed. Install it or switch the stage back to Automatic"; !strings.HasPrefix(p.Error, want) {
			t.Fatalf("the preset says %q, want it to start with %q", p.Error, want)
		}
	}
	var prob struct{ Detail string }
	if code := call(t, ts, "POST", "/v1/jobs", map[string]any{"input": "blake3:" + strings.Repeat("0", 64), "preset": "draft"}, &prob); code != 422 {
		t.Fatalf("a job with a pinned model that is not installed: %d", code)
	}

	// Pinned and installed; then removed: the pin goes with it, and the table it was in.
	eng.mu.Lock()
	eng.installed["shape-big"] = true
	eng.mu.Unlock()
	call(t, ts, "GET", "/v1/models?fresh=1", nil, nil)
	evs, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	if code := call(t, ts, "DELETE", "/v1/models/shape-big", nil, nil); code != 202 {
		t.Fatalf("remove: %d", code)
	}
	cfg, _ := config.Load("", nil)
	if cfg.Models.Stages["shape"] != "" {
		t.Fatalf("the pin outlived its model: %v", cfg.Models.Stages)
	}
	if raw, _ := os.ReadFile(config.UserFile()); strings.Contains(string(raw), "[models.stages]") {
		t.Fatalf("an empty table is left in config.toml:\n%s", raw)
	}
	heard := map[string]bool{}
	for len(evs) > 0 {
		heard[(<-evs).Type] = true
	}
	if !heard[EvSettingsChanged] || !heard[EvModelsChanged] {
		t.Fatalf("events %v", heard)
	}
	// The presets run again, with the model that is left.
	call(t, ts, "GET", "/v1/presets?fresh=1", nil, &list)
	for _, p := range list {
		if p.ID == "draft" && !p.Ready {
			t.Fatalf("draft is not ready after the pin went: %+v", p.Plan)
		}
	}
}

// 1e20 was written to config.toml and read back as a negative number; a reserve larger than
// the card, or minutes below zero, were saved as typed.
func TestNumberSettingsHaveRangesAndRefuseNonsenseInWords(t *testing.T) {
	ts, _, _ := server(t)
	var view SettingsView
	call(t, ts, "GET", "/v1/settings", nil, &view)
	by := map[string]SettingView{}
	for _, s := range view.Settings {
		by[s.Key] = s
	}
	if v := by["engine.idle_unload_minutes"]; v.Min == nil || *v.Min != 0 || v.Max == nil || *v.Max < 60 {
		t.Fatalf("idle minutes: %+v", v)
	}
	if v := by["engine.fit_mode"]; v.Min != nil || v.Max != nil {
		t.Fatalf("a text setting has no range: %+v", v)
	}
	// The detected card bounds the reserve once the local engine has been asked.
	call(t, ts, "GET", "/v1/models?fresh=1", nil, nil)
	for _, bad := range []struct {
		set  map[string]any
		want string
	}{
		{map[string]any{"engine.vram_reserve_gb": 1e20}, "VRAM reserve must be from 0 GB to"},
		{map[string]any{"engine.vram_reserve_gb": -1}, "VRAM reserve must be from 0 GB to"},
		{map[string]any{"engine.vram_reserve_gb": 13}, "VRAM reserve must be from 0 GB to 12 GB (the size of your graphics card)."},
		{map[string]any{"engine.vram_reserve_gb": "lots"}, "VRAM reserve needs a number from 0 GB to"},
		{map[string]any{"engine.idle_unload_minutes": -5}, "Unload idle models after must be from 0 minutes to 10080 minutes; 0 means never."},
		{map[string]any{"engine.idle_unload_minutes": 2.5}, "Unload idle models after needs a whole number"},
		{map[string]any{"engine.idle_unload_minutes": 1e20}, "Unload idle models after must be from 0 minutes"},
		{map[string]any{"routing.budget_usd.per_day": -1}, "The daily spending limit must be from $0 to $1000000; 0 means no limit."},
		{map[string]any{"routing.budget_usd.per_job": 1e300}, "The spending limit per generation must be"},
	} {
		var prob struct{ Detail string }
		if code := call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": bad.set}, &prob); code != 422 || !strings.Contains(prob.Detail, bad.want) {
			t.Errorf("%v: %d %q, want %q", bad.set, code, prob.Detail, bad.want)
		}
	}
	if raw, _ := os.ReadFile(config.UserFile()); strings.Contains(string(raw), "e+") {
		t.Fatalf("a huge number reached config.toml:\n%s", raw)
	}
	// 0 minutes means never unload, and the card's size is the most a reserve can be.
	for _, ok := range []map[string]any{{"engine.idle_unload_minutes": 0}, {"engine.vram_reserve_gb": 0.5}, {"routing.budget_usd.per_job": 0}} {
		if code := call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": ok}, nil); code != 200 {
			t.Errorf("%v: %d", ok, code)
		}
	}
	// A value typed into config.toml by hand is held to the same range.
	os.WriteFile(config.UserFile(), []byte("[engine]\nidle_unload_minutes = -3\n"), 0o644)
	if _, err := config.Load("", nil); err == nil || !strings.Contains(err.Error(), "idle_unload_minutes") {
		t.Fatalf("a hand-written negative: %v", err)
	}
}

// The app kept showing a stopped or failed engine as running until asked fresh, and a change
// to an engine setting said nothing of whether it had been applied.
func TestInfoTellsTheEnginesStateAndASettingSaysWhatBecameOfIt(t *testing.T) {
	ts, s, _ := newServer(t)
	type engineView struct {
		Running        bool   `json:"running"`
		State          string `json:"state"`
		Error          string `json:"error"`
		ErrorDetail    string `json:"error_detail"`
		RestartPending bool   `json:"restart_pending"`
	}
	read := func() engineView {
		var info struct{ Engine engineView }
		call(t, ts, "GET", "/v1/info", nil, &info)
		return info.Engine
	}
	if e := read(); e.State != "stopped" || e.Running || e.Error != "" {
		t.Fatalf("at first: %+v", e)
	}
	var view SettingsView
	if code := call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": map[string]any{"engine.vram_reserve_gb": 2}}, &view); code != 200 || view.Engine != "saved" {
		t.Fatalf("no engine runs: %d, engine %q", code, view.Engine)
	}
	// Nothing about the engine in a setting it does not read.
	view = SettingsView{}
	call(t, ts, "PATCH", "/v1/settings", map[string]any{"set": map[string]any{"models.stubs": true}}, &view)
	if view.Engine != "" {
		t.Fatalf("models.stubs: engine %q", view.Engine)
	}

	if err := os.MkdirAll(filepath.Dir(config.UserFile()), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(config.UserFile(), []byte("[engine]\ncommand = [\"/no/such/forja-engine\"]\n"), 0o644)
	if _, _, err := s.Engine.Endpoint(context.Background()); err == nil {
		t.Fatal("an engine that does not exist started")
	}
	e := read()
	if e.State != "failed" || e.Running || e.Error == "" || !strings.Contains(e.ErrorDetail, "/no/such/forja-engine") || strings.Contains(e.Error, "/no/such") {
		t.Fatalf("after a failed start: %+v", e)
	}
}

// slowComputer is a provider whose reachability check ends when the test says so.
type slowComputer struct {
	*fakeEngine
	release chan error
}

func (c *slowComputer) ID() string          { return "studio" }
func (c *slowComputer) Kind() provider.Kind { return provider.KindEngine }
func (c *slowComputer) Health(ctx context.Context) error {
	select {
	case err := <-c.release:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// One computer that is switched off delayed the whole list by its timeout on every load.
func TestTheProviderListAnswersAtOnceAndLearnsReachabilityInTheBackground(t *testing.T) {
	ts, s, eng := newServer(t)
	slow := &slowComputer{fakeEngine: eng, release: make(chan error, 1)}
	s.Providers.Add(slow)
	evs, stop := s.Hub.Subscribe()
	defer stop()
	row := func(path string) ProviderRow {
		var rows []ProviderRow
		call(t, ts, "GET", path, nil, &rows)
		for _, r := range rows {
			if r.ID == "studio" {
				return r
			}
		}
		t.Fatal("no studio row")
		return ProviderRow{}
	}
	start := time.Now()
	if r := row("/v1/providers"); !r.Checking || r.Healthy || r.Error != "" {
		t.Fatalf("first load: %+v", r)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("the list waited %v for a computer", took)
	}
	// Asking again while the check runs starts no second one.
	if r := row("/v1/providers"); !r.Checking {
		t.Fatalf("second load: %+v", r)
	}
	slow.release <- &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 7861}, Err: errors.New("connectex: refused")}
	for heard := false; !heard; {
		select {
		case ev := <-evs:
			heard = ev.Type == EvProvidersChanged
		case <-time.After(5 * time.Second):
			t.Fatal("nobody was told the check ended")
		}
	}
	r := row("/v1/providers")
	if r.Checking || r.Healthy || !strings.Contains(r.Error, "Studio") && !strings.Contains(r.Error, "studio") ||
		!strings.Contains(r.Error, "nothing answers at 192.0.2.10:7861") || strings.Contains(r.Error, "connectex") ||
		!strings.Contains(r.ErrorDetail, "connectex") {
		t.Fatalf("after the check: %+v", r)
	}
	// What was learned stands: no new check on the next load, and the answer is the same.
	if again := row("/v1/providers"); again.Checking || again.Error != r.Error {
		t.Fatalf("third load: %+v", again)
	}
}

// Cancel in "Add a computer" left the attempt running, and a late success added the computer
// anyway.
func TestACancelledAddingOfAComputerAddsNothing(t *testing.T) {
	secrets.InMemory()
	_, s, _ := newServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/pair":
			_, _ = w.Write([]byte(`{"token":"tok-0123456789","name":"Studio PC"}`))
		case "/v1/info":
			cancel() // the person pressed Cancel while the computer answered
			_ = json.NewEncoder(w).Encode(engineapi.Info{APIVersion: engineapi.APIVersion, Version: "test"})
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer remote.Close()
	body, _ := json.Marshal(map[string]string{"url": remote.URL, "code": "482913"})
	req := httptest.NewRequest("POST", "/v1/providers", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == 201 {
		t.Fatalf("a cancelled add succeeded: %s", rec.Body)
	}
	if _, ok := s.Providers.Get("studio-pc"); ok {
		t.Fatal("the computer was added after Cancel")
	}
	if cfg, _ := config.Load("", nil); len(cfg.Providers) != 0 {
		t.Fatalf("config.toml has %+v", cfg.Providers)
	}
	if _, err := secrets.Get("provider-studio-pc"); err == nil {
		t.Fatal("its token was stored")
	}

	// A computer that does not answer is described in a sentence, not as a Go error.
	var prob struct{ Detail string }
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	if code := call(t, ts, "POST", "/v1/providers", map[string]string{"url": "https://127.0.0.1:1", "code": "482913"}, &prob); code != 422 ||
		!strings.Contains(prob.Detail, "Could not reach https://127.0.0.1:1") || strings.Contains(prob.Detail, "dial tcp") {
		t.Fatalf("%d %q", code, prob.Detail)
	}
}

// A locked setting named an environment variable that does not exist, and a key supplied by
// the environment was said to be in the keychain.
func TestTheRealSourceOfASettingAndOfAKeyIsNamed(t *testing.T) {
	secrets.InMemory()
	ts, s, _ := newServer(t)
	t.Setenv("FORJA_ENGINE__FIT_MODE", "quality-first")
	t.Setenv("FORJA_SECRET_PROVIDER_STUDIO_PC", "tok-0123456789")
	os.MkdirAll(filepath.Dir(config.UserFile()), 0o755)
	os.WriteFile(config.UserFile(), []byte("[engine]\nidle_unload_minutes = 3\n\n[[providers]]\nid = \"studio-pc\"\nkind = \"engine\"\nurl = \"https://192.0.2.10:7861\"\nsecret = \"provider-studio-pc\"\n"), 0o644)
	var view SettingsView
	call(t, ts, "GET", "/v1/settings", nil, &view)
	by := map[string]SettingView{}
	for _, s := range view.Settings {
		by[s.Key] = s
	}
	if d := by["engine.fit_mode"].SourceDetail; d != "FORJA_ENGINE__FIT_MODE" {
		t.Fatalf("env setting: %q", d)
	}
	if d := by["engine.idle_unload_minutes"].SourceDetail; d != config.UserFile() {
		t.Fatalf("file setting: %q", d)
	}
	if d := by["models.stubs"].SourceDetail; d != "" {
		t.Fatalf("a default has no source: %q", d)
	}
	cfg, _ := config.Load("", nil)
	p, err := providers.Build(cfg.Providers[0], s.Store)
	if err != nil {
		t.Fatal(err)
	}
	s.Providers.Add(p)
	var rows []ProviderRow
	call(t, ts, "GET", "/v1/providers", nil, &rows)
	var key *KeyView
	for _, r := range rows {
		if r.ID == "studio-pc" {
			key = r.Key
		}
	}
	if key == nil || !key.Fixed || key.Store != "the environment variable FORJA_SECRET_PROVIDER_STUDIO_PC" || !key.Stored {
		t.Fatalf("key %+v", key)
	}
	var prob struct{ Detail string }
	if code := call(t, ts, "PUT", "/v1/providers/studio-pc/token", map[string]string{"token": "other-token-1234"}, &prob); code != 409 ||
		!strings.Contains(prob.Detail, "FORJA_SECRET_PROVIDER_STUDIO_PC") {
		t.Fatalf("replacing it: %d %q", code, prob.Detail)
	}
}

func TestSharingSaysWhyTheEngineDoesNotServe(t *testing.T) {
	ts, s, _ := newServer(t)
	os.MkdirAll(filepath.Dir(config.UserFile()), 0o755)
	os.WriteFile(config.UserFile(), []byte("[engine]\nshare = true\ncommand = [\"/no/such/forja-engine\"]\n"), 0o644)
	var view ShareView
	call(t, ts, "GET", "/v1/share", nil, &view)
	if !view.Enabled || view.Error != "" {
		t.Fatalf("before any start: %+v", view)
	}
	_, _, _ = s.Engine.Endpoint(context.Background())
	view = ShareView{}
	call(t, ts, "GET", "/v1/share", nil, &view)
	if view.Active || view.Error == "" || strings.Contains(view.Error, "/no/such") || view.CodeExpiresAt != nil {
		t.Fatalf("after a failed start: %+v", view)
	}
}

// lanComputer is a second computer that manages its own models.
type lanComputer struct{ *fakeEngine }

func (c *lanComputer) ID() string          { return "studio" }
func (c *lanComputer) Kind() provider.Kind { return provider.KindEngine }

// Install, Repair, Remove and Fix of a row that belongs to another computer acted on this one.
func TestModelActionsActOnTheNamedComputerOrSayWhyNot(t *testing.T) {
	ts, s, eng := newServer(t)
	s.Providers.Add(&lanComputer{eng})
	eng.installed["shape"] = true
	var entry installs.Entry
	if code := call(t, ts, "POST", "/v1/models/shape-big/install", map[string]any{"provider": "studio"}, &entry); code != 202 ||
		entry.Provider != "studio" {
		t.Fatalf("install on the other computer: %d %+v", code, entry)
	}
	if code := call(t, ts, "DELETE", "/v1/models/shape?provider=studio", nil, &entry); code != 202 || entry.Provider != "studio" {
		t.Fatalf("remove from the other computer: %d %+v", code, entry)
	}
	var prob struct{ Detail string }
	for _, c := range []struct {
		method, path string
		code         int
		want         string
	}{
		{"DELETE", "/v1/models/shape?provider=nope", 404, "nope"},
		{"DELETE", "/v1/models/ghost", 404, "has no model ghost"},
		{"POST", "/v1/checks/uv/fix?provider=studio", 409, "only run on that computer"},
		{"POST", "/v1/checks/uv/fix?provider=nope", 404, "no provider nope"},
		{"GET", "/v1/checks?provider=nope", 404, "nope"},
		{"POST", "/v1/models/shape-big/install", 202, ""},
	} {
		prob.Detail = ""
		if code := call(t, ts, c.method, c.path, map[string]any{}, &prob); code != c.code || !strings.Contains(prob.Detail, c.want) {
			t.Errorf("%s %s: %d %q, want %d %q", c.method, c.path, code, prob.Detail, c.code, c.want)
		}
	}
}

func TestDiagnosticsHideTheHomeFolderInAnyCaseAndOtherComputersAddresses(t *testing.T) {
	home, _ := os.UserHomeDir()
	in := "log in " + home + `\x and ` + strings.ToUpper(home) + " and " + strings.ToLower(home) +
		" reached https://192.0.2.10:7861/v1 and 10.1.2.3:7860 but http://127.0.0.1:5000 stays"
	out := maskPrivate(in)
	if strings.Contains(strings.ToLower(out), strings.ToLower(home)) && runtime.GOOS == "windows" {
		t.Fatalf("home folder left: %s", out)
	}
	if strings.Contains(out, "192.0.2.10") || strings.Contains(out, "10.1.2.3") || !strings.Contains(out, "127.0.0.1:5000") {
		t.Fatalf("addresses: %s", out)
	}
}

func TestDiagnosticsLeaveOutProviderAddressesAndEndpointIds(t *testing.T) {
	secrets.InMemory()
	ts, _, _ := newServer(t)
	os.MkdirAll(filepath.Dir(config.UserFile()), 0o755)
	os.WriteFile(config.UserFile(), []byte("[[providers]]\nid = \"pod\"\nkind = \"serverless\"\nvendor = \"runpod\"\nurl = \"https://192.0.2.10:7861\"\nendpoint_id = \"ep-secret123\"\nsecret = \"provider-pod\"\n"), 0o644)
	resp, err := http.Get(ts.URL + "/v1/diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), "ep-secret123") || strings.Contains(string(raw), "192.0.2.10") || !strings.Contains(string(raw), `"pod"`) {
		t.Fatalf("diagnostics:\n%s", raw)
	}
}

func TestANewConfigFileSaysWhereTheKeysAreDescribed(t *testing.T) {
	ts, _, _ := newServer(t)
	if code := call(t, ts, "POST", "/v1/settings/file", nil, nil); code != 200 {
		t.Fatalf("status %d", code)
	}
	raw, _ := os.ReadFile(config.UserFile())
	if !strings.Contains(string(raw), "docs/configuration.md") || strings.Contains(string(raw), "section 11") {
		t.Fatalf("header:\n%s", raw)
	}
}
