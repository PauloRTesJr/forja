package jobs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/library"
	"forja/internal/planner"
	"forja/internal/provider"
)

// cent absorbs float rounding when a cost is compared with a limit: $0.40 twice is not over $0.80.
const cent = 1e-9

// charge is what one stage is expected to cost where it runs.
type charge struct {
	// PerCall is owed once the stage runs: a hosted model's price per generation.
	PerCall float64
	// Rate is what each second of the stage costs: the provider's time, plus the model's when
	// the price table prices it by time.
	Rate float64
	// Overhead is the provider's own rate, for the time a batch spends outside its stages.
	Overhead float64
	// Seconds is how long the stage is expected to run; 0 when it never ran.
	Seconds float64
	// Unpriced is a paid stage with nothing to add up: a model priced in a vendor's compute unit
	// that the price table does not price, or a paid provider whose time has no price.
	Unpriced bool
}

func (c charge) USD() float64 { return c.PerCall + c.Rate*c.Seconds }

// Known is true when USD is all the stage is expected to cost.
func (c charge) Known() bool { return !c.Unpriced && (c.Rate == 0 || c.Seconds > 0) }

// chargeOf is what a pick is expected to cost: nothing on a free provider. Prices come from the
// price table (spec section 8), where an entry named after a model replaces the price its
// provider reports (describe applies it), and the provider's time is priced by its rate.
func (r *Runner) chargeOf(ctx context.Context, cfg *config.Loaded, p Pick) charge {
	var c charge
	if !p.Paid || p.Skipped {
		return c
	}
	c.Overhead, _ = cfg.Rate(p.Provider)
	c.Rate = c.Overhead
	switch {
	case p.Price == nil:
		c.Unpriced = c.Rate == 0
	case p.Price.Unit == engineapi.PerCall:
		c.PerCall = p.Price.Amount
	case p.Price.Unit == config.PerSecond || p.Price.Unit == config.PerHour:
		c.Rate += config.Price{USD: p.Price.Amount, Per: p.Price.Unit}.PerSecondUSD()
	default:
		c.Unpriced = true
	}
	if c.Rate > 0 {
		c.Seconds, _ = r.expect(ctx, p.Use, p.Model, p.Provider)
	}
	return c
}

// expected adds up what picks are expected to cost: the known part, and the names of the paid
// stages with no usable price.
func (r *Runner) expected(ctx context.Context, cfg *config.Loaded, picks []Pick) (usd float64, unpriced []string) {
	for _, p := range picks {
		c := r.chargeOf(ctx, cfg, p)
		if c.Unpriced {
			unpriced = append(unpriced, nameOf(p))
			continue
		}
		usd += c.USD()
	}
	return usd, unpriced
}

func nameOf(p Pick) string {
	switch {
	case p.ModelName != "":
		return p.ModelName
	case p.Model != "":
		return p.Model
	}
	return p.Stage + " on " + p.ProviderLabel
}

type window struct {
	limit float64
	since time.Time
	name  string
}

// windows are the daily and monthly limits that are set, from now.
func windows(b config.Budget, now time.Time) []window {
	var out []window
	for _, w := range []window{
		{b.PerDay, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()), "daily"},
		{b.PerMonth, time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()), "monthly"},
	} {
		if w.limit > 0 {
			out = append(out, w)
		}
	}
	return out
}

func limited(b config.Budget) bool { return b.PerJob > 0 || b.PerDay > 0 || b.PerMonth > 0 }

// withinBudget refuses a job whose paid stages would pass a spending limit in
// `[routing.budget_usd]`, counting what the other queued and running jobs are expected to
// spend; a limit of 0 means none. Time billed by the second is metered again while it runs, so
// a stage with no measured time yet passes here and stops at the limit.
func (r *Runner) withinBudget(ctx context.Context, cfg *config.Loaded, picks []Pick, job string) error {
	cost, unpriced := r.expected(ctx, cfg, picks)
	return r.budgetProblem(ctx, cfg.Routing.Budget, cost, unpriced, job, 1)
}

