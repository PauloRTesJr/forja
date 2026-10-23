// Package planner turns a preset and an input into stages with cache keys.
//
// A stage's key covers everything that determines its output: stage type, model, params,
// seed, what the routed provider runs for it (adapter version, weights revision, upstream
// source) and the keys (or content hashes) of its inputs. Keys chain through upstream keys,
// so the whole plan is keyed before anything runs and cache hits are known up front.
package planner

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/zeebo/blake3"

	"forja/internal/presets"
)

// keyVersion invalidates every cached stage when the key recipe changes.
const keyVersion = 2

// Fingerprint is what a provider runs for one stage. A new adapter version, new weights or new
// upstream code changes the key, so outputs cached by the old ones are never served again.
type Fingerprint struct {
	Adapter  string `json:"adapter"`
	Weights  string `json:"weights,omitempty"`
	Upstream string `json:"upstream,omitempty"`
}

// Fingerprints says what will run each stage; the job runner asks the provider it routed to.
type Fingerprints func(presets.Stage) (Fingerprint, error)

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
func Build(preset *presets.Preset, inputs map[string]string, seed int, fingerprints Fingerprints) (*Plan, error) {
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
		fp, err := fingerprints(s)
		if err != nil {
			return nil, fmt.Errorf("stage %s: %w", s.ID, err)
		}
		key, err := stageKey(s, seed, fp, inputs)
		if err != nil {
			return nil, err
		}
		keys[s.ID] = key
		plan.Stages = append(plan.Stages, Stage{Stage: s, Key: key})
	}
	return plan, nil
}

func stageKey(s presets.Stage, seed int, fp Fingerprint, inputs []string) (string, error) {
	// encoding/json sorts map keys, so params serialize canonically.
	raw, err := json.Marshal(struct {
		V      int            `json:"v"`
		Use    string         `json:"use"`
		Model  string         `json:"model"`
		Params map[string]any `json:"params"`
		Seed   int            `json:"seed"`
		Runs   Fingerprint    `json:"runs"`
		Inputs []string       `json:"inputs"`
		// Left out when empty, so stages that read one view keep the keys they had.
		Views []string `json:"views,omitempty"`
	}{keyVersion, s.Use, s.Model, s.Params, seed, fp, inputs, s.Views})
	if err != nil {
		return "", fmt.Errorf("stage %s: params are not serializable: %w", s.ID, err)
	}
	sum := blake3.Sum256(raw)
	return "stage:" + hex.EncodeToString(sum[:16]), nil
}
