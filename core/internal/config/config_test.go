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
	os.WriteFile(filepath.Join(proj, "forja.toml"), []byte("[engine]\nidle_unload_minutes = 7\n[project]\nname = \"MyGame\"\n"), 0o644)
	t.Setenv("FORJA_ENGINE__FAIRNESS_CAP", "3")

	root := FindProject(filepath.Join(proj, "Assets"))
	if root != proj {
		t.Fatalf("FindProject = %q", root)
	}
	l, err := Load(root, map[string]any{"engine": map[string]any{"vram_reserve_gb": 2.5}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Engine.IdleUnloadMinutes != 7 || l.Sources["engine.idle_unload_minutes"] != "project" {
		t.Errorf("project layer lost: %d %s", l.Engine.IdleUnloadMinutes, l.Sources["engine.idle_unload_minutes"])
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

func TestInvalidValuesFailWithTheKey(t *testing.T) {
	isolate(t)
	_, err := Load("", map[string]any{"routing": map[string]any{"policy": "yolo"}})
	if err == nil || err.Error()[:15] != "routing.policy:" {
		t.Fatalf("got %v", err)
	}
}
