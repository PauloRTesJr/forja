package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"forja/internal/api"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/installs"
	"forja/internal/library"
	"forja/internal/provider/fal"
	"forja/internal/secrets"
)

func TestGenerateFollowsAJobToAnAssetTheLibraryCanSave(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")

	out := h.ok("generate", img, "--preset", "draft", "--seed", "7")
	if !strings.Contains(out, "✓ chest (draft)") || !strings.Contains(out, "shape") {
		t.Fatalf("generate printed:\n%s", out)
	}

	var jobsList []*library.Job
	h.okJSON(&jobsList, "jobs", "ls", "--limit", "5")
	if len(jobsList) != 1 || jobsList[0].Status != library.Succeeded || jobsList[0].Seed != 7 {
		t.Fatalf("jobs %+v", jobsList)
	}
	job := jobsList[0]
	if out := h.ok("jobs", "show", job.ID); !strings.Contains(out, "succeeded") || !strings.Contains(out, "export") ||
		!strings.Contains(out, "asset     "+job.AssetID) {
		t.Fatalf("jobs show:\n%s", out)
	}

	// The core read the stats from the GLB; nothing had to open it in a viewer.
	var assets []*library.Asset
	h.okJSON(&assets, "assets", "ls", "chest")
	if len(assets) != 1 || assets[0].ID != job.AssetID || assets[0].Stats["triangles"] != 100.0 || assets[0].Stats["lods"] != 2.0 {
		t.Fatalf("assets %+v", assets)
	}
	if out := h.ok("assets", "ls", "no-such-name"); strings.Count(out, "\n") != 1 {
		t.Fatalf("a search for nothing listed:\n%s", out)
	}
	if out := h.ok("assets", "show", job.AssetID); !strings.Contains(out, "triangles  100") || !strings.Contains(out, "vertices   80") {
		t.Fatalf("assets show:\n%s", out)
	}

	dest := filepath.Join(h.dir, "out")
	if out := h.ok("assets", "get", job.AssetID, "-o", dest); strings.TrimSpace(out) != filepath.Join(dest, "chest.glb") {
		t.Fatalf("assets get printed %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(dest, "chest.glb"))
	want, _ := os.ReadFile(must(h.store.Path(assets[0].GLB)))
	if len(got) == 0 || !bytes.Equal(got, want) {
		t.Fatalf("saved %d bytes, want the asset's %d", len(got), len(want))
	}
	if r := h.cli("", "assets", "get", job.AssetID, job.AssetID, "-o", filepath.Join(dest, "one.glb")); r.code == 0 {
		t.Fatal("two assets were saved to one file")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestBlenderSwitchesSayWhyTheyCannotRunAndRefuseTheJob(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	r := h.cli("", "generate", img, "--preset", "hero-character", "--dry-run")
	for _, want := range []string{
		"Quad remesh with Blender (remesh.enabled)",
		"remesh cannot be turned on now: Blender 4.2 or newer was not found",
		"uv cannot be turned on now: Blender 4.2 or newer was not found",
		"see what it needs: forja models checks --stage mesh.remesh",
	} {
		if !strings.Contains(r.out, want) {
			t.Fatalf("dry run lacks %q:\n%s", want, r.out)
		}
	}
	var plan api.PresetView
	h.okJSON(&plan, "presets", "show", "hero-character")
	if plan.Unavailable["remesh"] == "" || plan.Unavailable["match"] != "" || !plan.Install["remesh"].Software {
		t.Fatalf("unavailable %v, install %v", plan.Unavailable, plan.Install)
	}
	if r := h.cli("", "models", "checks", "--stage", "mesh.remesh"); r.code != 0 {
		t.Fatalf("checks --stage: exit %d: %s", r.code, r.err)
	}
	r = h.cli("", "generate", img, "--preset", "hero-character", "--with", "remesh")
	if r.code == 0 || !strings.Contains(r.err, "no provider can run mesh.remesh: Blender 4.2 or newer was not found") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}

func TestADryRunShowsThePlanAndQueuesNothing(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	out := h.ok("generate", img, "--preset", "draft", "--dry-run")
	for _, want := range []string{"draft (Draft)", "STAGE", "shape", "Shape", "local"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry run lacks %q:\n%s", want, out)
		}
	}
	var plan api.PresetView
	h.okJSON(&plan, "generate", img, "--preset", "draft", "--dry-run", "--view", "back="+img)
	if !plan.Ready || len(plan.Plan) != 5 {
		t.Fatalf("plan %+v", plan)
	}
	var list []*library.Job
	h.okJSON(&list, "jobs", "ls")
	if len(list) != 0 {
		t.Fatalf("a dry run queued %d jobs", len(list))
	}
	// Without a model for a stage, the plan says so and the exit code too.
	h.eng.set(func(e *fakeEngine) { delete(e.installed, "shape") })
	if r := h.cli("", "generate", img, "--preset", "draft", "--dry-run"); r.code != 1 || !strings.Contains(r.out, "none ready") {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
	if out := h.ok("presets", "ls"); !strings.Contains(out, "shape: none ready") {
		t.Fatalf("presets ls:\n%s", out)
	}
	if r := h.cli("", "presets", "show", "no-such-preset"); r.code != 1 || !strings.Contains(r.err, "forja presets ls") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}

const sheet = `{"sheet": true, "size": [1200, 1300], "parts": [
	{"box": [100, 40, 800, 780], "kind": "figure", "view": "front"},
	{"box": [860, 90, 1190, 530], "kind": "detail", "view": ""},
	{"box": [540, 820, 690, 1190], "kind": "figure", "view": "left"},
	{"box": [800, 820, 1170, 1190], "kind": "figure", "view": "back"}]}`

func TestASheetOfViewsIsAnsweredFromTheCommandLine(t *testing.T) {
	h := newHarness(t)
	h.eng.set(func(e *fakeEngine) { e.sheet = sheet })
	img := h.image("turnaround.png")

	// Nobody at a terminal: the job waits, and the exit code says so.
	r := h.cli("", "--json", "generate", img, "--preset", "draft", "--sheet", "ask")
	if r.code != exitWaiting {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	var waiting []*library.Job
	if err := json.Unmarshal([]byte(r.out), &waiting); err != nil || len(waiting) != 1 || waiting[0].Status != library.Waiting {
		t.Fatalf("%v %s", err, r.out)
	}
	id := waiting[0].ID
	if out := h.ok("jobs", "sheet", id); !strings.Contains(out, "4 figures") || !strings.Contains(out, "close-up") ||
		!strings.Contains(out, "--views") {
		t.Fatalf("jobs sheet:\n%s", out)
	}
	if r := h.cli("", "jobs", "sheet", id, "--views", "front,back"); r.code != 1 || !strings.Contains(r.err, "each of the 4 figures") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if r := h.cli("", "jobs", "sheet", id, "--views", "back,-,left,-"); r.code != 1 || !strings.Contains(r.err, "front") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	out := h.ok("jobs", "sheet", id, "--views", "front,-,-,back")
	if !strings.Contains(out, "✓ turnaround (draft)") {
		t.Fatalf("jobs sheet --views:\n%s", out)
	}
	var done library.Job
	h.okJSON(&done, "jobs", "show", id)
	if done.Status != library.Succeeded || done.Views["front"] == "" || done.Views["back"] == "" || done.Views["left"] != "" {
		t.Fatalf("job %s, views %v", done.Status, done.Views)
	}
	if r := h.cli("", "jobs", "sheet", id, "--split"); r.code != 1 || !strings.Contains(r.err, "not waiting") {
		t.Fatalf("answering twice: exit %d: %s", r.code, r.err)
	}

	// At a terminal, generate asks: a wrong answer is explained, then the proposal is taken.
	r = h.cliAt(true, "back\n\n", "generate", h.image("second.png"), "--preset", "draft", "--sheet", "ask")
	if r.code != 0 || !strings.Contains(r.out, "Build from front, left, back?") || !strings.Contains(r.out, "each of the 4 figures") ||
		!strings.Contains(r.out, "✓ second (draft)") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	// Input that ends leaves the job waiting, as if answered later.
	r = h.cliAt(true, "", "generate", h.image("third.png"), "--preset", "draft", "--sheet", "ask")
	if r.code != exitWaiting || !strings.Contains(r.out, "forja jobs sheet") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	var list []*library.Job
	h.okJSON(&list, "jobs", "ls")
	if list[0].Status != library.Waiting {
		t.Fatalf("newest job %s", list[0].Status)
	}
	h.ok("jobs", "cancel", list[0].ID)
	h.okJSON(&done, "jobs", "show", list[0].ID)
	if done.Status != library.Cancelled {
		t.Fatalf("cancelled job is %s", done.Status)
	}
	// --sheet keep is the default, and says how to split next time.
	if out := h.ok("generate", h.image("fourth.png"), "--preset", "draft"); !strings.Contains(out, "--sheet split") {
		t.Fatalf("generate:\n%s", out)
	}
}

func TestSetupInstallsWhatThePresetsNeedThenTestsThem(t *testing.T) {
	h := newHarness(t)
	h.eng.set(func(e *fakeEngine) { e.installed = map[string]bool{} })

	out := h.ok("setup")
	for _, want := range []string{"TestOS", "Test GPU (cuda), 12.0 GB VRAM", "500 GB free", "background-removal", "install",
		"Re-run with --yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup lacks %q:\n%s", want, out)
		}
	}
	// The restricted license must be accepted; no one at a terminal can.
	if r := h.cli("", "setup", "--yes"); r.code != 1 || !strings.Contains(r.out, "--accept-license") {
		t.Fatalf("exit %d\n%s%s", r.code, r.out, r.err)
	}
	out = h.ok("setup", "--yes", "--accept-license")
	if !strings.Contains(out, "✓ install paint") || !strings.Contains(out, "Testing with a sample image") ||
		!strings.Contains(out, "✓ sample-chest") {
		t.Fatalf("setup --yes:\n%s", out)
	}
	if out := h.ok("setup", "--no-test"); !strings.Contains(out, "Every built-in preset can run") {
		t.Fatalf("setup again:\n%s", out)
	}
	var tasks []installs.Entry
	h.okJSON(&tasks, "installs", "ls")
	if len(tasks) < 3 {
		t.Fatalf("tasks %+v", tasks)
	}
	if out := h.ok("installs", "watch", tasks[0].ID); !strings.Contains(out, "✓") {
		t.Fatalf("installs watch:\n%s", out)
	}
	if r := h.cli("", "installs", "cancel", "local/no-such-task"); r.code != 1 || !strings.Contains(r.err, "forja installs ls") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
}

func TestModelsAreListedPinnedFixedAndRemoved(t *testing.T) {
	h := newHarness(t)
	out := h.ok("models", "ls")
	if !strings.Contains(out, "shape-mv") || !strings.Contains(out, "LicenseRef-Test") {
		t.Fatalf("models ls:\n%s", out)
	}
	h.ok("models", "pin", "shape", "shape-mv")
	cfg, _ := config.Load("", nil)
	if cfg.Models.Stages["shape"] != "shape-mv" || cfg.Routing.Stages["shape"] != "local" {
		t.Fatalf("pinned %v %v", cfg.Models.Stages, cfg.Routing.Stages)
	}
	if out := h.ok("models", "ls", "--all"); !strings.Contains(out, "shape (pinned)") {
		t.Fatalf("models ls --all:\n%s", out)
	}
	for args, want := range map[string]string{
		"models pin shape paint":   "does not do shape",
		"models pin shape nothing": "forja models ls --all",
		"models unpin shape.x":     "not a stage type",
	} {
		if r := h.cli("", strings.Fields(args)...); r.code != 1 || !strings.Contains(r.err, want) {
			t.Errorf("%s: exit %d: %s", args, r.code, r.err)
		}
	}
	h.ok("models", "unpin", "shape")
	if cfg, _ := config.Load("", nil); cfg.Models.Stages["shape"] != "" {
		t.Fatalf("still pinned: %v", cfg.Models.Stages)
	}

	out = h.ok("models", "checks")
	if !strings.Contains(out, "NO  C++ build tools") || !strings.Contains(out, "forja models fix msvc") {
		t.Fatalf("models checks:\n%s", out)
	}
	if out := h.ok("models", "fix", "msvc"); !strings.Contains(out, "✓ fix msvc") {
		t.Fatalf("models fix:\n%s", out)
	}
	if out := h.ok("models", "rm", "paint"); !strings.Contains(out, "✓ remove paint") {
		t.Fatalf("models rm:\n%s", out)
	}
}

// fakeFal answers fal.ai's model, pricing and catalog APIs: every endpoint returns a GLB
// except x/video.
func fakeFal(t *testing.T) {
	t.Helper()
	file := map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"},
		"content_type": map[string]any{"type": "string"}}}
	describe := func(id string) map[string]any {
		out := "model_glb"
		if id == "x/video" {
			out = "video"
		}
		return map[string]any{"endpoint_id": id, "metadata": map[string]any{"display_name": id},
			"openapi": map[string]any{
				"paths": map[string]any{
					"/" + id: map[string]any{"post": map[string]any{"requestBody": map[string]any{"content": map[string]any{
						"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/In"}}}}}},
					"/" + id + "/requests/{request_id}": map[string]any{"get": map[string]any{"responses": map[string]any{"200": map[string]any{
						"content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Out"}}}}}}},
				},
				"components": map[string]any{"schemas": map[string]any{
					"In":   map[string]any{"type": "object", "required": []any{"image_url"}, "properties": map[string]any{"image_url": map[string]any{"type": "string"}}},
					"Out":  map[string]any{"type": "object", "properties": map[string]any{out: map[string]any{"$ref": "#/components/schemas/File"}}},
					"File": file,
				}},
			}}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" && r.Header.Get("Authorization") != "Key fal-test" {
			w.WriteHeader(401)
			return
		}
		ids := r.URL.Query()["endpoint_id"]
		switch {
		case r.URL.Path == "/v1/models/pricing":
			var prices []any
			for _, id := range ids {
				prices = append(prices, map[string]any{"endpoint_id": id, "unit_price": 0.3, "unit": "generation", "currency": "USD"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"prices": prices})
		case r.URL.Path == "/v1/models" && r.URL.Query().Get("category") != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{describe("x/new-3d"), describe("x/video")},
				"has_more": true, "next_cursor": "page-2"})
		case r.URL.Path == "/v1/models":
			var models []any
			for _, id := range ids {
				models = append(models, describe(id))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	apiBase, queue := fal.APIBase, fal.QueueBase
	fal.APIBase, fal.QueueBase = srv.URL, srv.URL
	t.Cleanup(func() { fal.APIBase, fal.QueueBase = apiBase, queue })
}

