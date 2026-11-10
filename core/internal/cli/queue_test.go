package cli

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"forja/internal/api"
	"forja/internal/ipc"
	"forja/internal/library"
)

// status sends a request with no body to the harness's core and returns the status it answers.
func (h *harness) status(method, path string) int {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, ipc.BaseURL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	resp, err := ipc.HTTPClient(h.addr).Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) jobs() []*library.Job {
	h.t.Helper()
	var list []*library.Job
	h.okJSON(&list, "jobs", "ls")
	return list
}

func TestAFailedJobIsRetriedAndFinishedJobsAreCleared(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	h.eng.set(func(e *fakeEngine) { e.refuse = "the shape model is still loading" })
	if r := h.cli("", "generate", img, "--preset", "draft", "--seed", "7"); r.code != exitJobFailed {
		t.Fatalf("generate with an engine that refuses: exit %d\n%s%s", r.code, r.out, r.err)
	}
	failed := h.jobs()
	if len(failed) != 1 || failed[0].Status != library.Failed ||
		!strings.Contains(failed[0].Error, "This computer: the shape model is still loading") {
		t.Fatalf("jobs %+v", failed)
	}

	h.eng.set(func(e *fakeEngine) { e.refuse = "" })
	out := h.ok("jobs", "retry", failed[0].ID)
	if !strings.Contains(out, "✓ chest (draft)") {
		t.Fatalf("jobs retry printed:\n%s", out)
	}
	after := h.jobs()
	if len(after) != 1 || after[0].ID == failed[0].ID || after[0].RetryOf != failed[0].ID || after[0].Status != library.Succeeded ||
		after[0].Seed != 7 || after[0].AssetID == "" || after[0].StartedAt == nil || after[0].DurationSec <= 0 {
		t.Fatalf("after the retry: %+v", after)
	}
	made := after[0]

	// What the routes answer, for both clients.
	for path, want := range map[string]int{
		"/v1/jobs/" + failed[0].ID + "/retry": 404, // the retried job is gone
		"/v1/jobs/" + made.ID + "/retry":      409, // it succeeded: nothing to retry
		"/v1/jobs/nope/remove":                404,
	} {
		if got := h.status("POST", path); got != want {
			t.Fatalf("POST %s: %d, want %d", path, got, want)
		}
	}
	if r := h.cli("", "jobs", "retry", made.ID); r.code == 0 || !strings.Contains(r.err, "only a failed or cancelled job can be retried") {
		t.Fatalf("retrying a job that succeeded: exit %d, %s", r.code, r.err)
	}

	// Off the list, while the asset stays and still finds its job.
	h.ok("jobs", "rm", made.ID)
	if left := h.jobs(); len(left) != 0 {
		t.Fatalf("%d jobs left after jobs rm", len(left))
	}
	var hidden library.Job
	h.okJSON(&hidden, "jobs", "show", made.ID)
	if !hidden.Hidden || len(hidden.Stages) == 0 {
		t.Fatalf("the job behind the asset: %+v", hidden)
	}
	var assets []*library.Asset
	h.okJSON(&assets, "assets", "ls")
	if len(assets) != 1 || assets[0].ID != made.AssetID {
		t.Fatalf("assets after jobs rm: %+v", assets)
	}

	h.ok("generate", h.image("barrel.png"), "--preset", "draft")
	h.ok("generate", h.image("crate.png"), "--preset", "draft")
	if out := h.ok("jobs", "clear"); !strings.Contains(out, "Cleared 2 finished jobs.") {
		t.Fatalf("jobs clear printed: %s", out)
	}
	var cleared api.Cleared
	h.okJSON(&cleared, "jobs", "clear")
	if cleared.Removed == nil || len(cleared.Removed) != 0 || len(h.jobs()) != 0 {
		t.Fatalf("a second clear: %+v", cleared)
	}
	h.okJSON(&assets, "assets", "ls")
	if len(assets) != 3 {
		t.Fatalf("%d assets after clearing the jobs, want all 3", len(assets))
	}
}

func TestOneUnreadableImageQueuesNothing(t *testing.T) {
	h := newHarness(t)
	ok1, ok2 := h.image("chest.png"), h.image("barrel.png")
	notes := filepath.Join(h.dir, "notes.png")
	if err := os.WriteFile(notes, []byte("shopping list\nmilk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(h.dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for bad, want := range map[string]string{
		filepath.Join(h.dir, "gone.png"): "gone.png",
		notes:                            "notes.png is not an image (it looks like text/plain); use a PNG, JPEG or WebP file",
		empty:                            "empty.png is empty (0 bytes)",
	} {
		r := h.cli("", "generate", ok1, bad, ok2, "--preset", "draft")
		if r.code == 0 || !strings.Contains(r.err, want) {
			t.Fatalf("generate with %s: exit %d\n%s", filepath.Base(bad), r.code, r.err)
		}
		if list := h.jobs(); len(list) != 0 {
			t.Fatalf("%d jobs were queued although %s was refused", len(list), filepath.Base(bad))
		}
	}
}

// A dropped folder was refused although the drop target promises "a folder of images".
func TestAFolderOfImagesBecomesOneJobPerImage(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(h.dir, "pack")
	if err := os.MkdirAll(filepath.Join(dir, "more"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.png", "A.PNG", "c.jpg", "notes.txt", filepath.Join("more", "deep.png")} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("\x89PNG\r\n\x1a\nfake image "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h.ok("generate", dir, "--preset", "draft", "--no-wait")
	var names []string
	for _, j := range h.jobs() {
		names = append(names, j.Name)
	}
	slices.Sort(names)
	if strings.Join(names, ",") != "A,b,c" {
		t.Fatalf("jobs for %v, want A, b and c: the folder's own images and nothing under it", names)
	}

	empty := filepath.Join(h.dir, "empty-pack")
	os.MkdirAll(empty, 0o755)
	r := h.cli("", "generate", empty, "--preset", "draft", "--no-wait")
	if r.code == 0 || !strings.Contains(r.err, "the folder "+empty+" has no PNG, JPEG or WebP image directly inside it") {
		t.Fatalf("an empty folder: exit %d\n%s", r.code, r.err)
	}
}
