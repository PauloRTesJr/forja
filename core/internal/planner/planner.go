// Package planner turns a preset and an input into stages with keys.
//
// A stage's key covers the work it does: stage type, model, params, seed and the keys (or
// content hashes) of its inputs. Two stages of one job with the same key do the same work, so
// the job runs it once: the background removal it ran while looking for a sheet of views
// serves its own stages. Nothing is reused from one job to the next (docs/decisions.md, 37).
package planner

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zeebo/blake3"

	"forja/internal/presets"
)

type Stage struct {
	presets.Stage
	Key string `json:"key"`
	// Provider is filled by routing; empty until then.
	Provider string `json:"provider,omitempty"`
}

type Plan struct {
	Preset string `json:"preset"`
	// Input is the front image; Inputs holds it and the job's other views, by input name.
	Input  string            `json:"input"`
	Inputs map[string]string `json:"inputs"`
	Seed   int               `json:"seed"`
	Stages []Stage           `json:"stages"`
}

func (p *Plan) Index(id string) int {
	for i, s := range p.Stages {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// Build keys every stage of preset. inputs holds the content hash of each job input the stages
// read: presets.InputImage, and presets.InputView of each other view.
func Build(preset *presets.Preset, inputs map[string]string, seed int) (*Plan, error) {
	plan := &Plan{Preset: preset.ID, Input: inputs[presets.InputImage], Inputs: map[string]string{}, Seed: seed}
	keys := make(map[string]string, len(inputs)+len(preset.Stages))
	for name, ref := range inputs {
		keys[name], plan.Inputs[name] = ref, ref
	}
	for _, s := range preset.Stages {
		inputs := make([]string, len(s.Inputs))
		for i, in := range s.Inputs {
			k, ok := keys[in]
			if !ok {
				return nil, fmt.Errorf("stage %s: unknown input %q", s.ID, in)
			}
			inputs[i] = k
		}
		key, err := stageKey(s, seed, inputs)
		if err != nil {
			return nil, err
		}
		keys[s.ID] = key
		plan.Stages = append(plan.Stages, Stage{Stage: s, Key: key})
	}
	return plan, nil
}

func stageKey(s presets.Stage, seed int, inputs []string) (string, error) {
	// encoding/json sorts map keys, so params serialize canonically.
	raw, err := json.Marshal(struct {
		Use    string         `json:"use"`
		Model  string         `json:"model"`
		Params map[string]any `json:"params"`
		Seed   int            `json:"seed"`
		Inputs []string       `json:"inputs"`
		Views  []string       `json:"views,omitempty"`
	}{s.Use, s.Model, s.Params, seed, inputs, s.Views})
	if err != nil {
		return "", fmt.Errorf("stage %s: params are not serializable: %w", s.ID, err)
	}
	sum := blake3.Sum256(raw)
	return "stage:" + hex.EncodeToString(sum[:16]), nil
}
