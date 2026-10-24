package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/library"
)

// withMultiView adds a shape model that reads side views, ranked below the fake's own one.
func withMultiView(fp *fakeProvider, more ...engineapi.Model) {
	mv := model("shape-mv", "shape", 0)
	mv.Views = []string{"front", "left", "back", "right"}
	fp.set(func(f *fakeProvider) { f.extra = append([]engineapi.Model{mv}, more...) })
}

func jobStage(j *library.Job, id string) library.Stage {
	for _, s := range j.Stages {
		if s.ID == id {
			return s
		}
	}
	return library.Stage{}
}

func TestSideViewsGoToTheModelThatReadsThem(t *testing.T) {
	r, fp, _, st, img := setup(t)
	withMultiView(fp)
	ctx := context.Background()
	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if got := stageModels(j)["shape"]; got != "stub-shape" {
		t.Fatalf("without views the shape stage got %q", got)
	}
	wait(t, r, j.ID)

	left, _ := st.PutBytes([]byte("left view"))
	back, _ := st.PutBytes([]byte("back view"))
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Views: map[string]string{"back": back, "left": left}})
	if err != nil {
		t.Fatal(err)
	}
	if got := stageIDs(j); got != "bg,frame,bg-left,frame-left,bg-back,frame-back,shape,cleanup,export" {
		t.Fatalf("stages %s", got)
	}
	if got := stageModels(j)["shape"]; got != "shape-mv" {
		t.Fatalf("with views the shape stage got %q", got)
	}
	done := wait(t, r, j.ID)
	if done.Status != library.Succeeded || done.Sheet != nil {
		t.Fatalf("%s %s, sheet %+v", done.Status, done.Error, done.Sheet)
	}
	// The front's framing is cached from the first job, so it arrives by hash.
	shape := fp.spec("shape")
	if shape.Inputs[0] != jobStage(done, "frame").Outputs[0].Hash ||
		strings.Join(shape.Inputs[1:], ",") != "stage:frame-left,stage:frame-back" ||
		strings.Join(shape.Views, ",") != "front,left,back" {
		t.Fatalf("shape got %v %v", shape.Inputs, shape.Views)
	}
	if got := fp.spec("bg-left").Inputs; len(got) != 1 || got[0] != left {
		t.Fatalf("bg-left read %v", got)
	}
	if !jobStage(done, "bg").Cached || !jobStage(done, "frame").Cached {
		t.Fatal("the front's preparation was not reused from the first job")
	}
	for _, bad := range []map[string]string{{"front": left}, {"top": left}, {"left": "blake3:" + strings.Repeat("9", 64)}} {
		if _, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Views: bad}); err == nil {
			t.Fatalf("views %v were accepted", bad)
		}
	}
}

const sheetFound = `{"sheet": true, "size": [1200, 1300], "parts": [
	{"box": [100, 40, 800, 780], "kind": "figure", "view": "front"},
	{"box": [860, 90, 1190, 530], "kind": "detail", "view": ""},
	{"box": [30, 820, 400, 1190], "kind": "figure", "view": ""},
	{"box": [540, 820, 690, 1190], "kind": "figure", "view": "left"},
	{"box": [800, 820, 1170, 1190], "kind": "figure", "view": "back"}]}`

// waiting returns the job once it waits for an answer and has left the active jobs.
func waiting(t *testing.T, r *Runner, id string) *library.Job {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		active := r.active[id] != nil
		r.mu.Unlock()
		if j, err := r.Job(context.Background(), id); err == nil && j.Status == library.Waiting && !active {
			return j
		}
	}
	t.Fatal("job did not wait")
	return nil
}

func TestASheetWaitsForTheUserAndSplitsAsTold(t *testing.T) {
	r, fp, _, _, img := setup(t)
	withMultiView(fp)
	fp.set(func(f *fakeProvider) { f.sheet = sheetFound })
	ctx := context.Background()
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetAsk})
	if err != nil {
		t.Fatal(err)
	}
	got := waiting(t, r, j.ID)
	sh := got.Sheet
	if !sh.Checked || !sh.Found || sh.Answer != "" || len(sh.Parts) != 5 || sh.Size != [2]int{1200, 1300} {
		t.Fatalf("sheet %+v", sh)
	}
	if bg := jobStage(got, "bg"); bg.Status != library.Succeeded || sh.Cutout != bg.Outputs[0].Hash {
		t.Fatalf("the cut-out is not the background removal's output: %+v", bg)
	}
	if jobStage(got, "shape").Status != library.Queued || r.Busy() {
		t.Fatal("a waiting job went on, or holds its slot")
	}

	front := library.SheetPart{View: "front", Box: [4]int{100, 40, 800, 780}}
	back := library.SheetPart{View: "back", Box: [4]int{800, 820, 1170, 1190}}
	for _, bad := range [][]library.SheetPart{
		{back},
		{front, front},
		{front, {View: "left", Box: [4]int{0, 0, 2000, 10}}},
	} {
		if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Views: bad}); err == nil {
			t.Fatalf("answer %v was accepted", bad)
		}
	}
	// The user leaves the side view out.
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Views: []library.SheetPart{front, back}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Keep: true}); err == nil {
		t.Fatal("a job was answered twice")
	}
	done := wait(t, r, j.ID)
	if done.Status != library.Succeeded {
		t.Fatalf("%s: %s", done.Status, done.Error)
	}
	if done.Views["front"] == "" || done.Views["back"] == "" || len(done.Views) != 2 || done.Input != img {
		t.Fatalf("views %v, input %s", done.Views, done.Input)
	}
	if got := stageIDs(done); got != "bg,frame,bg-back,frame-back,shape,cleanup,export" {
		t.Fatalf("stages %s", got)
	}
	// The sheet and its cut-out, made while looking for the views.
	split := fp.spec("split")
	if strings.Join(split.Inputs, ",") != img+","+sh.Cutout || fmt.Sprint(split.Params["views"]) !=
		"[map[box:[100 40 800 780] view:front] map[box:[800 820 1170 1190] view:back]]" {
		t.Fatalf("split %v %v", split.Inputs, split.Params)
	}
	if got := fp.spec("bg").Inputs; got[0] != done.Views["front"] {
		t.Fatalf("the front's background removal read %v, not the cut-out front", got)
	}
	if stageModels(done)["shape"] != "shape-mv" || strings.Join(fp.spec("shape").Views, ",") != "front,back" {
		t.Fatalf("shape %v", fp.spec("shape"))
	}
}