// budgetProblem says why images costing cost each would pass a limit, counting what jobs in
// flight other than job are expected to spend; nil when they fit.
func (r *Runner) budgetProblem(ctx context.Context, b config.Budget, cost float64, unpriced []string, job string, images int) error {
	if (cost == 0 && len(unpriced) == 0) || !limited(b) {
		return nil
	}
	if len(unpriced) > 0 {
		return fmt.Errorf("%s has no known price, so the spending limits cannot be checked; "+
			"give it one in the price table (Settings, or forja prices set) or raise the limits to 0", strings.Join(unpriced, ", "))
	}
	if b.PerJob > 0 && cost > b.PerJob+cent {
		return fmt.Errorf("this image would cost about $%.2f, over the limit of $%.2f per image", cost, b.PerJob)
	}
	committed := r.committed(job)
	what := "this image"
	if images > 1 {
		what = fmt.Sprintf("these %d images", images)
	}
	for _, w := range windows(b, time.Now()) {
		spent, err := r.db.Spent(ctx, w.since)
		if err != nil {
			return err
		}
		if total := spent + committed + cost*float64(images); total > w.limit+cent {
			queued := ""
			if committed > 0 {
				queued = fmt.Sprintf(" with $%.2f for the jobs already queued", committed)
			}
			return fmt.Errorf("%s would bring spending to $%.2f%s, over the %s limit of $%.2f",
				what, total, queued, w.name, w.limit)
		}
	}
	return nil
}

// committed is what the jobs in flight, other than job, are still expected to spend.
func (r *Runner) committed(job string) float64 {
	r.mu.Lock()
	list := make([]*active, 0, len(r.active))
	for id, a := range r.active {
		if id != job {
			list = append(list, a)
		}
	}
	r.mu.Unlock()
	total := 0.0
	for _, a := range list {
		a.mu.Lock()
		for _, usd := range a.owed {
			total += usd
		}
		a.mu.Unlock()
	}
	return total
}

// BudgetError is a paid batch a spending limit kept from starting, or stopped while it ran.
type BudgetError struct {
	// Limit is "per image", "daily" or "monthly".
	Limit    string
	Max, USD float64
}

func (e *BudgetError) Error() string {
	if e.Limit == "per image" {
		return fmt.Sprintf("stopped by the spending limit of $%.2f per image: this image would cost $%.2f", e.Max, e.USD)
	}
	return fmt.Sprintf("stopped by the %s spending limit of $%.2f: spending would reach $%.2f", e.Limit, e.Max, e.USD)
}

// meter is a paid batch running up a bill: its prices per generation, owed once it runs, and
// its rate per second since it started.
type meter struct {
	job   string
	fixed float64
	rate  float64
	start time.Time
}

func (m *meter) owed(now time.Time) float64 { return m.fixed + m.rate*now.Sub(m.start).Seconds() }

// meterTick is how often a batch billed by the second is checked against the limits.
var meterTick = time.Second

// over is the spending limit a batch passes when it owes mine, counting what the job spent so
// far, what was spent today and this month, and what the other paid batches owe now.
func (r *Runner) over(ctx context.Context, b config.Budget, m *meter, mine float64, now time.Time) (*BudgetError, error) {
	if b.PerJob > 0 {
		spent, err := r.db.SpentOn(ctx, m.job)
		if err != nil {
			return nil, err
		}
		if spent+mine > b.PerJob+cent {
			return &BudgetError{Limit: "per image", Max: b.PerJob, USD: spent + mine}, nil
		}
	}
	others := 0.0
	r.mu.Lock()
	for o := range r.meters {
		if o != m {
			others += o.owed(now)
		}
	}
	r.mu.Unlock()
	for _, w := range windows(b, now) {
		spent, err := r.db.Spent(ctx, w.since)
		if err != nil {
			return nil, err
		}
		if total := spent + others + mine; total > w.limit+cent {
			return &BudgetError{Limit: w.name, Max: w.limit, USD: total}, nil
		}
	}
	return nil, nil
}

