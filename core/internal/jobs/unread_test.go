package jobs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forja/internal/engineapi"
)

// reader is a model for a stage type that reads these views and is not installed.
func reader(id, task string, rank int, views ...string) engineapi.Model {
	m := model(id, task, rank)
	m.Status, m.Device, m.Fit = "not-installed", nil, "fits"
	m.Views = append([]string{"front"}, views...)
	return m
}

func shapePick(t *testing.T, r *Runner, preset string, project string, views []string, use string) Pick {
	t.Helper()
	all, err := r.Presets(project)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.Resolve(context.Background(), project, all[preset], views)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plan {
		if p.Use == use {
			return p
		}
	}
	t.Fatalf("no %s stage in %v", use, plan)
	return Pick{}
}

func TestAJobWithoutViewsHasNothingUnread(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{reader("shape-mv", "shape", 0, "left", "back", "right")}
	})
	p := shapePick(t, r, "draft", "", nil, "shape")
	if p.UnreadViews != nil || p.ReadBy != nil || p.Model != "stub-shape" {
		t.Fatalf("a job without views: %+v", p)
	}
}

func TestAModelThatReadsTheViewsLeavesNothingUnread(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	mv := model("shape-mv", "shape", 0)
	mv.Views = []string{"front", "left", "back", "right"}
	fp.set(func(f *fakeProvider) { f.extra = []engineapi.Model{mv} })
	p := shapePick(t, r, "draft", "", []string{"left", "back"}, "shape")
	if p.Model != "shape-mv" || p.UnreadViews != nil || p.ReadBy != nil {
		t.Fatalf("the reading model is ready: %+v", p)
	}
}

func TestViewsNoReadyModelReadsAreSaidAndWhoCouldReadThem(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{reader("shape-mv", "shape", 0, "left", "back", "right")}
	})
	p := shapePick(t, r, "draft", "", []string{"left", "back", "right"}, "shape")
	if p.Model != "stub-shape" {
		t.Fatalf("the pick changed: %+v", p)
	}
	if got := strings.Join(p.UnreadViews, ","); got != "left,back,right" {
		t.Fatalf("unread %q", got)
	}
	rb := p.ReadBy
	if rb == nil || rb.Model != "shape-mv" || rb.ModelName != "shape-mv" || rb.InstallOn != LocalProvider ||
		rb.ProviderLabel != "This computer" || rb.Installed || strings.Join(rb.Reads, ",") != "left,back,right" {
		t.Fatalf("read by %+v", rb)
	}

	// Only some of the views are in the job: the others are not unread.
	p = shapePick(t, r, "draft", "", []string{"back"}, "shape")
	if strings.Join(p.UnreadViews, ",") != "back" || p.ReadBy == nil || strings.Join(p.ReadBy.Reads, ",") != "back" {
		t.Fatalf("one view: %+v", p)
	}
}

func TestTheModelThatReadsTheMostViewsIsOffered(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	installed := reader("shape-installed", "shape", 1, "left", "back")
	installed.Status = "ready" // on the provider, but nothing places it
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{
			reader("shape-back", "shape", 90, "back"),
			installed,
			reader("shape-b", "shape", 5, "left"),
			reader("shape-a", "shape", 5, "left"),
			reader("paint-mv", "texture", 99, "left", "back", "right"),
		}
	})
	p := shapePick(t, r, "draft", "", []string{"left", "back", "right"}, "shape")
	if rb := p.ReadBy; rb == nil || rb.Model != "shape-installed" || !rb.Installed || strings.Join(rb.Reads, ",") != "left,back" {
		t.Fatalf("the most views, installed or not: %+v", p.ReadBy)
	}

	// Rank, then id, among models that read as many.
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{reader("shape-b", "shape", 5, "left"), reader("shape-a", "shape", 5, "left"), reader("shape-hi", "shape", 9, "left")}
	})
	if _, err := r.catalog.Ask(context.Background(), LocalProvider); err != nil {
		t.Fatal(err)
	}
	p = shapePick(t, r, "draft", "", []string{"left"}, "shape")
	if p.ReadBy == nil || p.ReadBy.Model != "shape-hi" {
		t.Fatalf("by rank: %+v", p.ReadBy)
	}
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{reader("shape-b", "shape", 5, "left"), reader("shape-a", "shape", 5, "left")}
	})
	if _, err := r.catalog.Ask(context.Background(), LocalProvider); err != nil {
		t.Fatal(err)
	}
	p = shapePick(t, r, "draft", "", []string{"left"}, "shape")
	if p.ReadBy == nil || p.ReadBy.Model != "shape-a" {
		t.Fatalf("by id: %+v", p.ReadBy)
	}
}

