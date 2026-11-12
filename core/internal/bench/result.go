package bench

import (
	"maps"
	"math"
	"slices"
	"sort"
	"time"

	"forja/internal/engineapi"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/quality"
)

// ResultSchema is the version of the result format. Readers refuse other versions instead of
// comparing numbers that may mean something else.
const ResultSchema = 1

// Run states besides a job's own.
const (
	Running   = "running"
	Succeeded = "succeeded"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// Result is one run of a suite on one device at one commit.
type Result struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	// Status is running until every sample ended; failed when a sample failed.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Phase says what a running benchmark does now: "warm-up" (loading the models with an
	// image that is not measured) or "measuring".
	Phase string `json:"phase,omitempty"`
	// WarmUp is the job loading the models during the warm-up, so a client can show it.
	WarmUp     string     `json:"warm_up,omitempty"`
	Suite      SuiteRef   `json:"suite"`
	Options    Options    `json:"options"`
	Forja      Build      `json:"forja"`
	Device     Device     `json:"device"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Total counts the samples the run takes; Jobs lists those started, in order.
	Total   int         `json:"total"`
	Skipped []Skipped   `json:"skipped"`
	Jobs    []JobResult `json:"jobs"`
	Summary *Summary    `json:"summary,omitempty"`
	// Report is the HTML report beside the result, once it is written.
	Report string `json:"report,omitempty"`
}

type SuiteRef struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Path    string `json:"path"`
	Project string `json:"project,omitempty"`
}

type Options struct {
	Presets []string `json:"presets"`
	Cold    bool     `json:"cold"`
	Batch   bool     `json:"batch"`
	Repeat  int      `json:"repeat"`
	Seed    int      `json:"seed"`
}

// Build is what ran: the core's version and commit, the engine's version and API.
type Build struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	// Modified is set when the core was built from a checkout with uncommitted changes.
	Modified bool   `json:"modified,omitempty"`
	Engine   string `json:"engine,omitempty"`
	API      string `json:"api,omitempty"`
}

// Device is the machine the models ran on, as its provider describes it.
type Device struct {
	// ID names the device in file names and comparisons, such as "nvidia-geforce-rtx-4070-ti".
	ID        string `json:"id"`
	Provider  string `json:"provider"`
	OS        string `json:"os,omitempty"`
	Arch      string `json:"arch,omitempty"`
	CPU       string `json:"cpu,omitempty"`
	RAMBytes  int64  `json:"ram_bytes,omitempty"`
	GPU       string `json:"gpu,omitempty"`
	Backend   string `json:"backend,omitempty"`
	VRAMBytes int64  `json:"vram_bytes,omitempty"`
	Driver    string `json:"driver,omitempty"`
}

// JobResult is one sample: an image through a preset.
type JobResult struct {
	Image    string         `json:"image"`
	Path     string         `json:"path"`
	Category string         `json:"category,omitempty"`
	Preset   string         `json:"preset"`
	Repeat   int            `json:"repeat"`
	Job      string         `json:"job,omitempty"`
	Input    string         `json:"input,omitempty"`
	Status   library.Status `json:"status"`
	Error    string         `json:"error,omitempty"`
	// WallSeconds runs from submit to the GLB, less the time the engine spent measuring.
	WallSeconds float64 `json:"wall_seconds"`
	// StageSeconds adds up the stages' own time and LoadSeconds their model loads.
	StageSeconds float64 `json:"stage_seconds"`
	LoadSeconds  float64 `json:"load_seconds"`
	// CoreOverheadSeconds is the core's own time: everything but inference and the wait for a
	// job slot.
	CoreOverheadSeconds float64       `json:"core_overhead_seconds"`
	Stages              []StageResult `json:"stages"`
	// Asset is what the final GLB holds: triangles, vertices, materials, LODs.
	Asset map[string]any `json:"asset,omitempty"`
	// Quality would score the output against the input (CLIP similarity of rendered views,
	// spec section 10). Nothing computes it yet, so it is always null.
	Quality *float64 `json:"quality"`
}

// StageResult is one stage of a sample, as the engine measured it.
type StageResult struct {
	ID       string         `json:"id"`
	Use      string         `json:"use"`
	Model    string         `json:"model,omitempty"`
	Provider string         `json:"provider,omitempty"`
	Status   library.Status `json:"status"`
	Error    string         `json:"error,omitempty"`
	// Seconds is the stage's own time, model load excluded.
	Seconds       float64                 `json:"seconds"`
	Device        string                  `json:"device,omitempty"`
	LoadSeconds   *float64                `json:"load_seconds"`
	PeakVRAMBytes *int64                  `json:"peak_vram_bytes"`
	PeakRAMBytes  *int64                  `json:"peak_ram_bytes"`
	GPUUtil       *float64                `json:"gpu_util"`
	Outputs       []engineapi.OutputStats `json:"outputs,omitempty"`
	// Metrics are what the stage measured about its outputs (match_iou, bake_hit_ratio, ...).
	// Results written before the engine reported them have none.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

// Summary is what the run comes to, against the suite's budgets.
type Summary struct {
	Presets []PresetSummary `json:"presets"`
	Stages  []StageSummary  `json:"stages"`
	// CoreOverhead spreads the core's own seconds per job.
	CoreOverhead         Spread  `json:"core_overhead"`
	CoreOverheadBudgetMS float64 `json:"core_overhead_budget_ms,omitempty"`
	// Devices say how busy each device the models ran on kept while samples ran.
	Devices []DeviceUse `json:"devices"`
	// WallSeconds runs from the first sample's submit to the last one's end.
	WallSeconds float64 `json:"wall_seconds"`
}

// Spread is the median, 90th percentile and maximum of N values.
type Spread struct {
	N      int     `json:"n"`
	Median float64 `json:"median"`
	P90    float64 `json:"p90"`
	Max    float64 `json:"max"`
}

type PresetSummary struct {
	Preset string `json:"preset"`
	Images int    `json:"images"`
	Failed int    `json:"failed"`
	// Seconds spreads the succeeded samples' wall time, image to GLB.
	Seconds       Spread  `json:"seconds"`
	BudgetSeconds float64 `json:"budget_seconds,omitempty"`
	OverBudget    int     `json:"over_budget"`
}

type StageSummary struct {
	Preset      string `json:"preset"`
	Stage       string `json:"stage"`
	Use         string `json:"use"`
	Model       string `json:"model,omitempty"`
	Seconds     Spread `json:"seconds"`
	LoadSeconds Spread `json:"load_seconds"`
	// The highest peaks over the samples; nil when never measured.
	PeakVRAMBytes *int64 `json:"peak_vram_bytes"`
	PeakRAMBytes  *int64 `json:"peak_ram_bytes"`
	// GPUUtil is the mean utilization, weighted by each sample's seconds.
	GPUUtil *float64 `json:"gpu_util"`
	// Of the stage's first mesh output: median triangles, the share of samples that were
	// watertight, median UV coverage.
	Triangles  *float64 `json:"triangles"`
	Watertight *float64 `json:"watertight"`
	UVCoverage *float64 `json:"uv_coverage"`
	// Metrics are the stage's own measures over the succeeded samples: the median of each, except
	// a count of flaws (lod_open_edges), which keeps the worst sample.
	Metrics map[string]float64 `json:"metrics,omitempty"`
}

type DeviceUse struct {
	Device string `json:"device"`
	// BusySeconds adds up the stages that ran on it; Busy is that share of the run's wall time.
	BusySeconds float64 `json:"busy_seconds"`
	Busy        float64 `json:"busy"`
	// GPUUtil is the share of the run's wall time the GPU ran kernels, from NVML: the
	// utilization across the batch the spec's budget names. Nil without NVML.
	GPUUtil *float64 `json:"gpu_util"`
}

// jobResult turns a finished sample into a result.
func jobResult(base JobResult, s jobs.Sample) JobResult {
	j := s.Job
	base.Job, base.Input, base.Status, base.Error = j.ID, j.Input, j.Status, j.Error
	base.Asset = s.Stats
	measuring := 0.0
	base.Stages = make([]StageResult, 0, len(j.Stages))
	for _, st := range j.Stages {
		r := StageResult{ID: st.ID, Use: st.Use, Model: st.Model, Provider: st.Provider, Status: st.Status,
			Error: st.Error, Seconds: st.Seconds, Metrics: st.Metrics()}
		if st.PeakVRAM > 0 {
			r.PeakVRAMBytes = ptr(st.PeakVRAM)
		}
		if m := s.Metrics[st.ID]; m != nil {
			r.Device, r.LoadSeconds, r.PeakRAMBytes, r.GPUUtil, r.Outputs = m.Device, m.LoadSeconds, m.PeakRAMBytes,
				m.GPUUtil, m.Outputs
			measuring += m.MeasureSeconds
		}
		if st.Status == library.Succeeded {
			base.StageSeconds += st.Seconds
			if r.LoadSeconds != nil {
				base.LoadSeconds += *r.LoadSeconds
			}
		}
		base.Stages = append(base.Stages, r)
	}
	base.StageSeconds += base.LoadSeconds
	base.WallSeconds = max(0, s.Wall.Seconds()-measuring)
	base.CoreOverheadSeconds = max(0, s.Overhead().Seconds())
	return base
}

// summarize adds up a run's samples. wall is how long they ran, start to end.
func summarize(res *Result, suite *Suite, wall time.Duration) *Summary {
	sum := &Summary{Presets: []PresetSummary{}, Stages: []StageSummary{}, Devices: []DeviceUse{},
		CoreOverheadBudgetMS: suite.CoreOverheadMS, WallSeconds: wall.Seconds()}
	var overhead []float64
	type stageKey struct{ preset, stage string }
	var order []stageKey
	byStage := map[stageKey][]StageResult{}
	busy, util := map[string]float64{}, map[string]float64{}
	measuredUtil := map[string]bool{}
	for _, preset := range res.Options.Presets {
		ps := PresetSummary{Preset: preset, BudgetSeconds: suite.Budgets[preset]}
		var secs []float64
		for _, j := range res.Jobs {
			if j.Preset != preset {
				continue
			}
			ps.Images++
			if j.Status != library.Succeeded {
				ps.Failed++
				continue
			}
			secs = append(secs, j.WallSeconds)
			if ps.BudgetSeconds > 0 && j.WallSeconds > ps.BudgetSeconds {
				ps.OverBudget++
			}
			overhead = append(overhead, j.CoreOverheadSeconds)
			for _, st := range j.Stages {
				if st.Status != library.Succeeded {
					continue
				}
				k := stageKey{preset, st.ID}
				if _, ok := byStage[k]; !ok {
					order = append(order, k)
				}
				byStage[k] = append(byStage[k], st)
				if st.Device != "" && st.Device != "cpu-pool" {
					busy[st.Device] += st.Seconds
					if st.GPUUtil != nil {
						util[st.Device] += *st.GPUUtil / 100 * st.Seconds
						measuredUtil[st.Device] = true
					}
				}
			}
		}
		ps.Seconds = spread(secs)
		sum.Presets = append(sum.Presets, ps)
	}
	sum.CoreOverhead = spread(overhead)
	for _, k := range order {
		sum.Stages = append(sum.Stages, stageSummary(k.preset, byStage[k]))
	}
	for _, dev := range slices.Sorted(maps.Keys(busy)) {
		d := DeviceUse{Device: dev, BusySeconds: round(busy[dev], 3)}
		if w := wall.Seconds(); w > 0 {
			d.Busy = round(min(1, busy[dev]/w), 3)
			if measuredUtil[dev] {
				u := round(min(1, util[dev]/w), 3)
				d.GPUUtil = &u
			}
		}
		sum.Devices = append(sum.Devices, d)
	}
	return sum
}

func stageSummary(preset string, runs []StageResult) StageSummary {
	first := runs[0]
	ss := StageSummary{Preset: preset, Stage: first.ID, Use: first.Use, Model: first.Model}
	var secs, loads, tris, cover []float64
	var utilSum, utilSecs float64
	watertight, meshes := 0, 0
	for _, r := range runs {
		secs = append(secs, r.Seconds)
		if r.LoadSeconds != nil {
			loads = append(loads, *r.LoadSeconds)
		}
		ss.PeakVRAMBytes = maxPtr(ss.PeakVRAMBytes, r.PeakVRAMBytes)
		ss.PeakRAMBytes = maxPtr(ss.PeakRAMBytes, r.PeakRAMBytes)
		if r.GPUUtil != nil {
			utilSum += *r.GPUUtil * r.Seconds
			utilSecs += r.Seconds
		}
		if len(r.Outputs) > 0 {
			o := r.Outputs[0]
			meshes++
			tris = append(tris, float64(o.Triangles))
			if o.Watertight {
				watertight++
			}
			if o.UVCoverage != nil {
				cover = append(cover, *o.UVCoverage)
			}
		}
	}
	ss.Seconds, ss.LoadSeconds = spread(secs), spread(loads)
	ss.Metrics = stageMetrics(runs)
	if utilSecs > 0 {
		ss.GPUUtil = ptr(round(utilSum/utilSecs, 1))
	}
	if meshes > 0 {
		ss.Triangles = ptr(spread(tris).Median)
		ss.Watertight = ptr(round(float64(watertight)/float64(meshes), 3))
	}
	if len(cover) > 0 {
		ss.UVCoverage = ptr(spread(cover).Median)
	}
	return ss
}

// stageMetrics sums up a stage's metrics over its samples. A metric some samples lack is summed
// over those that have it.
func stageMetrics(runs []StageResult) map[string]float64 {
	values := map[string][]float64{}
	for _, r := range runs {
		for k, v := range r.Metrics {
			values[k] = append(values[k], v)
		}
	}
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]float64, len(values))
	for k, vs := range values {
		if quality.DirectionOf(k) == quality.FewerIsBetter {
			out[k] = slices.Max(vs)
		} else {
			out[k] = spread(vs).Median
		}
	}
	return out
}

// spread uses the nearest rank, as the M0 report did (bench/m0/report.py).
func spread(xs []float64) Spread {
	if len(xs) == 0 {
		return Spread{}
	}
	s := slices.Clone(xs)
	sort.Float64s(s)
	at := func(q float64) float64 { return s[int(math.Round(q*float64(len(s)-1)))] }
	median := at(0.5)
	if len(s)%2 == 0 {
		median = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return Spread{N: len(s), Median: round(median, 4), P90: round(at(0.9), 4), Max: round(s[len(s)-1], 4)}
}

func round(x float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(x*p) / p
}

func ptr[T any](v T) *T { return &v }

func maxPtr(a, b *int64) *int64 {
	if b == nil || (a != nil && *a >= *b) {
		return a
	}
	return ptr(*b)
}