func TestASheetIsKeptWholeOrSplitAsProposedWhenTheJobSaysSo(t *testing.T) {
	r, fp, _, _, img := setup(t)
	withMultiView(fp)
	fp.set(func(f *fakeProvider) { f.sheet = sheetFound })
	ctx := context.Background()

	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	kept := wait(t, r, j.ID)
	if kept.Status != library.Succeeded || !kept.Sheet.Found || kept.Sheet.Answer != library.SheetKeep || len(kept.Views) != 0 {
		t.Fatalf("kept %s %+v %v", kept.Status, kept.Sheet, kept.Views)
	}
	if bg := jobStage(kept, "bg"); bg.Cached {
		t.Fatal("background removal ran while looking for a sheet, yet shows as cached")
	}

	j, _ = r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetSplit})
	split := wait(t, r, j.ID)
	if split.Status != library.Succeeded || len(split.Views) != 3 || split.Sheet.Answer != library.SheetSplit {
		t.Fatalf("split %s %s %v", split.Status, split.Error, split.Views)
	}
	if got := stageIDs(split); got != "bg,frame,bg-left,frame-left,bg-back,frame-back,shape,cleanup,export" {
		t.Fatalf("stages %s", got)
	}

	// The same sheet split the same way again computes nothing.
	n := fp.count()
	j, _ = r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetSplit})
	if again := wait(t, r, j.ID); again.Status != library.Succeeded || fp.executed(n) != "" {
		t.Fatalf("again %s %s, ran %q", again.Status, again.Error, fp.executed(n))
	}
}

func TestAWaitingJobCanBeCancelled(t *testing.T) {
	r, fp, _, _, img := setup(t)
	fp.set(func(f *fakeProvider) { f.sheet = sheetFound })
	ctx := context.Background()
	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Sheet: library.SheetAsk})
	waiting(t, r, j.ID)
	if !r.Cancel(j.ID) {
		t.Fatal("cancel refused")
	}
	got, _ := r.Job(ctx, j.ID)
	if got.Status != library.Cancelled || jobStage(got, "shape").Status != library.Cancelled {
		t.Fatalf("%s %+v", got.Status, got.Stages)
	}
	if _, err := r.AnswerSheet(ctx, j.ID, SheetAnswer{Keep: true}); err == nil || r.Cancel(j.ID) {
		t.Fatal("a cancelled job was answered or cancelled again")
	}
}

func TestAModelThatNeedsViewsRunsOnlyWithThem(t *testing.T) {
	r, fp, _, st, img := setup(t)
	needs := model("needs-back", "shape", 99)
	needs.Views, needs.ViewsRequired = []string{"front", "back"}, []string{"front", "back"}
	withMultiView(fp, needs)
	ctx := context.Background()
	j, _ := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft"})
	if got := stageModels(j)["shape"]; got != "stub-shape" {
		t.Fatalf("without a back view the shape stage got %q", got)
	}
	back, _ := st.PutBytes([]byte("back view"))
	j, _ = r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Views: map[string]string{"back": back}})
	if got := stageModels(j)["shape"]; got != "needs-back" {
		t.Fatalf("with a back view the shape stage got %q", got)
	}
	_, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Overrides: map[string]any{"shape.model": "needs-back"}})
	if err == nil || !strings.Contains(err.Error(), "needs the back view") {
		t.Fatalf("pinned without its view: %v", err)
	}
	all, _ := r.Presets("")
	plan, _ := r.Resolve(ctx, "", all["draft"], []string{"left"})
	for _, p := range plan {
		if p.Stage == "shape" && (p.Model != "shape-mv" || strings.Join(p.Views, ",") != "front,left,back,right") {
			t.Fatalf("resolving with a left view: %+v", p)
		}
	}
}

func TestASheetIsFoundWhileEveryJobSlotIsBusy(t *testing.T) {
	r, fp, _, _, img := setup(t)
	hold := make(chan struct{})
	fp.set(func(f *fakeProvider) { f.sheet, f.hold = sheetFound, hold })
	defer close(hold)
	ctx := context.Background()
	// Kept whole, so they go on to build shapes and hold every slot (the test runner has 4).
	for seed := 1; seed <= 4; seed++ {
		if _, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed}); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		fp.mu.Lock()
		held := fp.held
		fp.mu.Unlock()
		if held == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d jobs hold a slot", held)
		}
	}
	seed := 5
	j, err := r.Submit(ctx, SubmitRequest{Input: img, Preset: "draft", Seed: &seed, Sheet: library.SheetAsk})
	if err != nil {
		t.Fatal(err)
	}
	if got := waiting(t, r, j.ID); !got.Sheet.Found {
		t.Fatalf("sheet %+v", got.Sheet)
	}
}
