package jobs

import (
	"context"
	"errors"
	"regexp"
	"slices"

	"forja/internal/config"
	"forja/internal/library"
)

// EvJobFallback tells clients a job's stage moved to another provider after failing on one.
const EvJobFallback = "job.fallback"

// stageError is a batch that failed on a provider, saying whether another provider may do
// better: this one could not be reached (down) or ran out of memory (oom). stages are the ones
// that should leave it; billed are the stages it charged for even so.
type stageError struct {
	provider string
	stages   []string
	billed   []string
	down     bool
	oom      bool
	err      error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// outOfMemory matches how engines report a stage that did not fit: PyTorch's and CUDA's own
// words, and the engine's refusal in quality-first mode.
var outOfMemory = regexp.MustCompile(`(?i)out of memory|OutOfMemoryError|CUBLAS_STATUS_ALLOC_FAILED|needs about [0-9.]+ ?GB but this device has`)

// fallback moves the stages that failed on a provider to the next place that can run them, when
// the provider could not be reached or ran out of memory (spec section 8). The routing that
// picked the first place picks the next, so a stage never moves to a paid provider unless
// routing.allow_paid_fallback is on, nor away from a provider the job or project names, and a
// stage the provider billed moves only to a free one: it is paid for once already. Stages
// that already succeeded there stay. It returns false when nothing can take over, and the
// failure stands.
func (r *Runner) fallback(ctx context.Context, a *active, cfg *config.Loaded, prep *prepared, err error) (*prepared, bool) {
	var se *stageError
	if ctx.Err() != nil || !errors.As(err, &se) || (!se.down && !se.oom) || len(se.stages) == 0 {
		return nil, false
	}
	reason := "could not be reached"
	if se.oom {
		reason = "ran out of memory"
	}
	leaving := slices.Clone(se.stages)
	if se.down {
		// The later stages it was to run would only find it down again.
		done := map[string]bool{}
		for _, s := range snapshot(a).Stages {
			done[s.ID] = s.Status == library.Succeeded
		}
		for _, s := range prep.preset.Stages {
			if prep.routes[s.ID] == se.provider && !done[s.ID] && !slices.Contains(leaving, s.ID) {
				leaving = append(leaving, s.ID)
			}
		}
	}
	for _, id := range leaving {
		a.avoid.add(id, se.provider, reason)
	}
	for _, id := range se.billed {
		for _, p := range r.providers.All() {
			if p.Paid() && !a.avoid.has(id, p.ID()) {
				a.avoid.add(id, p.ID(), "billed for it already")
			}
		}
	}
	next, perr := r.prepare(ctx, cfg, snapshot(a), a.avoid)
	if perr != nil {
		r.log.Info("no provider can take over", "job", a.job.ID, "from", se.provider, "err", perr)
		return nil, false
	}
	type move struct{ stage, to string }
	var moves []move
	for _, id := range leaving {
		if to := next.routes[id]; to != "" && to != prep.routes[id] {
			moves = append(moves, move{id, to})
		}
	}
	if len(moves) == 0 {
		return nil, false
	}
	r.update(a, func(j *library.Job) {
		r.restage(ctx, j, next)
		for _, m := range moves {
			if st := stage(j, m.stage); st != nil {
				st.Fallbacks = append(st.Fallbacks, library.Fallback{From: se.provider, To: m.to, Reason: reason,
					FromLabel: r.label(cfg, se.provider), ToLabel: r.label(cfg, m.to)})
			}
		}
	})
	a.set(next)
	for _, m := range moves {
		r.log.Info("stage moved to another provider", "job", a.job.ID, "stage", m.stage, "from", se.provider, "to", m.to,
			"reason", reason)
		r.hub.Publish(EvJobFallback, map[string]any{
			"job": a.job.ID, "stage": m.stage, "from": se.provider, "to": m.to, "reason": reason,
			"from_label": r.label(cfg, se.provider), "to_label": r.label(cfg, m.to), "error": se.Error(),
		})
	}
	r.hub.Publish(EvJobUpdated, snapshot(a))
	return next, true
}

// restage lists a job's stages for a new plan, keeping what already succeeded where it still
// runs the same way, and every stage's fallbacks.
func (r *Runner) restage(ctx context.Context, j *library.Job, prep *prepared) {
	old := map[string]library.Stage{}
	for _, s := range j.Stages {
		old[s.ID] = s
	}
	r.setStages(ctx, j, prep)
	for i := range j.Stages {
		s := &j.Stages[i]
		o, ok := old[s.ID]
		if !ok {
			continue
		}
		s.Fallbacks = o.Fallbacks
		if o.Status == library.Succeeded && o.Provider == s.Provider && o.Model == s.Model {
			s.Status, s.Outputs, s.Seconds, s.PeakVRAM = o.Status, o.Outputs, o.Seconds, o.PeakVRAM
		}
	}
}
