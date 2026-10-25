package jobs

import (
	"context"
	"math"
	"strings"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/provider"
)

func TestAnEstimateAddsUpMeasuredTimeAndPricedCost(t *testing.T) {
	r, fp, _, _, _ := setup(t)
	withHub(t, r, fp, 0.4)
	ctx := context.Background()
	_ = r.db.RecordTiming(ctx, "background-removal", "stub-background", LocalProvider, 2)
	_ = r.db.RecordTiming(ctx, "framing", "", LocalProvider, 1)
	// The hosted model never ran: its time is unknown, its price is not.
	project := withProject(t, "[models.stages]\nshape = \"oneshot\"\n[routing.stages]\nshape = \"hub\"\n")
	est, err := r.Estimate(ctx, EstimateRequest{Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if est.Seconds != 3 || strings.Join(est.Unmeasured, ",") != "shape,cleanup,export" || est.Problem != "" {
		t.Fatalf("time %v, unmeasured %v, problem %q", est.Seconds, est.Unmeasured, est.Problem)
	}
	if est.CostUSD != 0.4 || !est.Paid || len(est.Unpriced) != 0 {
		t.Fatalf("cost %v paid %v unpriced %v", est.CostUSD, est.Paid, est.Unpriced)
	}
	for _, s := range est.Stages {
		if s.Stage == "shape" && (s.Provider != "hub" || s.CostUSD != 0.4 || s.Measured != "") {
			t.Fatalf("shape %+v", s)
		}
		if s.Stage == "bg" && (s.Seconds != 2 || s.Measured != "here" || s.ProviderLabel != "This computer") {
			t.Fatalf("bg %+v", s)
		}
	}
	// Three images at once against a daily limit two of them fit in.
	limited := withProject(t, "[models.stages]\nshape = \"oneshot\"\n[routing.stages]\nshape = \"hub\"\n[routing.budget_usd]\nper_day = 1.0\n")
	est, err = r.Estimate(ctx, EstimateRequest{Preset: "draft", Project: limited, Images: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(est.Problem, "these 3 images would bring spending to $1.20") || est.Spend.Limits.PerDay != 1 {
		t.Fatalf("problem %q spend %+v", est.Problem, est.Spend)
	}
}

func TestAnEstimatePricesTimeOnAProviderBilledByTheSecond(t *testing.T) {
	r, _, _, st, _ := setup(t)
	cloud := newEngine(st, "cloud", provider.KindServerless, true)
	r.providers.Add(cloud)
	ctx := context.Background()
	withUser(t, rented)
	project := withProject(t, "[routing.stages]\nshape = \"cloud\"\n")
	est, err := r.Estimate(ctx, EstimateRequest{Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	// Never ran anywhere: billed by the second for a time nobody knows yet.
	if est.CostUSD != 0 || len(est.Unpriced) != 0 {
		t.Fatalf("cost %v unpriced %v", est.CostUSD, est.Unpriced)
	}
	// Measured on this computer: the rented GPU is priced for that long at RunPod's default rate.
	_ = r.db.RecordTiming(ctx, "shape", "stub-shape", LocalProvider, 100)
	est, _ = r.Estimate(ctx, EstimateRequest{Preset: "draft", Project: project})
	for _, s := range est.Stages {
		if s.Stage == "shape" && (s.Measured != "elsewhere" || math.Abs(s.CostUSD-0.031) > 1e-9) {
			t.Fatalf("shape %+v", s)
		}
	}
}

func TestARentedEngineStampingItsRateOnItsModelsIsNotChargedTwice(t *testing.T) {
	r, _, _, st, _ := setup(t)
	cloud := newEngine(st, "cloud", provider.KindServerless, true)
	cloud.with(func(e *engine) {
		e.shape.Price = &engineapi.Price{Amount: 0.00031, Unit: "second", Currency: "USD"}
	})
	r.providers.Add(cloud)
	ctx := context.Background()
	withUser(t, rented)
	project := withProject(t, "[routing.stages]\nshape = \"cloud\"\n")
	_ = r.db.RecordTiming(ctx, "shape", "stub-shape", LocalProvider, 100)
	est, err := r.Estimate(ctx, EstimateRequest{Preset: "draft", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range est.Stages {
		if s.Stage == "shape" && math.Abs(s.CostUSD-0.031) > 1e-9 {
			t.Fatalf("100 s at $0.00031/s should cost $0.031, got %+v", s)
		}
	}
}
