package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const handWritten = `# Machine-local config, written by hand.
[engine]
# Point the app at the checkout's engine.
command = ["uv", "run", "forja-engine"]
fit_mode = "fit-first" # offline wins

[[providers]]
id = "desktop"
kind = "engine"
`

func edit(t *testing.T, start string, changes map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if start != "" {
		os.WriteFile(path, []byte(start), 0o644)
	}
	if err := Edit(path, changes, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	return string(out)
}

func TestEditKeepsCommentsAndOtherKeys(t *testing.T) {
	got := edit(t, handWritten, map[string]any{
		"engine.idle_unload_minutes": int64(5),
		"engine.fit_mode":            "quality-first",
		"models.stages.shape":        "triposg",
		"models.stages.texture.x":    nil, // removing what is not there is fine
	})
	for _, want := range []string{
		"# Machine-local config, written by hand.",
		"# Point the app at the checkout's engine.",
		`command = ["uv", "run", "forja-engine"]`,
		`fit_mode = "quality-first"`,
		"idle_unload_minutes = 5",
		"[models.stages]\nshape = \"triposg\"",
		"[[providers]]\nid = \"desktop\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Index(got, "idle_unload_minutes") > strings.Index(got, "[[providers]]") {
		t.Errorf("new engine key landed outside [engine]:\n%s", got)
	}
}

func TestEditRemovesKeysAndQuotesOddOnes(t *testing.T) {
	got := edit(t, "[models.stages]\nshape = \"triposg\"\ntexture = \"x\"\n", map[string]any{
		"models.stages.shape":              nil,
		"models.stages.background-removal": "birefnet",
		"routing.stages.texture.match":     "local",
	})
	if strings.Contains(got, "shape") || !strings.Contains(got, `background-removal = "birefnet"`) {
		t.Fatalf("got:\n%s", got)
	}
	if !strings.Contains(got, "[routing.stages.texture]\nmatch") && !strings.Contains(got, `"texture.match"`) {
		t.Fatalf("dotted key not written as a nested key:\n%s", got)
	}
}

func TestEditCreatesTheFile(t *testing.T) {
	got := edit(t, "", map[string]any{"engine.vram_reserve_gb": 2.5})
	if got != "[engine]\nvram_reserve_gb = 2.5\n" {
		t.Fatalf("got %q", got)
	}
}

func TestEditRewritesWhatItCannotEditInPlace(t *testing.T) {
	start := "[engine]\ncommand = [\n  \"uv\",\n  \"run\",\n]\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(start), 0o644)
	if err := Edit(path, map[string]any{"engine.fit_mode": "quality-first"}, nil); err != nil {
		t.Fatal(err)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != start {
		t.Fatal("the original was not kept")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "quality-first") || !strings.Contains(string(raw), "uv") {
		t.Fatalf("got:\n%s", raw)
	}
}

func TestEditRestoresTheFileWhenValidationFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(handWritten), 0o644)
	err := Edit(path, map[string]any{"engine.fit_mode": "fastest"}, func() error { return errors.New("bad fit_mode") })
	if err == nil {
		t.Fatal("validation error swallowed")
	}
	if raw, _ := os.ReadFile(path); string(raw) != handWritten {
		t.Fatalf("not restored:\n%s", raw)
	}
}

func TestProvidersAreAddedAndRemovedWithoutTouchingTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(handWritten), 0o644)
	p := Provider{ID: "studio", Label: "Studio PC", Kind: "engine", URL: "https://10.0.0.5:7861", Secret: "provider-studio", Fingerprint: "ab12"}
	if err := AddProvider(path, p, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), handWritten) || !strings.Contains(string(raw), `label = "Studio PC"`) {
		t.Fatalf("got:\n%s", raw)
	}
	if err := RemoveProvider(path, "desktop", nil); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	got := string(raw)
	if strings.Contains(got, `id = "desktop"`) || !strings.Contains(got, `id = "studio"`) || !strings.Contains(got, "# Point the app") {
		t.Fatalf("got:\n%s", got)
	}
	if err := RemoveProvider(path, "studio", nil); err != nil {
		t.Fatal(err)
	}
	if raw, _ = os.ReadFile(path); strings.Contains(string(raw), "providers") {
		t.Fatalf("got:\n%s", raw)
	}
}
