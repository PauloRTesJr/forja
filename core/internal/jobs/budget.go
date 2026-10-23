package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/library"
)

// perGeneration reports whether a price is charged once per call, so it can be added up
// before a job runs; per-second or per-megapixel prices cannot.
func perGeneration(p *engineapi.Price) bool {
	if p == nil {
		return false
	}
	unit := strings.ToLower(p.Unit)
	for _, variable := range []string{"second", "minute", "megapixel", "token", "step"} {
		if strings.Contains(unit, variable) {
			return false
		}
	}
	return true
}

// Estimate is what the paid stages of a job will cost, in USD, and the models whose price is
// not known in advance.
func Estimate(picks []Pick) (usd float64, unknown []string) {
	for _, p := range picks {
		if p.Skipped || !p.Paid {
			continue
		}
		if !perGeneration(p.Price) {
			name := p.ModelName
			if name == "" {
				name = p.Model
			}
			unknown = append(unknown, name)
			continue
		}
		usd += p.Price.Amount
	}
	return usd, unknown
}

// withinBudget refuses a job whose paid stages would pass a spending limit in
// `[routing.budget_usd]`; a limit of 0 means none.
func (r *Runner) withinBudget(ctx context.Context, cfg *config.Loaded, picks []Pick) error {
	b := cfg.Routing.Budget
	cost, unknown := Estimate(picks)
	if cost == 0 && len(unknown) == 0 {
		return nil
	}
	limited := b.PerJob > 0 || b.PerDay > 0 || b.PerMonth > 0
	if limited && len(unknown) > 0 {
		return fmt.Errorf("%s has no fixed price per generation, so the spending limits cannot be checked; "+
			"raise them to 0 to allow it", strings.Join(unknown, ", "))
	}
	if b.PerJob > 0 && cost > b.PerJob {
		return fmt.Errorf("this image would cost about $%.2f, over the limit of $%.2f per image", cost, b.PerJob)
	}
	now := time.Now()
	for _, w := range []struct {
		limit float64
		since time.Time
		name  string
	}{
		{b.PerDay, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), "daily"},
		{b.PerMonth, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), "monthly"},
	} {
		if w.limit <= 0 {
			continue
		}
		spent, err := r.db.Spent(ctx, w.since)
		if err != nil {
			return err
		}
		if spent+cost > w.limit {
			return fmt.Errorf("this image would bring spending to $%.2f, over the %s limit of $%.2f",
				spent+cost, w.name, w.limit)
		}
	}
	return nil
}

// recordCost remembers what a paid stage cost, for the daily and monthly limits.
func (r *Runner) recordCost(ctx context.Context, jobID, stage, providerID, model string) {
	e, ok := r.catalog.Last(providerID)
	if !ok {
		return
	}
	m, ok := e.Model(model)
	if !ok || !perGeneration(m.Price) {
		return
	}
	cost := library.Cost{Job: jobID, Stage: stage, Provider: providerID, Model: model,
		Amount: m.Price.Amount, Currency: m.Price.Currency}
	if err := r.db.RecordCost(ctx, cost); err != nil {
		r.log.Warn("recording a cost", "job", jobID, "err", err)
	}
}
