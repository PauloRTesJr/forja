package config

import (
	"os"
	"path/filepath"
	"testing"
)

func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "user"))
	return dir
}

func TestDefaultsAreFitFirstAndOffline(t *testing.T) {
	isolate(t)
	l, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Engine.FitMode != "fit-first" || l.Routing.AllowPaidFallback {
		t.Fatalf("unexpected defaults: %+v", l.Engine)
	}
	if l.Sources["engine.fit_mode"] != "default" {
		t.Fatal("source not tracked")
	}
}

func TestLayersOverrideInOrder(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	os.MkdirAll(user, 0o755)
	os.WriteFile(filepath.Join(user, "config.toml"), []byte("[engine]\nidle_unload_minutes = 5\nfit_mode = \"quality-first\"\n"), 0o644)
	proj := filepath.Join(dir, "game")
	os.MkdirAll(filepath.Join(proj, "Assets"), 0o755)
	os.WriteFile(filepath.Join(proj, "forja.toml"), []byte("[engine]\nidle_unload_minutes = 7\n[routing]\npolicy = \"fastest\"\n[project]\nname = \"MyGame\"\n"), 0o644)
	t.Setenv("FORJA_ENGINE__FAIRNESS_CAP", "3")

	root := FindProject(filepath.Join(proj, "Assets"))
	if root != proj {
		t.Fatalf("FindProject = %q", root)
	}
	l, err := Load(root, map[string]any{"engine": map[string]any{"vram_reserve_gb": 2.5}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Routing.Policy != "fastest" || l.Sources["routing.policy"] != "project" {
		t.Errorf("project layer lost: %s %s", l.Routing.Policy, l.Sources["routing.policy"])
	}
	// A project may not set the engine's settings: the user's value stands.
	if l.Engine.IdleUnloadMinutes != 5 || l.Sources["engine.idle_unload_minutes"] != "user" {
		t.Errorf("project set an engine setting: %d %s", l.Engine.IdleUnloadMinutes, l.Sources["engine.idle_unload_minutes"])
	}
	if l.Engine.FitMode != "quality-first" || l.Sources["engine.fit_mode"] != "user" {
		t.Error("user layer lost")
	}
	if l.Engine.FairnessCap != 3 || l.Sources["engine.fairness_cap"] != "env" {
		t.Error("env layer lost")
	}
	if l.Engine.VRAMReserveGB != 2.5 || l.Sources["engine.vram_reserve_gb"] != "flag" {
		t.Error("flag layer lost")
	}
	if l.Project.Name != "MyGame" || l.Project.ExportDir != "Assets/Generated" || l.Project.Root != proj {
		t.Errorf("project section: %+v", l.Project)
	}
}

func TestAProjectFileCannotRunProgramsOrLoosenSpending(t *testing.T) {
	dir := isolate(t)
	user := filepath.Join(dir, "user")
	os.MkdirAll(user, 0o755)
	os.WriteFile(filepath.Join(user, "config.toml"), []byte("[routing.budget_usd]\nper_day = 5.0\n"), 0o644)
	proj := filepath.Join(dir, "cloned")
	os.MkdirAll(proj, 0o755)
	os.WriteFile(filepath.Join(proj, "forja.toml"), []byte(`
[engine]
command = ["evil.exe"]
[blender]
path = "evil.exe"
[[providers]]
id = "free-fal"
kind = "api"
vendor = "fal"
secret = "provider-fal"
paid = false
[prices."fal-ai/model"]
usd = 0.0
per = "generation"
[routing]
allow_paid_fallback = true
[routing.budget_usd]
per_day = 50.0
per_job = 0.5
[routing.stages]
shape = "local"
`), 0o644)
	l, err := Load(proj, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Engine.Command) != 0 || l.Blender.Path != "" || len(l.Providers) != 0 || len(l.Prices) != len(Defaults()["prices"].(map[string]any)) {
		t.Fatalf("a project ran or priced things: command %v blender %q providers %v prices %v",
			l.Engine.Command, l.Blender.Path, l.Providers, l.Prices)
	}
	if l.Routing.AllowPaidFallback || l.Routing.Budget.PerDay != 5 || l.Routing.Budget.PerJob != 0.5 {
		t.Fatalf("spending loosened: paid fallback %v, budget %+v", l.Routing.AllowPaidFallback, l.Routing.Budget)
	}
	if l.Routing.Stages["shape"] != "local" {
		t.Fatal("a project's routing by stage was lost")
	}
}

func TestInvalidValuesFailWithTheKey(t *testing.T) {
	isolate(t)
	_, err := Load("", map[string]any{"routing": map[string]any{"policy": "yolo"}})
	if err == nil || err.Error()[:15] != "routing.policy:" {
		t.Fatalf("got %v", err)
	}
}
