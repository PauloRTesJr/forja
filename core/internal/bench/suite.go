// Package bench runs a benchmark suite (spec, section 10) as ordinary jobs, measured: every
// stage of every image runs, and the engine reports its time, peak VRAM and RAM, model load,
// GPU utilization and output stats. A run's result is JSON with a versioned schema, recorded
// per commit and device, with a self-contained HTML report; Compare tells a regression from
// noise.
package bench

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SuiteSchema is the version of the suite file format this core reads.
const SuiteSchema = 1

// Suite is a fixed set of reference images and the presets to run them through.
type Suite struct {
	Schema int    `yaml:"schema" json:"schema"`
	Name   string `yaml:"name" json:"name"`
	// Version changes whenever the images or presets do: only results of one version compare.
	Version     int      `yaml:"version" json:"version"`
	Description string   `yaml:"description" json:"description,omitempty"`
	Presets     []string `yaml:"presets" json:"presets"`
	// Project is a folder, relative to the suite file, whose forja.toml pins the models a
	// benchmark runs (docs/decisions.md, decision 12).
	Project string `yaml:"project" json:"project,omitempty"`
	Seed    *int   `yaml:"seed" json:"seed,omitempty"`
	// Budgets are the targets per preset, in seconds from image to GLB (spec, section 10).
	Budgets map[string]float64 `yaml:"budgets" json:"budgets,omitempty"`
	// CoreOverheadMS is the budget for the core's own time per job.
	CoreOverheadMS float64     `yaml:"core_overhead_ms" json:"core_overhead_ms,omitempty"`
	Images         []ImageSpec `yaml:"images" json:"-"`
	// Path is the suite file, absolute.
	Path string `yaml:"-" json:"path"`
}

// ImageSpec names one reference image (Path) or a folder of them (Dir), relative to the suite.
type ImageSpec struct {
	Path     string `yaml:"path"`
	Dir      string `yaml:"dir"`
	Name     string `yaml:"name"`
	Category string `yaml:"category"`
}

// Image is one reference image a run measures.
type Image struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Category string `json:"category,omitempty"`
}

// Skipped is something the suite names that cannot run here, and why.
type Skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

var imageExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// LoadSuite reads a suite file.
func LoadSuite(path string) (*Suite, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading the suite: %w", err)
	}
	var s Suite
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", abs, err)
	}
	s.Path = abs
	switch {
	case s.Schema != SuiteSchema:
		return nil, fmt.Errorf("%s: schema %d; this forja reads suite schema %d", abs, s.Schema, SuiteSchema)
	case s.Name == "":
		return nil, fmt.Errorf("%s: the suite has no name", abs)
	case len(s.Presets) == 0:
		return nil, fmt.Errorf("%s: the suite names no presets", abs)
	case len(s.Images) == 0:
		return nil, fmt.Errorf("%s: the suite names no images", abs)
	}
	for i, im := range s.Images {
		if (im.Path == "") == (im.Dir == "") {
			return nil, fmt.Errorf("%s: image %d needs either path or dir", abs, i+1)
		}
	}
	if s.Project != "" {
		s.Project = s.resolve(s.Project)
	}
	return &s, nil
}

func (s *Suite) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(filepath.Dir(s.Path), filepath.FromSlash(p))
}

// Resolve lists the images that exist, in suite order, and what it skips: a file not there
// yet, a folder with no images. It never fetches anything.
func (s *Suite) Resolve() ([]Image, []Skipped) {
	var images []Image
	skipped := []Skipped{}
	names := map[string]int{}
	add := func(path, name, category string) {
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		// Two files may share a name in different folders; results are read by name.
		if names[name]++; names[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, names[name])
		}
		images = append(images, Image{Name: name, Path: path, Category: category})
	}
	for _, spec := range s.Images {
		if spec.Path != "" {
			path := s.resolve(spec.Path)
			st, err := os.Stat(path)
			switch {
			case errors.Is(err, os.ErrNotExist):
				skipped = append(skipped, Skipped{path, "not there: add the image, or take it out of the suite"})
			case err != nil:
				skipped = append(skipped, Skipped{path, err.Error()})
			case st.IsDir():
				skipped = append(skipped, Skipped{path, "a folder: name it with dir:"})
			default:
				add(path, spec.Name, spec.Category)
			}
			continue
		}
		dir := s.resolve(spec.Dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			skipped = append(skipped, Skipped{dir, "no such folder"})
			continue
		}
		found := 0
		for _, e := range entries {
			if e.Type().IsRegular() && slices.Contains(imageExts, strings.ToLower(filepath.Ext(e.Name()))) {
				add(filepath.Join(dir, e.Name()), "", spec.Category)
				found++
			}
		}
		if found == 0 {
			skipped = append(skipped, Skipped{dir, "no PNG, JPEG or WebP images in this folder yet"})
		}
	}
	return images, skipped
}
