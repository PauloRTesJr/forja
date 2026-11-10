package config

import (
	"reflect"
	"slices"
	"sync"

	"github.com/BurntSushi/toml"

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

func TestAHubProviderKeepsItsModelList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(handWritten), 0o644)
	p := Provider{ID: "fal", Label: "fal.ai", Kind: "api", Vendor: "fal", Secret: "provider-fal", Paid: true,
		Models: []string{"fal-ai/trellis-2"}}
	if err := AddProvider(path, p, nil); err != nil {
		t.Fatal(err)
	}
	p.Models = append(p.Models, "tripo3d/h3.1/image-to-3d")
	if err := UpdateProvider(path, p, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Fatal("updating a provider rewrote the whole file")
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	if !strings.Contains(got, `models = ["fal-ai/trellis-2", "tripo3d/h3.1/image-to-3d"]`) || strings.Count(got, `id = "fal"`) != 1 ||
		!strings.Contains(got, "# Point the app") || !strings.Contains(got, `vendor = "fal"`) || !strings.Contains(got, "paid = true") {
		t.Fatalf("got:\n%s", got)
	}
}

// Changing a setting in the app dropped the note written beside it in config.toml.
func TestAnEditedLineKeepsTheCommentBesideIt(t *testing.T) {
	got := edit(t, handWritten, map[string]any{"engine.fit_mode": "quality-first"})
	if !strings.Contains(got, `fit_mode = "quality-first" # offline wins`) {
		t.Fatalf("got:\n%s", got)
	}
	// A # inside the value is part of the value, not a comment to keep.
	got = edit(t, "[project]\nname = \"crate #7\"   # the seventh\nexport_dir = \"Assets/#gen\"\n", map[string]any{
		"project.name": "barrel", "project.export_dir": "Assets/Generated",
	})
	if !strings.Contains(got, "name = \"barrel\"   # the seventh\n") || !strings.Contains(got, "export_dir = \"Assets/Generated\"\n") {
		t.Fatalf("got:\n%s", got)
	}
}

// Unpinning the last model, or putting the last price back, left "[models.stages]" or
// "[prices.x]" with nothing in it.
func TestRemovingATablesLastKeyRemovesTheTable(t *testing.T) {
	start := "[engine]\nfit_mode = \"fit-first\"\n\n[models.stages]\nshape = \"triposg\"\n\n[routing.stages]\nshape = \"lan\"\n\n[project]\nname = \"x\"\n"
	got := edit(t, start, map[string]any{"models.stages.shape": nil, "routing.stages.shape": nil})
	if want := "[engine]\nfit_mode = \"fit-first\"\n\n[project]\nname = \"x\"\n"; got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	// At the end of the file, and with a quoted name.
	got = edit(t, "[engine]\nfit_mode = \"fit-first\"\n\n[prices.\"fal-ai/trellis-2\"]\nusd = 0.5\nper = \"generation\"\n",
		map[string]any{Key("prices", "fal-ai/trellis-2", "usd"): nil, Key("prices", "fal-ai/trellis-2", "per"): nil})
	if want := "[engine]\nfit_mode = \"fit-first\"\n"; got != want {
		t.Fatalf("got:\n%q", got)
	}
	// A table that keeps a key, a comment or a table under it stays.
	for _, keep := range []string{
		"[models.stages]\nshape = \"triposg\"\ntexture = \"paint\"\n",
		"[models.stages]\n# shape = \"hunyuan\" was slower\nshape = \"triposg\"\n",
		"[models]\nstubs = true\n\n[models.stages]\nshape = \"triposg\"\n",
	} {
		got := edit(t, keep, map[string]any{"models.stages.shape": nil})
		if strings.Contains(got, "triposg") || !strings.Contains(got, "[models") {
			t.Fatalf("from %q got:\n%s", keep, got)
		}
		if strings.Contains(keep, "texture") && !strings.Contains(got, "[models.stages]\ntexture = \"paint\"") {
			t.Fatalf("got:\n%s", got)
		}
		if strings.Contains(keep, "stubs") && strings.Contains(got, "[models.stages]") {
			t.Fatalf("the emptied table stayed:\n%s", got)
		}
	}
}

func TestARemovedProviderTakesTheCommentAboveItAlong(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	start := "[engine]\nfit_mode = \"fit-first\"\n\n# The studio PC, in the back room.\n# Paired in March.\n[[providers]]\nid = \"studio\"\nkind = \"engine\"\n\n" +
		"# The laptop.\n[[providers]]\nid = \"laptop\"\nkind = \"engine\"\n"
	os.WriteFile(path, []byte(start), 0o644)
	if err := RemoveProvider(path, "studio", nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if want := "[engine]\nfit_mode = \"fit-first\"\n\n# The laptop.\n[[providers]]\nid = \"laptop\"\nkind = \"engine\"\n"; string(raw) != want {
		t.Fatalf("got:\n%s", raw)
	}
}

// The file the test app starts from: no blank line between tables, a comment first, and
// [models] holding only stubs.
const tightFile = "# Written by the launcher.\n# My own note above the engine.\n[engine]\ncommand = [\"uv\", \"run\", \"--quiet\", \"forja-engine\"]\nstart_timeout = \"300s\"\n[models]\nstubs = true\n"

// withoutEmpty drops the tables that hold nothing, which no reader can tell from absent ones.
func withoutEmpty(m map[string]any) map[string]any {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			if withoutEmpty(sub); len(sub) == 0 {
				delete(m, k)
			}
		}
	}
	return m
}

func decoded(t *testing.T, text string) map[string]any {
	t.Helper()
	m := map[string]any{}
	if _, err := toml.Decode(text, &m); err != nil {
		t.Fatalf("not TOML any more: %v\n%s", err, text)
	}
	out, _ := normalize(withoutEmpty(m)).(map[string]any)
	return out
}

