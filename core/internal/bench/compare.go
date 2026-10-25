package bench

import (
	"errors"
	"fmt"
	"time"
)

// Thresholds say what counts as a regression: a value that grew by more than Pct percent,
// and by at least the floor of its kind, which keeps timer noise on tiny stages from failing.
type Thresholds struct {
	Pct        float64 `json:"pct"`
	MinSeconds float64 `json:"min_seconds"`
	MinVRAMMB  float64 `json:"min_vram_mb"`
}

// Metrics a comparison reads.
const (
	MetricSeconds  = "seconds"         // median stage, or image-to-GLB, time
	MetricVRAM     = "peak_vram_bytes" // highest peak over the samples
	MetricOverhead = "core_overhead"   // median core seconds per job
	MetricFailed   = "failed"          // samples that did not succeed
)

// Delta is one metric of one preset (and stage, unless it is the whole image) in both runs.
type Delta struct {
	Preset string `json:"preset,omitempty"`
	// Stage is empty for the image as a whole.
	Stage    string  `json:"stage,omitempty"`
	Metric   string  `json:"metric"`
	Baseline float64 `json:"baseline"`
	Current  float64 `json:"current"`
	// Pct is the change in percent of the baseline; nil when the baseline is zero.
	Pct        *float64 `json:"pct"`
	Regression bool     `json:"regression"`
}

// RunRef names one side of a comparison.
type RunRef struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	Suite     SuiteRef  `json:"suite"`
	Forja     Build     `json:"forja"`
	Device    Device    `json:"device"`
	Options   Options   `json:"options"`
	StartedAt time.Time `json:"started_at"`
}

// Comparison is a run against a baseline. Regressions counts the deltas past the thresholds.
type Comparison struct {
	Baseline    RunRef     `json:"baseline"`
	Current     RunRef     `json:"current"`
	Thresholds  Thresholds `json:"thresholds"`
	Deltas      []Delta    `json:"deltas"`
	Regressions int        `json:"regressions"`
	// Warnings say why the numbers may not compare: another device, suite or option.
	Warnings []string `json:"warnings"`
}

func ref(r *Result) RunRef {
	return RunRef{ID: r.ID, Label: r.Label, Suite: r.Suite, Forja: r.Forja, Device: r.Device, Options: r.Options,
		StartedAt: r.StartedAt}
}

// Compare reads current against baseline: per preset, the image-to-GLB time and failures;
// per stage, the median time and the peak VRAM; and the core's overhead per job.
func Compare(baseline, current *Result, t Thresholds) (*Comparison, error) {
	if t.Pct <= 0 {
		return nil, errors.New("the threshold must be a percent above 0")
	}
	for _, r := range []*Result{baseline, current} {
		if r.Summary == nil {
			return nil, fmt.Errorf("benchmark %s has no summary: it has not finished", r.ID)
		}
	}
	c := &Comparison{Baseline: ref(baseline), Current: ref(current), Thresholds: t, Deltas: []Delta{}, Warnings: []string{}}
	warn := func(format string, args ...any) { c.Warnings = append(c.Warnings, fmt.Sprintf(format, args...)) }
	if baseline.Device.ID != current.Device.ID {
		warn("the baseline ran on %s and this run on %s", baseline.Device.ID, current.Device.ID)
	}
	if baseline.Suite.Name != current.Suite.Name || baseline.Suite.Version != current.Suite.Version {
		warn("the baseline ran suite %s version %d and this run %s version %d", baseline.Suite.Name,
			baseline.Suite.Version, current.Suite.Name, current.Suite.Version)
	}
	if baseline.Options.Cold != current.Options.Cold || baseline.Options.Batch != current.Options.Batch {
		warn("the runs differ in --cold or --batch, which change what the times include")
	}
	floorSeconds, floorVRAM := t.MinSeconds, t.MinVRAMMB*(1<<20)
	add := func(d Delta, floor float64) {
		if d.Baseline != 0 {
			pct := round((d.Current-d.Baseline)/d.Baseline*100, 1)
			d.Pct = &pct
			d.Regression = pct > t.Pct && d.Current-d.Baseline >= floor
		}
		if d.Regression {
			c.Regressions++
		}
		c.Deltas = append(c.Deltas, d)
	}

	base := map[string]PresetSummary{}
	for _, p := range baseline.Summary.Presets {
		base[p.Preset] = p
	}
	for _, p := range current.Summary.Presets {
		b, ok := base[p.Preset]
		if !ok {
			warn("the baseline did not run preset %s", p.Preset)
			continue
		}
		failed := Delta{Preset: p.Preset, Metric: MetricFailed, Baseline: float64(b.Failed), Current: float64(p.Failed),
			Regression: p.Failed > b.Failed}
		if failed.Regression {
			c.Regressions++
		}
		c.Deltas = append(c.Deltas, failed)
		if b.Seconds.N > 0 && p.Seconds.N > 0 {
			add(Delta{Preset: p.Preset, Metric: MetricSeconds, Baseline: b.Seconds.Median, Current: p.Seconds.Median},
				floorSeconds)
		}
	}
	stages := map[[2]string]StageSummary{}
	for _, s := range baseline.Summary.Stages {
		stages[[2]string{s.Preset, s.Stage}] = s
	}
	for _, s := range current.Summary.Stages {
		b, ok := stages[[2]string{s.Preset, s.Stage}]
		if !ok {
			if _, ran := base[s.Preset]; ran {
				warn("the baseline's %s has no stage %s", s.Preset, s.Stage)
			}
			continue
		}
		add(Delta{Preset: s.Preset, Stage: s.Stage, Metric: MetricSeconds, Baseline: b.Seconds.Median,
			Current: s.Seconds.Median}, floorSeconds)
		if b.PeakVRAMBytes != nil && s.PeakVRAMBytes != nil {
			add(Delta{Preset: s.Preset, Stage: s.Stage, Metric: MetricVRAM, Baseline: float64(*b.PeakVRAMBytes),
				Current: float64(*s.PeakVRAMBytes)}, floorVRAM)
		}
	}
	if baseline.Summary.CoreOverhead.N > 0 && current.Summary.CoreOverhead.N > 0 {
		add(Delta{Metric: MetricOverhead, Baseline: baseline.Summary.CoreOverhead.Median,
			Current: current.Summary.CoreOverhead.Median}, floorSeconds)
	}
	return c, nil
}
