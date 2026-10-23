// Package presets loads pipeline definitions: built-in, user and project YAML files.
//
// Stages run in listed order. A stage without `inputs` reads the previous stage; inputs name
// earlier stage ids or `input:image`. Later layers replace earlier presets with the same id.
//
// Built-in presets set budgets and stage types, never models: a stage without `model` gets
// one when the job is submitted (docs/decisions.md, decision 12). Pin one per project with
// an override: "shape.model" = "triposg".
package presets

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed builtin/*.yaml
var builtin embed.FS

const InputImage = "input:image"

type Stage struct {
	ID       string         `yaml:"id" json:"id"`
	Use      string         `yaml:"use" json:"use"`
	Model    string         `yaml:"model,omitempty" json:"model,omitempty"`
	Params   map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
	Inputs   []string       `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Provider string         `yaml:"provider,omitempty" json:"provider,omitempty"`
	// Optional stages can be switched off per job with "<id>.enabled" = false. A stage that is
	// off hands its first input to the stages that read it.
	Optional *Option `yaml:"optional,omitempty" json:"optional,omitempty"`
	Off      bool    `yaml:"-" json:"off,omitempty"`
}

// Option is how the UI offers an optional stage: a labelled switch with a default.
type Option struct {
	Label   string `yaml:"label" json:"label"`
	Default bool   `yaml:"default" json:"default"`
}

type Preset struct {
	ID          string  `yaml:"id" json:"id"`
	Name        string  `yaml:"name" json:"name"`
	Description string  `yaml:"description" json:"description"`
	Stages      []Stage `yaml:"stages" json:"stages"`
	Source      string  `yaml:"-" json:"source"`
}

func (p *Preset) Builtin() bool { return p.Source == "builtin" }

// Load returns presets by id from built-ins, then each dir in order.
func Load(dirs ...string) (map[string]*Preset, error) {
	out := map[string]*Preset{}
	entries, _ := fs.ReadDir(builtin, "builtin")
	for _, e := range entries {
		raw, _ := builtin.ReadFile("builtin/" + e.Name())
		p, err := Parse(raw, "builtin")
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	for _, dir := range dirs {
		files, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if f.IsDir() || !(strings.HasSuffix(f.Name(), ".yaml") || strings.HasSuffix(f.Name(), ".yml")) {
				continue
			}
			path := filepath.Join(dir, f.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			p, err := Parse(raw, path)
			if err != nil {
				return nil, err
			}
			out[p.ID] = p
		}
	}
	return out, nil
}

func Parse(raw []byte, source string) (*Preset, error) {
	var p Preset
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	p.Source = source
	if err := p.normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return &p, nil
}

func (p *Preset) normalize() error {
	if p.ID == "" || len(p.Stages) == 0 {
		return errors.New("preset needs an id and at least one stage")
	}
	seen := map[string]bool{}
	for i := range p.Stages {
		s := &p.Stages[i]
		if s.ID == "" || s.Use == "" || seen[s.ID] {
			return fmt.Errorf("stage %d: needs a unique id and a use", i)
		}
		if len(s.Inputs) == 0 {
			if i == 0 {
				s.Inputs = []string{InputImage}
			} else {
				s.Inputs = []string{p.Stages[i-1].ID}
			}
		}
		for _, in := range s.Inputs {
			if in != InputImage && !seen[in] {
				return fmt.Errorf("stage %s reads %q, which is not an earlier stage", s.ID, in)
			}
		}
		if s.Optional != nil {
			if s.Optional.Label == "" {
				return fmt.Errorf("stage %s: an optional stage needs a label", s.ID)
			}
			s.Off = !s.Optional.Default
		}
		seen[s.ID] = true
	}
	if p.Stages[len(p.Stages)-1].Optional != nil {
		return errors.New("the last stage cannot be optional")
	}
	return nil
}

// Active returns the stages that run: optional stages that are off are dropped, and whatever
// read them reads their first input instead.
func (p *Preset) Active() *Preset {
	cp := *p
	cp.Stages = nil
	through := map[string]string{}
	resolve := func(in string) string {
		for {
			next, ok := through[in]
			if !ok {
				return in
			}
			in = next
		}
	}
	for _, s := range p.Stages {
		if s.Off {
			through[s.ID] = s.Inputs[0]
			continue
		}
		inputs := make([]string, len(s.Inputs))
		for i, in := range s.Inputs {
			inputs[i] = resolve(in)
		}
		s.Inputs = inputs
		cp.Stages = append(cp.Stages, s)
	}
	return &cp
}

// WithOverrides returns a copy with "stage.param" (or "stage.model"/"stage.provider") values applied.
func (p *Preset) WithOverrides(overrides map[string]any) (*Preset, error) {
	cp := *p
	cp.Stages = make([]Stage, len(p.Stages))
	for i, s := range p.Stages {
		s.Params = cloneMap(s.Params)
		cp.Stages[i] = s
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		stageID, field, ok := strings.Cut(key, ".")
		if !ok {
			return nil, fmt.Errorf("override %q: want <stage>.<param>", key)
		}
		idx := -1
		for i := range cp.Stages {
			if cp.Stages[i].ID == stageID {
				idx = i
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("override %q: preset %s has no stage %q", key, p.ID, stageID)
		}
		st := &cp.Stages[idx]
		switch field {
		case "model":
			st.Model = fmt.Sprint(overrides[key])
		case "provider":
			st.Provider = fmt.Sprint(overrides[key])
		case "enabled":
			if st.Optional == nil {
				return nil, fmt.Errorf("override %q: stage %s is not optional", key, stageID)
			}
			on, err := truthy(overrides[key])
			if err != nil {
				return nil, fmt.Errorf("override %q: %w", key, err)
			}
			st.Off = !on
		default:
			if st.Params == nil {
				st.Params = map[string]any{}
			}
			st.Params[field] = overrides[key]
		}
	}
	return &cp, nil
}

// truthy accepts booleans and the strings and numbers that `--set` and env vars produce.
func truthy(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "on", "yes":
			return true, nil
		case "false", "0", "off", "no":
			return false, nil
		}
	case int:
		return x != 0, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	}
	return false, fmt.Errorf("want true or false, got %v", v)
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
