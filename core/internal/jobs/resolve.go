package jobs

import (
	"context"
	"fmt"
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
	// Missing is set when the stage needs a model and no provider has one ready.
	Missing bool `json:"missing,omitempty"`
	// Skipped stages are done by the model of an earlier stage, named by By: a hosted model
	// that returns a textured mesh does the texture stage too (docs/decisions.md, 16).
	Skipped bool   `json:"skipped,omitempty"`
	By      string `json:"by,omitempty"`
	// Paid and Price come from the provider and the model; the UI adds them up per image.
	Paid  bool             `json:"paid,omitempty"`
	Price *engineapi.Price `json:"price,omitempty"`
	Tasks []string         `json:"-"`
}

// MissingError is a job that cannot run until models are installed for some stage types.
type MissingError struct{ Uses []string }

func (e *MissingError) Error() string {
	return fmt.Sprintf("no %s model is installed: install one on the Models screen or with `forja models install`",
		strings.Join(e.Uses, ", "))
}

func missingError(picks []Pick) error {
	var uses []string
	for _, p := range picks {
		if p.Missing && !p.Skipped {
			uses = append(uses, p.Use)
		}
	}
	if len(uses) > 0 {
		return &MissingError{Uses: uses}
	}
	return nil
}

// Resolve says how a preset would run in project right now, without submitting anything.
// The picks cover every stage of the active preset, skipped ones included.
func (r *Runner) Resolve(ctx context.Context, project string, p *presets.Preset) ([]Pick, error) {
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	if po, ok := cfg.Presets[p.ID]; ok && len(po.Overrides) > 0 {
		if p, err = p.WithOverrides(po.Overrides); err != nil {
			return nil, err
		}
	}
	picks, _, err := r.resolve(ctx, cfg, p.Active())
	return picks, err
}

// resolve picks a provider and a model for every stage (docs/decisions.md, decision 12), then
// skips the stages a picked model already does. It returns the picks for every stage and the
// preset that runs: skipped stages left out, and whatever read them reading their input.
func (r *Runner) resolve(ctx context.Context, cfg *config.Loaded, preset *presets.Preset) ([]Pick, *presets.Preset, error) {
	picks := make([]Pick, 0, len(preset.Stages))
	for _, s := range preset.Stages {
		pick, err := r.pick(ctx, cfg, s)
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

// pick chooses the provider and model of one stage: a model named by the preset or the job,
// then `[models.stages]`, then the highest-ranked model ready on the first provider in routing
// order that has one. Paid providers are in routing order only when allowed.
func (r *Runner) pick(ctx context.Context, cfg *config.Loaded, s presets.Stage) (Pick, error) {
	pick := Pick{Stage: s.ID, Use: s.Use, Model: s.Model}
	if pick.Model == "" {
		pick.Model = cfg.Models.Stages[s.Use]
	}
	cands, err := r.candidates(cfg, s)
	if err != nil {
		return pick, err
	}
	if pick.Model != "" {
		pick.Provider = cands[0]
		// A provider that cannot answer is given the benefit of the doubt; one that can and
		// lacks the model would only fail the job later.
		if e, err := r.catalog.Get(ctx, pick.Provider); err == nil {
			m, ok := e.Model(pick.Model)
			pick.Missing = !ok || !m.Ready()
			if ok {
				r.describe(&pick, &m)
			}
		}
		return pick, nil
	}
	var firstErr error
	reached := false
	for _, id := range cands {
		e, err := r.catalog.Get(ctx, id)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		reached = true
		if builtin(e, s.Use) {
			pick.Provider = cands[0]
			break
		}
		if m := best(e.Models, s.Use, cfg.Models.Stubs); m != nil {
			pick.Model, pick.Provider = m.ID, id
			r.describe(&pick, m)
			break
		}
	}
	if !reached {
		return pick, firstErr
	}
	pick.Missing = pick.Provider == ""
	return pick, nil
}

func (r *Runner) describe(pick *Pick, m *engineapi.Model) {
	pick.ModelName, pick.Tasks, pick.Price = m.Name, m.Tasks, m.Price
	if p, ok := r.providers.Get(pick.Provider); ok {
		pick.Paid = p.Paid()
	}
}

func builtin(e *catalog.Entry, use string) bool {
	_, ok := e.Stages[use]
	return ok
}

// best is the highest-ranked model ready for a stage type; stubs only when allowed.
func best(models []engineapi.Model, use string, stubs bool) *engineapi.Model {
	var out *engineapi.Model
	for i := range models {
		m := &models[i]
		if !m.Does(use) || !m.Ready() || (m.Stub && !stubs) {
			continue
		}
		if out == nil || m.Rank > out.Rank || (m.Rank == out.Rank && m.ID < out.ID) {
			out = m
		}
	}
	return out
}

// BestModel is the model a preset stage of this type gets on a provider when nothing pins one.
func BestModel(models []engineapi.Model, use string, stubs bool) *engineapi.Model {
	return best(models, use, stubs)
}

// Pick says which provider and model a stage of this type gets in project right now.
func (r *Runner) Pick(ctx context.Context, project, use string) (Pick, error) {
	cfg, err := r.config(project)
	if err != nil {
		return Pick{}, err
	}
	return r.pick(ctx, cfg, presets.Stage{ID: use, Use: use})
}
