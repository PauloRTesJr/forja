package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
)

// EstimateRequest is a job as it would be submitted, to estimate before queueing it.
type EstimateRequest struct {
	Preset  string `json:"preset,omitempty"`
	Project string `json:"project,omitempty"`
	// Views are the other views of the subject the job would bring: "left", "back", "right".
	Views     []string       `json:"views,omitempty"`
	Overrides map[string]any `json:"overrides,omitempty"`
	// Images is how many images would be queued this way; 1 when unset.
	Images int `json:"images,omitempty"`
}

// StageEstimate is one stage of an estimate: where it would run, how long it should take there
// and what it should cost.
type StageEstimate struct {
	Pick
	Seconds float64 `json:"seconds,omitempty"`
	// Measured says where Seconds comes from: "here" (earlier runs on this provider),
	// "elsewhere" (on other providers), or "" when the stage never ran.
	Measured string  `json:"measured,omitempty"`
	CostUSD  float64 `json:"cost_usd,omitempty"`
	// Priced is false for a paid stage that has no usable price.
	Priced bool `json:"priced"`
}

// Spend is what was spent today and this month, and what queued and running jobs are expected
// to spend, next to the limits.
type Spend struct {
	Limits config.Budget `json:"limits"`
	Today  float64       `json:"today"`
	Month  float64       `json:"month"`
	Queued float64       `json:"queued"`
}

// Estimate is what a job would take and cost, before it is queued (spec section 8): time from
// the stages' measured history, cost from the price table and the prices hosted models report,
// against the spending limits.
type Estimate struct {
	Preset string          `json:"preset"`
	Images int             `json:"images"`
	Stages []StageEstimate `json:"stages"`
	// Seconds and CostUSD are for one image. Unmeasured names the stages Seconds leaves out,
	// Unpriced the paid ones CostUSD leaves out.
	Seconds    float64  `json:"seconds"`
	CostUSD    float64  `json:"cost_usd"`
	Unmeasured []string `json:"unmeasured"`
	Unpriced   []string `json:"unpriced"`
	// Paid is true when some stage runs on a paid provider.
	Paid  bool  `json:"paid"`
	Spend Spend `json:"spend"`
	// Problem says why the job would be refused now: no model for a stage, a spending limit.
	Problem string `json:"problem,omitempty"`
}

// Estimate says how a job would run now, how long it should take and what it should cost.
func (r *Runner) Estimate(ctx context.Context, req EstimateRequest) (*Estimate, error) {
	project := req.Project
	if project != "" {
		project, _ = filepath.Abs(project)
	}
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	all, err := r.Presets(project)
	if err != nil {
		return nil, err
	}
	id := req.Preset
	if id == "" {
		id = cfg.Project.DefaultPreset
	}
	base, ok := all[id]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", id)
	}
	overrides := map[string]any{}
	if po, ok := cfg.Presets[id]; ok {
		for k, v := range po.Overrides {
			overrides[k] = v
		}
	}
	for k, v := range req.Overrides {
		overrides[k] = v
	}
	preset, err := base.WithOverrides(overrides)
	if err != nil {
		return nil, err
	}
	for _, v := range req.Views {
		if v == "front" || !slices.Contains(engineapi.Views, v) {
			return nil, fmt.Errorf("view %q: want left, back or right", v)
		}
	}
	picks, _, err := r.resolve(ctx, cfg, preset.Active(), ordered(req.Views), nil)
	if err != nil {
		return nil, err
	}
	est := &Estimate{Preset: id, Images: max(1, req.Images), Stages: []StageEstimate{}, Unmeasured: []string{}, Unpriced: []string{}}
	for _, p := range picks {
		se := StageEstimate{Pick: p, Priced: true}
		if !p.Skipped && !p.Missing {
			se.Seconds, se.Measured = r.expect(ctx, p.Use, p.Model, p.Provider)
			if se.Seconds > 0 {
				est.Seconds += se.Seconds
			} else {
				est.Unmeasured = append(est.Unmeasured, p.Stage)
			}
			c := r.chargeOf(ctx, cfg, p)
			se.CostUSD, se.Priced = c.USD(), !c.Unpriced
			est.CostUSD += se.CostUSD
			if c.Unpriced {
				est.Unpriced = append(est.Unpriced, nameOf(p))
			}
			est.Paid = est.Paid || p.Paid
		}
		est.Stages = append(est.Stages, se)
	}
	now := time.Now()
	est.Spend = Spend{Limits: cfg.Routing.Budget, Queued: r.committed("")}
	est.Spend.Today, _ = r.db.Spent(ctx, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()))
	est.Spend.Month, _ = r.db.Spent(ctx, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
	switch err := missingError(picks); {
	case err != nil:
		est.Problem = err.Error()
	default:
		for _, p := range picks {
			if len(p.NeedsViews) > 0 && est.Problem == "" {
				est.Problem = fmt.Sprintf("%s needs the %s view", orName(p), strings.Join(p.NeedsViews, " and "))
			}
		}
		if est.Problem == "" {
			if err := r.budgetProblem(ctx, cfg.Routing.Budget, est.CostUSD, est.Unpriced, "", est.Images); err != nil {
				est.Problem = err.Error()
			}
		}
	}
	return est, nil
}

func orName(p Pick) string {
	if p.ModelName != "" {
		return p.ModelName
	}
	return p.Model
}