func TestAHubIsConnectedBrowsedAndItsModelsPinned(t *testing.T) {
	fakeFal(t)
	h := newHarness(t)
	if r := h.cli("wrong\n", "hubs", "connect", "fal"); r.code != 1 {
		t.Fatalf("a wrong key: exit %d", r.code)
	}
	if r := h.cli("", "hubs", "connect", "fal"); r.code != 1 || !strings.Contains(r.err, "no API key") {
		t.Fatalf("no key: exit %d: %s", r.code, r.err)
	}
	out := h.cli("fal-test\n", "hubs", "connect", "fal")
	if out.code != 0 || !strings.Contains(out.out, "Connected fal.ai as fal") {
		t.Fatalf("exit %d\n%s%s", out.code, out.out, out.err)
	}
	if raw, _ := os.ReadFile(config.UserFile()); strings.Contains(string(raw), "fal-test") {
		t.Fatal("the key is in config.toml")
	}
	if out := h.ok("hubs", "ls"); !strings.Contains(out, "fal.ai") || !strings.Contains(out, "ok, ") {
		t.Fatalf("hubs ls:\n%s", out)
	}

	out2 := h.ok("hubs", "search", "fal", "chest")
	if !strings.Contains(out2, "x/new-3d") || !strings.Contains(out2, "cannot use") || !strings.Contains(out2, "--cursor page-2 chest") {
		t.Fatalf("hubs search:\n%s", out2)
	}
	if r := h.cli("", "hubs", "add", "fal", "x/video"); r.code != 1 || !strings.Contains(r.err, "cannot use") {
		t.Fatalf("adding a model without a mesh: exit %d: %s", r.code, r.err)
	}
	h.ok("hubs", "add", "fal", "x/new-3d")
	h.ok("models", "pin", "shape", "x/new-3d")
	var plan api.PresetView
	h.okJSON(&plan, "presets", "show", "draft")
	if plan.Plan[2].Provider != "fal" || !plan.Plan[2].Paid {
		t.Fatalf("shape %+v", plan.Plan[2])
	}
	if out := h.ok("generate", h.image("chest.png"), "--preset", "draft", "--dry-run"); !strings.Contains(out, "$0.30 per generation") ||
		!strings.Contains(out, "Each image costs about $0.30") {
		t.Fatalf("dry run:\n%s", out)
	}
	h.ok("hubs", "rm", "fal", "x/new-3d")
	if cfg, _ := config.Load("", nil); cfg.Models.Stages["shape"] != "" {
		t.Fatalf("removing the pinned model left the pin: %v", cfg.Models.Stages)
	}
	if out := h.ok("spend"); !strings.Contains(out, "today       $0.00  (no limit)") {
		t.Fatalf("spend:\n%s", out)
	}
	h.ok("config", "set", "routing.budget_usd.per_day=2.5")
	if out := h.ok("spend"); !strings.Contains(out, "limit $2.50") {
		t.Fatalf("spend:\n%s", out)
	}
	if _, err := secrets.Get("provider-fal"); err != nil {
		t.Fatalf("keychain: %v", err)
	}
}

