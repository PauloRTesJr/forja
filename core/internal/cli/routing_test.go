package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"forja/internal/api"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/provider"
)

func TestAnEstimateIsPrintedAndNothingIsQueued(t *testing.T) {
	h := newHarness(t)
	img := h.image("chest.png")
	h.ok("generate", img, "--preset", "draft") // so the stages have a measured time
	out := h.ok("generate", img, h.image("crate.png"), "--preset", "draft", "--estimate")
	for _, want := range []string{"STAGE", "RUNS ON", "This computer", "free", "Each image: about", "2 images: about"} {
		if !strings.Contains(out, want) {
			t.Fatalf("estimate lacks %q:\n%s", want, out)
		}
	}
	var est jobs.Estimate
	h.okJSON(&est, "generate", img, "--preset", "draft", "--estimate")
	if est.Preset != "draft" || est.Images != 1 || est.Seconds <= 0 || est.Paid || est.Problem != "" {
		t.Fatalf("estimate %+v", est)
	}
	var list []*library.Job
	h.okJSON(&list, "jobs", "ls")
	if len(list) != 1 {
		t.Fatalf("an estimate queued jobs: %d", len(list))
	}

	// A hosted model over the limit per image: the estimate says the job would be refused.
	fakeFal(t)
	if r := h.cli("fal-test\n", "hubs", "connect", "fal"); r.code != 0 {
		t.Fatalf("connect: %s", r.err)
	}
	h.ok("models", "pin", "shape", "fal-ai/trellis-2")
	h.ok("config", "set", "routing.budget_usd.per_job=0.2")
	r := h.cli("", "generate", img, "--preset", "draft", "--estimate")
	if r.code != 1 || !strings.Contains(r.out, "$0.30") || !strings.Contains(r.out, "Would be refused") {
		t.Fatalf("exit %d:\n%s", r.code, r.out)
	}
}

func TestThePriceTableIsChangedFromTheCommandLine(t *testing.T) {
	fakeFal(t)
	h := newHarness(t)
	out := h.ok("prices", "ls")
	if !strings.Contains(out, "runpod-serverless-4090") || !strings.Contains(out, "$0.00031 per second") {
		t.Fatalf("prices ls:\n%s", out)
	}
	h.ok("prices", "set", "runpod-serverless-4090", "0.0004", "--per", "second")
	var view api.PricesView
	h.okJSON(&view, "prices", "ls")
	for _, p := range view.Prices {
		if p.ID == "runpod-serverless-4090" && (p.USD != 0.0004 || p.Source != "user" || p.Default == nil) {
			t.Fatalf("after set %+v", p)
		}
	}
	if r := h.cli("", "prices", "set", "x", "1", "--per", "minute"); r.code != 1 || !strings.Contains(r.err, "per: want") {
		t.Fatalf("a price per minute: exit %d: %s", r.code, r.err)
	}
	h.ok("prices", "rm", "runpod-serverless-4090")
	if r := h.cli("", "prices", "rm", "runpod-serverless-4090"); r.code != 1 || !strings.Contains(r.err, "built-in") {
		t.Fatalf("removing a built-in price: exit %d: %s", r.code, r.err)
	}

	// A hosted model's own price, replaced and given back.
	if r := h.cli("fal-test\n", "hubs", "connect", "fal"); r.code != 0 {
		t.Fatalf("connect: %s", r.err)
	}
	h.ok("models", "ls", "--all")
	h.ok("prices", "set", "fal-ai/trellis-2", "0.45")
	if cfg, _ := config.Load("", nil); cfg.Prices["fal-ai/trellis-2"].USD != 0.45 {
		t.Fatalf("prices %+v", cfg.Prices)
	}
	if out := h.ok("prices", "ls"); !strings.Contains(out, "$0.45 per image") {
		t.Fatalf("prices ls:\n%s", out)
	}
	h.ok("prices", "rm", "fal-ai/trellis-2")
	if out := h.ok("prices", "ls"); !strings.Contains(out, "vendor") {
		t.Fatalf("prices ls:\n%s", out)
	}
}

// offline is another computer that answers what it runs but no longer takes runs.
type offline struct{ *fakeEngine }

func (o offline) ID() string          { return "lan" }
func (o offline) Kind() provider.Kind { return provider.KindEngine }
func (o offline) Run(context.Context, engineapi.RunRequest, chan<- engineapi.Event) (*engineapi.Run, error) {
	return nil, errors.New("dial tcp 192.0.2.10:7861: connection refused")
}

func TestAFallbackIsPrintedWhileAJobRuns(t *testing.T) {
	h := newHarness(t)
	h.reg.Add(offline{h.eng})
	raw, _ := os.ReadFile(config.UserFile())
	raw = append(raw, "[routing]\npriority = [\"lan\", \"local\"]\n\n[[providers]]\nid = \"lan\"\nkind = \"engine\"\nlabel = \"Studio PC\"\n"...)
	if err := os.WriteFile(config.UserFile(), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	out := h.ok("generate", h.image("chest.png"), "--preset", "draft")
	if !strings.Contains(out, "moves to This computer: Studio PC could not be reached") || !strings.Contains(out, "✓ chest") {
		t.Fatalf("generate:\n%s", out)
	}
	var list []*library.Job
	h.okJSON(&list, "jobs", "ls")
	if out := h.ok("jobs", "show", list[0].ID); !strings.Contains(out, "shape moved from lan to local: it could not be reached") {
		t.Fatalf("jobs show:\n%s", out)
	}
}
