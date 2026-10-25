package jobs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/presets"
)

// Pick says where one stage runs and with which model.
type Pick struct {
	Stage     string `json:"stage"`
	Use       string `json:"use"`
	Model     string `json:"model,omitempty"`
	ModelName string `json:"model_name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	// ProviderLabel names the provider for people: "This computer", "Studio PC", "fal.ai".
	ProviderLabel string `json:"provider_label,omitempty"`
	// Missing is set when the stage needs a model and no provider has one ready, or when it is
	// a built-in no provider can run; Problem then says why (Blender was not found).
	Missing bool   `json:"missing,omitempty"`
	Problem string `json:"problem,omitempty"`
	// Skipped stages are done by the model of an earlier stage, named by By: a hosted model
	// that returns a textured mesh does the texture stage too (docs/decisions.md, 16).
	Skipped bool   `json:"skipped,omitempty"`
	By      string `json:"by,omitempty"`
	// Paid and Price come from the provider and the model; the UI adds them up per image.
	Paid  bool             `json:"paid,omitempty"`
	Price *engineapi.Price `json:"price,omitempty"`
	Tasks []string         `json:"-"`
	// Views are the views of the subject the model reads (empty: the front alone). NeedsViews
	// are views it cannot run without that the job lacks.
	Views      []string `json:"views,omitempty"`
	NeedsViews []string `json:"needs_views,omitempty"`
}

// MissingError is a job that cannot run until models are installed for some stage types, or
// until a provider can run some built-in stage (Problems, by stage type).
type MissingError struct {
	Uses     []string
	Problems map[string]string
}

func (e *MissingError) Error() string {
	var parts, models []string
	for _, use := range e.Uses {
		if why, ok := e.Problems[use]; ok {
			parts = append(parts, fmt.Sprintf("no provider can run %s: %s", use, why))
		} else {
			models = append(models, use)
		}
	}
	if len(models) > 0 {
		parts = append(parts, fmt.Sprintf(
			"no %s model is installed: install one on the Models screen or with `forja models install`",
			strings.Join(models, ", ")))
	}
	return strings.Join(parts, "; ")
}

func missingError(picks []Pick) error {
	e := &MissingError{Problems: map[string]string{}}
	for _, p := range picks {
		if p.Missing && !p.Skipped {
			e.Uses = append(e.Uses, p.Use)
			if p.Problem != "" {
				e.Problems[p.Use] = p.Problem
			}
		}
	}
	if len(e.Uses) > 0 {
		return e
	}
	return nil
}

// Resolve says how a preset would run in project right now, without submitting anything, for
// a job with the given other views of its subject. The picks cover every stage of the active
// preset, skipped ones included.
func (r *Runner) Resolve(ctx context.Context, project string, p *presets.Preset, views []string) ([]Pick, error) {
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	if po, ok := cfg.Presets[p.ID]; ok && len(po.Overrides) > 0 {
		if p, err = p.WithOverrides(po.Overrides); err != nil {
			return nil, err
		}
	}
	picks, _, err := r.resolve(ctx, cfg, p.Active(), ordered(views), nil)
	if err != nil {
		return nil, err
	}
	// An optional stage that is off has no pick. One that reads a skipped stage gets a skipped
	// pick anyway, so the app does not offer to turn on a step that would not run.
	by := map[string]string{}
	for _, pick := range picks {
		by[pick.Stage] = pick.By
	}
	for _, s := range p.Stages {
		if _, ok := by[s.ID]; ok || s.Optional == nil {
			continue
		}
		for _, in := range s.Inputs {
			if by[in] != "" {
				picks = append(picks, Pick{Stage: s.ID, Use: s.Use, Skipped: true, By: by[in]})
				break
			}
		}
	}
	return picks, nil
}

// Unavailable says, for each optional stage of a preset that is off in project, why turning it
// on would refuse the job: no provider runs it (a Blender stage where no Blender is found) or
// no model for it is ready. Stages that could run are left out.
func (r *Runner) Unavailable(ctx context.Context, project string, p *presets.Preset) (map[string]Blocked, error) {
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	if po, ok := cfg.Presets[p.ID]; ok && len(po.Overrides) > 0 {
		if p, err = p.WithOverrides(po.Overrides); err != nil {
			return nil, err
		}
	}
	out := map[string]Blocked{}
	for _, s := range p.Stages {
		if s.Optional == nil || !s.Off {
			continue
		}
		pick, _, err := r.pick(ctx, cfg, s, nil, nil)
		if err != nil || !pick.Missing {
			continue
		}
		if pick.Problem != "" {
			out[s.ID] = Blocked{Use: s.Use, Reason: pick.Problem, Software: true}
		} else {
			out[s.ID] = Blocked{Use: s.Use, Reason: fmt.Sprintf("no %s model is installed", s.Use)}
		}
	}
	return out, nil
}

// Blocked is why an optional stage could not run if turned on.
type Blocked struct {
	Use    string
	Reason string
	// Software is set when no provider runs the built-in stage for lack of a program there
	// (Blender), rather than for lack of a model.
	Software bool
}

// resolve picks a provider and a model for every stage (docs/decisions.md, decisions 12 and
// 17), then skips the stages a picked model already does. It returns the picks for every stage
// and the preset that runs: skipped stages left out, and whatever read them reading their input.
// avoid holds the providers stages of this job failed on, which they no longer go to.
func (r *Runner) resolve(ctx context.Context, cfg *config.Loaded, preset *presets.Preset, views []string, avoid failed,
) ([]Pick, *presets.Preset, error) {
	picks := make([]Pick, 0, len(preset.Stages))
	for _, s := range preset.Stages {
		pick, _, err := r.pick(ctx, cfg, s, views, avoid)
		if err != nil {
			return nil, nil, err
		}
		picks = append(picks, pick)
	}

	done := map[string]string{} // stage type -> the stage whose model does it
	skip := map[string]string{} // stage id -> the stage that does its work
	for i, s := range preset.Stages {
		if by, ok := done[s.Use]; ok {
			skip[s.ID] = by
			continue
		}
		for _, t := range picks[i].Tasks {
			if _, ok := done[t]; !ok {
				done[t] = s.ID
			}
		}
	}
	// Optional stages refine what the stage they read made; they go with it.
	for _, s := range preset.Stages {
		for _, in := range s.Inputs {
			if by, ok := skip[in]; ok && s.Optional != nil {
				skip[s.ID] = by
			}
		}
	}
	if len(skip) == 0 {
		return picks, preset, nil
	}
	cp := *preset
	cp.Stages = append([]presets.Stage(nil), preset.Stages...)
	for i := range cp.Stages {
		if by, ok := skip[cp.Stages[i].ID]; ok {
			cp.Stages[i].Off = true
			picks[i] = Pick{Stage: cp.Stages[i].ID, Use: cp.Stages[i].Use, Skipped: true, By: by}
		}
	}
	return picks, cp.Active(), nil
}

// pick chooses the provider and model of one stage (docs/decisions.md, decision 12), and lists
// the other places it could run, in the order a failure falls back to them.
//
// A provider the stage, the job or `[routing.stages]` names is the only one. Otherwise the
// stage goes to the first provider in routing order that can run it, with the order sorted by
// the routing policy: a model named by the preset, the job or `[models.stages]` where it is
// ready (capability routing), an open stage with the best model ready there. Paid providers
// take part only when routing.allow_paid_fallback is on, or for a pinned model that no free
// provider has (a hosted model, decision 16). views are the job's views besides the front.
func (r *Runner) pick(ctx context.Context, cfg *config.Loaded, s presets.Stage, views []string, avoid failed) (Pick, []Pick, error) {
	base := Pick{Stage: s.ID, Use: s.Use, Model: s.Model}
	if base.Model == "" {
		base.Model = cfg.Models.Stages[s.Use]
	}
	want := s.Provider
	if want == "auto" {
		want = ""
	}
	if want == "" {
		want = cfg.Routing.Stages[s.Use]
	}
	if want != "" {
		p, ok := r.providers.Get(want)
		if !ok {
			return base, nil, fmt.Errorf("stage %s routes to provider %q, which is not configured", s.ID, want)
		}
		if !offline(cfg, p) {
			return base, nil, fmt.Errorf("stage %s: policy offline-only forbids provider %q", s.ID, want)
		}
		if avoid.has(s.ID, want) {
			return base, nil, fmt.Errorf("stage %s runs only on %s, which failed", s.ID, r.label(cfg, want))
		}
		pick, ok, _, err := r.on(ctx, cfg, base, want, views)
		switch {
		case err != nil && base.Model == "":
			return base, nil, err
		case err != nil:
			// A provider that cannot answer is given the benefit of the doubt; one that can and
			// lacks the model would only fail the job later.
			pick = base
			pick.Provider, pick.ProviderLabel = want, r.label(cfg, want)
		case !ok:
			pick.Missing = true
		}
		return pick, nil, nil
	}

	ids := r.routing(cfg, s.ID, avoid)
	var opts []Pick
	var firstErr error
	var silent []string // providers that could not say what they run
	listed := false     // a free provider lists the pinned model, ready or not
	problem := ""       // why a provider cannot run the stage (a Blender stage without Blender)
	for _, id := range ids {
		p, _ := r.providers.Get(id)
		pick, ok, has, err := r.on(ctx, cfg, base, id, views)
		if err != nil {
			firstErr = cmp.Or(firstErr, err)
			silent = append(silent, id)
			continue
		}
		if !p.Paid() {
			listed = listed || has
		}
		if ok {
			opts = append(opts, pick)
		} else if problem == "" {
			problem = pick.Problem
		}
	}
	paidOK := cfg.Routing.AllowPaidFallback || (base.Model != "" && !listed)
	if !paidOK {
		opts = slices.DeleteFunc(opts, func(p Pick) bool { return p.Paid })
	}
	if slices.ContainsFunc(opts, func(p Pick) bool { return p.Model != "" }) {
		opts = r.byPolicy(ctx, cfg, opts)
	}
	switch {
	case len(opts) > 0:
		return opts[0], opts[1:], nil
	case len(silent) > 0 && len(silent) == len(ids):
		return base, nil, firstErr
	}
	// A pinned model is given the benefit of the doubt on a provider that cannot say what it
	// runs now: one that can and lacks it would only fail the job later.
	for _, id := range silent {
		if p, _ := r.providers.Get(id); base.Model != "" && (paidOK || !p.Paid()) {
			base.Provider, base.ProviderLabel = id, r.label(cfg, id)
			return base, nil, nil
		}
	}
	base.Missing, base.Problem = true, problem
	return base, nil, nil
}

// on is what a stage gets on one provider: ok when it can run there, has when the provider
// lists the stage's pinned model at all, err when the provider could not say what it runs.
func (r *Runner) on(ctx context.Context, cfg *config.Loaded, base Pick, id string, views []string) (pick Pick, ok, has bool, err error) {
	pick = base
	pick.Provider, pick.ProviderLabel = id, r.label(cfg, id)
	e, err := r.catalog.Get(ctx, id)
	if err != nil {
		return base, false, false, err
	}
	if p, found := r.providers.Get(id); found {
		pick.Paid = p.Paid()
	}
	if base.Model != "" {
		m, has := e.Model(base.Model)
		if !has {
			return pick, false, false, nil
		}
		r.describe(cfg, &pick, &m)
		pick.NeedsViews = m.Lacks(views)
		return pick, m.Ready(), true, nil
	}
	// Not every provider runs every built-in (a Blender stage needs Blender there).
	if builtin(e, base.Use) {
		return pick, true, false, nil
	}
	if m := best(e.Models, base.Use, cfg.Models.Stubs, views); m != nil {
		pick.Model = m.ID
		r.describe(cfg, &pick, m)
		return pick, true, false, nil
	}
	pick.Problem = e.Unavailable[base.Use]
	return pick, false, false, nil
}

// describe copies what the job needs to know of the model into its pick. A price table entry
// named after the model replaces the price its provider reports.
func (r *Runner) describe(cfg *config.Loaded, pick *Pick, m *engineapi.Model) {
	pick.ModelName, pick.Tasks, pick.Price, pick.Views = m.Name, m.Tasks, m.Price, m.Views
	if entry, ok := cfg.Prices[m.ID]; ok {
		pick.Price = &engineapi.Price{Amount: entry.USD, Unit: entry.Per, Currency: "USD"}
	}
	if p, ok := r.providers.Get(pick.Provider); ok {
		pick.Paid = p.Paid()
	}
}

func builtin(e *catalog.Entry, use string) bool {
	_, ok := e.Stages[use]
	return ok
}

// best is the model ready for a stage type that reads the most of the job's other views, then
// the highest-ranked; stubs only when allowed. Models that need views the job lacks are out.
func best(models []engineapi.Model, use string, stubs bool, views []string) *engineapi.Model {
	var out *engineapi.Model
	for i := range models {
		m := &models[i]
		if !m.Does(use) || !m.Ready() || (m.Stub && !stubs) || len(m.Lacks(views)) > 0 {
			continue
		}
		if out == nil {
			out = m
			continue
		}
		mine, theirs := len(m.Reads(views)), len(out.Reads(views))
		if mine > theirs || (mine == theirs && (m.Rank > out.Rank || (m.Rank == out.Rank && m.ID < out.ID))) {
			out = m
		}
	}
	return out
}

// BestModel is the model a preset stage of this type gets on a provider when nothing pins one.
func BestModel(models []engineapi.Model, use string, stubs bool) *engineapi.Model {
	return best(models, use, stubs, nil)
}

// Pick says which provider and model a stage of this type gets in project right now.
func (r *Runner) Pick(ctx context.Context, project, use string) (Pick, error) {
	cfg, err := r.config(project)
	if err != nil {
		return Pick{}, err
	}
	pick, _, err := r.pick(ctx, cfg, presets.Stage{ID: use, Use: use}, nil, nil)
	return pick, err
}