func TestAnotherComputerIsPairedTestedAndForgotten(t *testing.T) {
	h := newHarness(t)
	tokens := map[string]bool{"tok-0123456789": true, "tok-new-9876": true}
	remote := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pair" {
			_, _ = w.Write([]byte(`{"token":"tok-0123456789","name":"Studio PC"}`))
			return
		}
		if token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer")); token != "" && !tokens[token] {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/info":
			_ = json.NewEncoder(w).Encode(engineapi.Info{APIVersion: engineapi.APIVersion, Version: "9.9",
				System: map[string]any{"devices": []any{map[string]any{"backend": "cuda", "name": "RTX Test"}}}})
		case "/v1/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	// The first try at an unknown certificate fails the handshake by design; keep it quiet.
	remote.Config.ErrorLog = log.New(io.Discard, "", 0)
	remote.StartTLS()
	defer remote.Close()

	if out := h.ok("providers", "test", remote.URL); !strings.Contains(out, "forja-engine 9.9") || !strings.Contains(out, "RTX Test") {
		t.Fatalf("providers test:\n%s", out)
	}
	if r := h.cli("", "providers", "add", remote.URL); r.code != 1 || !strings.Contains(r.err, "--code") {
		t.Fatalf("no code: exit %d: %s", r.code, r.err)
	}
	out := h.ok("providers", "add", remote.URL, "--code", "482 913")
	if !strings.Contains(out, "Added Studio PC as studio-pc") || !strings.Contains(out, "certificate") {
		t.Fatalf("providers add:\n%s", out)
	}
	if raw, _ := os.ReadFile(config.UserFile()); strings.Contains(string(raw), "tok-0123456789") {
		t.Fatal("the token is in config.toml")
	}
	if out := h.ok("providers", "ls"); !strings.Contains(out, "studio-pc") || !strings.Contains(out, "…6789") ||
		!strings.Contains(out, "local") {
		t.Fatalf("providers ls:\n%s", out)
	}
	if r := h.cli("tok-wrong\n", "providers", "token", "studio-pc"); r.code != 1 {
		t.Fatal("a token that does not work replaced the old one")
	}
	if r := h.cli("tok-new-9876\n", "providers", "token", "studio-pc"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if v, _ := secrets.Get("provider-studio-pc"); v != "tok-new-9876" {
		t.Fatalf("keychain holds %q", v)
	}
	h.ok("providers", "rm", "studio-pc")
	if _, err := secrets.Get("provider-studio-pc"); err == nil {
		t.Fatal("the token outlived its provider")
	}
	if out := h.ok("providers", "kinds"); !strings.Contains(out, "engine: Another computer running Forja") {
		t.Fatalf("providers kinds:\n%s", out)
	}
}