func TestAModelThatReadsNoMoreIsNotOffered(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	// The ready model reads the back view already; the other reads only that one too.
	back := model("shape-back", "shape", 50)
	back.Views = []string{"front", "back"}
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{back, reader("shape-back2", "shape", 99, "back")}
	})
	p := shapePick(t, r, "draft", "", []string{"left", "back"}, "shape")
	if p.Model != "shape-back" || strings.Join(p.UnreadViews, ",") != "left" || p.ReadBy != nil {
		t.Fatalf("%+v", p)
	}
}

func TestStubsTooBigAndViewHungryModelsAreNotOffered(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	stub := reader("shape-stub", "shape", 90, "left")
	stub.Stub = true
	big := reader("shape-big", "shape", 80, "left")
	big.Fit = "too-big"
	needs := reader("shape-needs", "shape", 70, "left", "back")
	needs.ViewsRequired = []string{"front", "back"}
	fp.set(func(f *fakeProvider) { f.extra = []engineapi.Model{stub, big, needs} })

	p := shapePick(t, r, "draft", "", []string{"left"}, "shape")
	if len(p.UnreadViews) != 1 || p.ReadBy != nil {
		t.Fatalf("stub, too big, and one that needs a back view the job lacks: %+v", p.ReadBy)
	}

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[models]\nstubs = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p = shapePick(t, r, "draft", project, []string{"left"}, "shape")
	if p.ReadBy == nil || p.ReadBy.Model != "shape-stub" {
		t.Fatalf("with stubs allowed: %+v", p.ReadBy)
	}
}

func TestATexturePainterThatIgnoresViewsIsSaidToo(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	fp.set(func(f *fakeProvider) { f.extra = []engineapi.Model{reader("paint-mv", "texture", 1, "back")} })
	p := shapePick(t, r, "mobile-prop", "", []string{"back"}, "texture")
	if p.Model != "stub-paint" || strings.Join(p.UnreadViews, ",") != "back" || p.ReadBy == nil || p.ReadBy.Model != "paint-mv" {
		t.Fatalf("texture: %+v", p)
	}
}

func TestBuiltInStagesHaveNoUnreadViews(t *testing.T) {
	r, _, _, _, _ := setup(t)
	all, _ := r.Presets("")
	plan, err := r.Resolve(context.Background(), "", all["mobile-prop"], []string{"left", "back", "right"})
	if err != nil {
		t.Fatal(err)
	}
	builtIns := 0
	for _, p := range plan {
		// Background removal is a model too, but each view has a copy of it; it never "ignores" one.
		if p.Model == "" || slices.Contains([]string{"background-removal", "framing", "mesh.cleanup", "export"}, p.Use) {
			builtIns++
			if p.UnreadViews != nil || p.ReadBy != nil {
				t.Fatalf("built-in %s: %+v", p.Use, p)
			}
		}
	}
	if builtIns == 0 {
		t.Fatal("the plan has no built-in stage")
	}
}

func TestAPinnedModelThatIgnoresViewsStillSaysSo(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	fp.set(func(f *fakeProvider) {
		f.extra = []engineapi.Model{model("shape-front", "shape", 1), reader("shape-mv", "shape", 0, "left")}
	})
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "forja.toml"), []byte("[models.stages]\nshape = \"shape-front\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := shapePick(t, r, "draft", project, []string{"left"}, "shape")
	if p.Model != "shape-front" || !p.Pinned || len(p.UnreadViews) != 1 || p.ReadBy == nil || p.ReadBy.Model != "shape-mv" {
		t.Fatalf("%+v", p)
	}
}
