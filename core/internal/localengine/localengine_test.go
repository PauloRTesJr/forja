package localengine

import (
	"slices"
	"testing"

	"forja/internal/config"
)

func TestTheEngineIsToldWhichBlenderTheUserSet(t *testing.T) {
	cfg := &config.Loaded{}
	if env := Env(cfg); len(env) != 0 {
		t.Fatalf("without blender.path the engine looks for Blender itself, got %v", env)
	}
	cfg.Blender.Path = "/opt/blender/blender"
	if env := Env(cfg); !slices.Equal(env, []string{"FORJA_BLENDER=/opt/blender/blender"}) {
		t.Fatalf("env %v", env)
	}
}