func TestSharingIsTurnedOnAndOff(t *testing.T) {
	h := newHarness(t)
	if out := h.ok("share"); !strings.Contains(out, "Not sharing") {
		t.Fatalf("share:\n%s", out)
	}
	if out := h.ok("share", "on"); !strings.Contains(out, "Sharing is on") {
		t.Fatalf("share on:\n%s", out)
	}
	if cfg, _ := config.Load("", nil); !cfg.Engine.Share {
		t.Fatal("not saved")
	}
	var v api.ShareView
	h.okJSON(&v, "share", "off")
	if v.Enabled {
		t.Fatalf("share off: %+v", v)
	}
}

func TestSettingsAreReadAndWrittenThroughTheCore(t *testing.T) {
	h := newHarness(t)
	h.ok("config", "set", "engine.idle_unload_minutes=5", "models.stubs=true")
	if out := h.ok("config", "get", "engine.idle_unload_minutes"); strings.TrimSpace(out) != "5" {
		t.Fatalf("config get: %q", out)
	}
	out := h.ok("config", "get")
	if !strings.Contains(out, "engine.idle_unload_minutes") || !strings.Contains(out, "user") || !strings.Contains(out, "15") {
		t.Fatalf("config get:\n%s", out)
	}
	for _, bad := range []string{"routing.policy=whatever", "engine.idle_unload_minutes=1.5", "engine.command=rm", "nonsense"} {
		if r := h.cli("", "config", "set", bad); r.code != 1 {
			t.Errorf("config set %s: exit %d", bad, r.code)
		}
	}
	if r := h.cli("", "config", "get", "engine.command"); r.code != 1 || !strings.Contains(r.err, "forja config show") {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	h.ok("config", "unset", "engine.idle_unload_minutes")
	if cfg, _ := config.Load("", nil); cfg.Engine.IdleUnloadMinutes != 15 || !cfg.Models.Stubs {
		t.Fatalf("engine %+v, models %+v", cfg.Engine, cfg.Models)
	}

	project := filepath.Join(h.dir, "game")
	h.ok("config", "set", "--project", project, "project.export_dir=Assets/Generated")
	if raw, _ := os.ReadFile(filepath.Join(project, "forja.toml")); !strings.Contains(string(raw), "Assets/Generated") {
		t.Fatalf("forja.toml:\n%s", raw)
	}
	if out := h.ok("config", "get", "--project", project, "project.export_dir"); strings.TrimSpace(out) != `"Assets/Generated"` {
		t.Fatalf("config get: %q", out)
	}
	if out := h.ok("config", "show", "--effective", "--project", project); !strings.Contains(out, "project.export_dir = project") {
		t.Fatalf("config show:\n%s", out)
	}
	var files struct{ Path string }
	h.okJSON(&files, "config", "edit")
	if files.Path != config.UserFile() {
		t.Fatalf("config edit: %+v", files)
	}
}

func TestTheCoreCanBeAskedAboutAndStopped(t *testing.T) {
	h := newHarness(t)
	if out := h.ok("daemon", "status"); !strings.Contains(out, "A core is running") {
		t.Fatalf("daemon status:\n%s", out)
	}
	if out := h.ok("info"); !strings.Contains(out, `"version": "test"`) {
		t.Fatalf("info:\n%s", out)
	}
	if out := h.ok("diagnostics"); !strings.Contains(out, "## Settings") || !strings.Contains(out, "## Engine log") {
		t.Fatalf("diagnostics:\n%s", out)
	}
	h.ok("engine", "unload")
	h.ok("daemon", "stop")
	select {
	case <-h.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the core was not asked to stop")
	}
}

func TestCommandsThatMustOutliveThemselvesNeedARunningCore(t *testing.T) {
	newHarness(t)
	gone := testAddress(t)
	for _, args := range [][]string{
		{"share", "code"},
		{"daemon", "stop"},
		{"generate", "x.png", "--no-wait"},
	} {
		var out, errOut bytes.Buffer
		a := &app{version: "test", in: strings.NewReader(""), out: &out, errOut: &errOut}
		if code := a.run(append([]string{"--socket", gone}, args...)); code != 1 || !strings.Contains(errOut.String(), "running core") {
			t.Errorf("%v: exit %d: %s", args, code, errOut.String())
		}
		if a.core != nil {
			t.Errorf("%v started a core", args)
		}
	}
	var out, errOut bytes.Buffer
	a := &app{version: "test", in: strings.NewReader(""), out: &out, errOut: &errOut}
	if code := a.run([]string{"--socket", gone, "daemon", "status"}); code != 1 || !strings.Contains(out.String(), "No core is running") {
		t.Fatalf("daemon status: exit %d: %s", code, out.String())
	}
}

func TestEventsArePrintedAsJSONLines(t *testing.T) {
	h := newHarness(t)
	var out, errOut bytes.Buffer
	a := &app{version: "test", in: strings.NewReader(""), out: &syncBuffer{b: &out}, errOut: &errOut}
	done := make(chan int)
	go func() { done <- a.run([]string{"--socket", h.addr, "events", "--type", "settings."}) }()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(a.out.(*syncBuffer).String(), "settings.changed") {
		if time.Now().After(deadline) {
			t.Fatalf("no settings event:\n%s", a.out.(*syncBuffer).String())
		}
		h.ok("config", "set", "models.stubs=true")
		time.Sleep(20 * time.Millisecond)
	}
	line := strings.SplitN(a.out.(*syncBuffer).String(), "\n", 2)[0]
	var ev Event
	if err := json.Unmarshal([]byte(line), &ev); err != nil || ev.Type != api.EvSettingsChanged {
		t.Fatalf("%v: %q", err, line)
	}
	_ = done // the stream ends with the test's core
}
