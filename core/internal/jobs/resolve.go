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
		if p.Missing {
			uses = append(uses, p.Use)
		}
	}
	if len(uses) > 0 {
		return &MissingError{Uses: uses}
	}
	return nil
}

// Resolve says how a preset would run in project right now, without submitting anything.
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
	return r.resolve(ctx, cfg, p.Active())
}

// resolve picks a provider and a model for every stage (docs/decisions.md, decision 12): a
// model named by the preset or the job, then `[models.stages]`, then the highest-ranked model
// ready on the first provider in routing order that has one.
func (r *Runner) resolve(ctx context.Context, cfg *config.Loaded, preset *presets.Preset) ([]Pick, error) {
	picks := make([]Pick, 0, len(preset.Stages))
	for _, s := range preset.Stages {
		pick := Pick{Stage: s.ID, Use: s.Use, Model: s.Model}
		if pick.Model == "" {
			pick.Model = cfg.Models.Stages[s.Use]
		}
		cands, err := r.candidates(cfg, s)
		if err != nil {
			return nil, err
		}
		if pick.Model != "" {
			pick.Provider = cands[0]
			// A provider that cannot answer is given the benefit of the doubt; one that can
			// and lacks the model would only fail the job later.
			if e, err := r.catalog.Get(ctx, pick.Provider); err == nil {
				m, ok := e.Model(pick.Model)
				pick.ModelName = m.Name
				pick.Missing = !ok || !m.Ready()
			}
			picks = append(picks, pick)
			continue
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
				pick.Model, pick.ModelName, pick.Provider = m.ID, m.Name, id
				break
			}
		}
		if !reached {
			return nil, firstErr
		}
		pick.Missing = pick.Provider == ""
		picks = append(picks, pick)
	}
	return picks, nil
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
