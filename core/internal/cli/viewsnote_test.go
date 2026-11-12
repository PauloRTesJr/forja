package cli

import (
	"strings"
	"testing"

	"forja/internal/api"
	"forja/internal/jobs"
)

func TestADryRunWithViewsSaysWhatTheModelWillNotRead(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	// The model that is installed shapes from the front alone; one that reads the views is not installed.
	out := h.ok("generate", img, "--preset", "draft", "--dry-run", "--view", "left="+img, "--view", "back="+img, "--view", "right="+img)
	want := "Shape shapes from the front alone: the left, back and right views will not change the shape. " +
		"Shape-mv reads them (forja models install shape-mv)."
	if !strings.Contains(out, "\n"+want+"\n") {
		t.Fatalf("dry run lacks %q:\n%s", want, out)
	}
	out = h.ok("generate", img, "--preset", "draft", "--estimate", "--view", "back="+img)
	want = "Shape shapes from the front alone: the back view will not change the shape. Shape-mv reads it (forja models install shape-mv)."
	if !strings.Contains(out, want) {
		t.Fatalf("estimate lacks %q:\n%s", want, out)
	}
	// --json prints the API's own type.
	var plan api.PresetView
	h.okJSON(&plan, "generate", img, "--preset", "draft", "--dry-run", "--view", "back="+img, "--view", "left="+img)
	var shape jobs.Pick
	for _, p := range plan.Plan {
		if p.Use == "shape" {
			shape = p
		}
	}
	if strings.Join(shape.UnreadViews, ",") != "left,back" || shape.ReadBy == nil || shape.ReadBy.Model != "shape-mv" {
		t.Fatalf("shape: %+v", shape)
	}
	if out := h.ok("generate", img, "--preset", "draft", "--dry-run"); strings.Contains(out, "front alone") {
		t.Fatalf("a job without views:\n%s", out)
	}
	// Once the model that reads them is installed, nothing is left unsaid.
	h.eng.set(func(e *fakeEngine) { e.installed["shape-mv"] = true })
	if out := h.ok("generate", img, "--preset", "draft", "--dry-run", "--view", "back="+img); strings.Contains(out, "front alone") {
		t.Fatalf("with the reading model installed:\n%s", out)
	}
}

func TestTheNoteReadsNaturallyForOneTwoAndThreeViews(t *testing.T) {
	read := &jobs.ReadBy{Model: "mv", ModelName: "Hunyuan3D-2mv", InstallOn: "local", Reads: []string{"left", "back", "right"}}
	cases := []struct {
		name string
		pick jobs.Pick
		want string
	}{
		{"one", jobs.Pick{Use: "shape", ModelName: "TripoSG", UnreadViews: []string{"back"}},
			"TripoSG shapes from the front alone: the back view will not change the shape."},
		{"two", jobs.Pick{Use: "shape", ModelName: "TripoSG", UnreadViews: []string{"left", "right"}},
			"TripoSG shapes from the front alone: the left and right views will not change the shape."},
		{"three", jobs.Pick{Use: "shape", ModelName: "TripoSG", UnreadViews: []string{"left", "back", "right"}, ReadBy: read},
			"TripoSG shapes from the front alone: the left, back and right views will not change the shape. Hunyuan3D-2mv reads them (forja models install mv)."},
		{"a painter", jobs.Pick{Use: "texture", Model: "paint", UnreadViews: []string{"back"}},
			"paint paints from the front alone: the back view will not change the texture."},
		{"a model that reads some", jobs.Pick{Use: "shape", ModelName: "Duo", Views: []string{"front", "back"}, UnreadViews: []string{"left", "right"}},
			"Duo does not read the left and right views: they will not change the shape."},
		{"a reader that covers part, elsewhere", jobs.Pick{Use: "shape", ModelName: "TripoSG", UnreadViews: []string{"left", "back"},
			ReadBy: &jobs.ReadBy{Model: "mv", ModelName: "MV", InstallOn: "studio", Reads: []string{"back"}}},
			"TripoSG shapes from the front alone: the left and back views will not change the shape. MV reads the back view (forja models install mv --provider studio)."},
		{"installed but not ready", jobs.Pick{Use: "shape", ModelName: "TripoSG", UnreadViews: []string{"back"},
			ReadBy: &jobs.ReadBy{Model: "mv", ModelName: "MV", InstallOn: "local", ProviderLabel: "This computer", Installed: true, Reads: []string{"back"}}},
			"TripoSG shapes from the front alone: the back view will not change the shape. MV reads it, but cannot run on This computer yet."},
		{"nothing unread", jobs.Pick{Use: "shape", ModelName: "MV"}, ""},
	}
	for _, c := range cases {
		if got := unreadNote(c.pick); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}