// admit lets a paid batch start only when what it is expected to cost fits the spending limits
// (a hard stop: spec section 8), and meters it while it runs. stop cancels the batch with the
// limit it passed; release ends the metering once the batch is billed.
func (r *Runner) admit(ctx context.Context, project, job string, charges []charge, stop context.CancelCauseFunc,
) (release func(), err error) {
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	b := cfg.Routing.Budget
	m := &meter{job: job, start: time.Now()}
	expected, unpriced := 0.0, false
	for _, c := range charges {
		m.fixed += c.PerCall
		m.rate += c.Rate
		expected += c.USD()
		unpriced = unpriced || c.Unpriced
	}
	if limited(b) {
		if unpriced {
			return nil, fmt.Errorf("a paid stage has no known price, so the spending limits cannot be checked")
		}
		over, err := r.over(ctx, b, m, expected, m.start)
		if err != nil {
			return nil, err
		}
		if over != nil {
			return nil, over
		}
	}
	r.mu.Lock()
	r.meters[m] = true
	r.mu.Unlock()
	done := make(chan struct{})
	if m.rate > 0 && limited(b) {
		go r.watch(ctx, project, m, stop, done)
	}
	return func() {
		close(done)
		r.mu.Lock()
		delete(r.meters, m)
		r.mu.Unlock()
	}, nil
}

// watch stops a batch billed by the second once it would pass a limit, read afresh each tick
// so a lowered limit applies at once.
func (r *Runner) watch(ctx context.Context, project string, m *meter, stop context.CancelCauseFunc, done <-chan struct{}) {
	t := time.NewTicker(meterTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case now := <-t.C:
			cfg, err := r.config(project)
			if err != nil {
				continue
			}
			if over, _ := r.over(ctx, cfg.Routing.Budget, m, m.owed(now), now); over != nil {
				r.log.Warn("a spending limit stopped a paid stage", "job", m.job, "limit", over.Limit)
				stop(over)
				return
			}
		}
	}
}

// bill records what a paid batch cost, for the spending limits and `forja spend`: each stage's
// price per generation once the provider ran it (it succeeded, or the provider billed it even
// so), and its seconds at its rate. The batch's time outside its stages goes on its first stage
// at the provider's rate. What is recorded is no longer owed by the job.
func (r *Runner) bill(a *active, p provider.Provider, stages []planner.Stage, charges []charge, run *engineapi.Run,
	billed map[string]bool, wall float64,
) {
	ctx := context.Background()
	amounts := make([]float64, len(stages))
	inside := 0.0
	for i, s := range stages {
		var st engineapi.StageState
		if run != nil {
			st = run.Stages[s.ID]
		}
		if charges[i].PerCall > 0 && (st.Status == "succeeded" || billed[s.ID]) {
			amounts[i] += charges[i].PerCall
		}
		if st.Seconds != nil {
			amounts[i] += charges[i].Rate * *st.Seconds
			inside += *st.Seconds
		}
	}
	if len(stages) > 0 && wall > inside {
		amounts[0] += charges[0].Overhead * (wall - inside)
	}
	for i, s := range stages {
		a.mu.Lock()
		delete(a.owed, s.ID)
		a.mu.Unlock()
		if amounts[i] <= 0 {
			continue
		}
		c := library.Cost{Job: a.job.ID, Stage: s.ID, Provider: p.ID(), Model: s.Model, Amount: amounts[i], Currency: "USD"}
		if err := r.db.RecordCost(ctx, c); err != nil {
			r.log.Warn("recording a cost", "job", a.job.ID, "err", err)
		}
	}
}

// chargesFor is what each stage of a batch on p is expected to cost. A stage the job did not
// pick a model for (looking for a sheet of views) costs the provider's time.
func (r *Runner) chargesFor(ctx context.Context, a *active, p provider.Provider, stages []planner.Stage) []charge {
	out := make([]charge, len(stages))
	if !p.Paid() {
		return out
	}
	cfg, err := r.config(a.job.Project)
	if err != nil {
		return out
	}
	a.mu.Lock()
	picks := a.picks
	a.mu.Unlock()
	for i, s := range stages {
		if pick, ok := picks[s.ID]; ok && pick.Provider == p.ID() {
			out[i] = r.chargeOf(ctx, cfg, pick)
			continue
		}
		rate, _ := cfg.Rate(p.ID())
		out[i] = charge{Rate: rate, Overhead: rate, Unpriced: rate == 0}
	}
	return out
}