// Everything the edits did not name must come out as it went in, byte for byte.
func TestEditsNeverTouchWhatTheyDoNotName(t *testing.T) {
	variants := map[string]string{
		"no blank lines between tables": tightFile,
		"no final newline":              strings.TrimSuffix(tightFile, "\n"),
		"CRLF":                          strings.ReplaceAll(tightFile, "\n", "\r\n"),
		"models then pin":               "[engine]\nfit_mode = \"fit-first\"\n[models]\nstubs = true\n[models.stages]\nshape = \"triposg\"\n[project]\nname = \"x\"\n",
		"only a pin":                    "# top\n[models]\n[models.stages]\nshape = \"triposg\"\n[engine]\nstart_timeout = \"300s\"\n",
		"price next to stubs":           "[models]\nstubs = true\n[prices.\"fal-ai/x\"]\nusd = 0.5\nper = \"generation\"\n[engine]\nstart_timeout = \"300s\"\n",
		"budget beside the engine":      "[engine]\nstart_timeout = \"300s\"\n[routing.budget_usd]\nper_day = 5.0\n[models]\nstubs = true\n",
	}
	price := func(k string) string { return Key("prices", "fal-ai/x", k) }
	sequences := map[string][]map[string]any{
		"vram":    {{"engine.vram_reserve_gb": 2.5}, {"engine.vram_reserve_gb": nil}},
		"idle":    {{"engine.idle_unload_minutes": int64(5)}, {"engine.idle_unload_minutes": nil}},
		"limit":   {{"routing.budget_usd.per_job": 3.0}, {"routing.budget_usd.per_job": nil}},
		"limits":  {{"routing.budget_usd.per_day": 9.0, "routing.budget_usd.per_month": 40.0}, {"routing.budget_usd.per_day": nil, "routing.budget_usd.per_month": nil}},
		"price":   {{price("usd"): 0.7, price("per"): "generation"}, {price("usd"): nil, price("per"): nil}},
		"pin":     {{"models.stages.shape": "triposg", "routing.stages.shape": nil}, {"models.stages.shape": nil, "routing.stages.shape": nil}},
		"unpin":   {{"models.stages.shape": nil, "routing.stages.shape": nil}},
		"engine+": {{"engine.fit_mode": "quality-first"}, {"engine.fit_mode": nil}},
	}
	for vname, start := range variants {
		for sname, steps := range sequences {
			t.Run(vname+"/"+sname, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config.toml")
				os.WriteFile(path, []byte(start), 0o644)
				want := decoded(t, start)
				for _, step := range steps {
					if err := Edit(path, step, nil); err != nil {
						t.Fatal(err)
					}
				}
				raw, _ := os.ReadFile(path)
				got := string(raw)
				// The same data, except what the sequence itself removed (the original pin, price or limit).
				for _, step := range steps[len(steps)-1:] {
					for k, v := range step {
						if v == nil {
							deletePath(want, keyParts(k))
						}
					}
				}
				if g := decoded(t, got); !reflect.DeepEqual(g, withoutEmpty(want)) {
					t.Fatalf("data changed\nfrom:\n%s\nto:\n%s", start, got)
				}
				// And the lines of the original that the edits did not name are all still there, in order.
				in := strings.Split(strings.ReplaceAll(start, "\r\n", "\n"), "\n")
				out := strings.Split(strings.ReplaceAll(got, "\r\n", "\n"), "\n")
				named := map[string]bool{}
				for _, step := range steps {
					for k := range step {
						parts := keyParts(k)
						named[parts[len(parts)-1]] = true
					}
				}
				at := 0
				for _, line := range in {
					key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
					if strings.TrimSpace(line) == "" || named[key] || (strings.HasPrefix(line, "[") && sname != "pin" && sname != "unpin" && sname != "price") {
						continue
					}
					if strings.HasPrefix(line, "[") {
						// a header may go only when the edit emptied its table
						continue
					}
					i := slices.Index(out[at:], line)
					if i < 0 {
						t.Fatalf("line %q is gone or out of order\nfrom:\n%s\nto:\n%s", line, start, got)
					}
					at += i + 1
				}
			})
		}
	}
}

func deletePath(m map[string]any, parts []string) {
	for _, p := range parts[:len(parts)-1] {
		sub, ok := m[p].(map[string]any)
		if !ok {
			return
		}
		m = sub
	}
	delete(m, parts[len(parts)-1])
}

func TestTwoEditsAtOnceBothLand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte(tightFile), 0o644)
	var wg sync.WaitGroup
	keys := []string{"engine.vram_reserve_gb", "engine.idle_unload_minutes", "routing.budget_usd.per_job", "routing.budget_usd.per_day", "engine.fit_mode"}
	vals := []any{2.0, int64(7), 1.0, 2.0, "quality-first"}
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 5; n++ {
				if err := Edit(path, map[string]any{keys[i]: vals[i]}, nil); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	raw, _ := os.ReadFile(path)
	m := decoded(t, string(raw))
	if !reflect.DeepEqual(lookupPath(m, "models.stubs"), true) || lookupPath(m, "engine.vram_reserve_gb") != 2.0 ||
		lookupPath(m, "engine.fit_mode") != "quality-first" || lookupPath(m, "routing.budget_usd.per_day") != 2.0 {
		t.Fatalf("an edit was lost:\n%s", raw)
	}
}

func lookupPath(m map[string]any, key string) any {
	var cur any = m
	for _, p := range strings.Split(key, ".") {
		sub, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = sub[p]
	}
	return cur
}
