package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"forja/internal/events"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/provider"
	"forja/internal/store"
)

// Events published on the hub; both carry the Result.
const (
	EvUpdated  = "bench.updated"
	EvFinished = "bench.finished"
)

var (
	// ErrBusy refuses a second benchmark while one runs: they would measure each other.
	ErrBusy     = errors.New("a benchmark is already running: let it finish or cancel it")
	ErrNotFound = errors.New("no such benchmark result")
)

// Request starts a run of a suite.
type Request struct {
	// Suite is the suite file, a full path.
	Suite string `json:"suite"`
	// Presets replace the suite's own when given (`forja bench --preset mobile-prop`).
	Presets []string `json:"presets,omitempty"`
	// Cold unloads the models before every image, so each sample pays its model loads. A warm
	// run, the default, first loads them with one image per preset that it does not measure.
	Cold bool `json:"cold,omitempty"`
	// Batch queues a preset's images together, as a folder dropped on Generate would, to
	// measure throughput and GPU utilization; otherwise one image runs at a time, so each
	// image's time is its own.
	Batch bool `json:"batch,omitempty"`
	// Repeat runs every image this many times (default 1).
	Repeat int    `json:"repeat,omitempty"`
	Label  string `json:"label,omitempty"`
}

// Bench runs suites through the job runner and keeps each result in Dir: a JSON file and
// an HTML report per run.
type Bench struct {
	Runner    *jobs.Runner
	Store     *store.Store
	Providers *provider.Registry
	Hub       *events.Hub
	Dir       string
	Version   string
	// Unload frees the models of this computer's engine, for cold runs.
	Unload func(ctx context.Context) error
	Log    *slog.Logger

	mu  sync.Mutex
	cur *run
}

type run struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	res    *Result
}

func (r *run) update(fn func(res *Result)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r.res)
}

// snapshot is a copy of the result that the run goes on changing.
func (r *run) snapshot() *Result {
	r.mu.Lock()
	raw, err := json.Marshal(r.res)
	r.mu.Unlock()
	var out Result
	if err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return &out
}

// Start checks a request and runs the suite in the background.
func (b *Bench) Start(ctx context.Context, req Request) (*Result, error) {
	if !filepath.IsAbs(req.Suite) {
		return nil, fmt.Errorf("suite %q: give the suite file's full path", req.Suite)
	}
	suite, err := LoadSuite(req.Suite)
	if err != nil {
		return nil, err
	}
	images, skipped := suite.Resolve()
	if len(images) == 0 {
		why := make([]string, len(skipped))
		for i, s := range skipped {
			why[i] = s.Path + ": " + s.Reason
		}
		return nil, fmt.Errorf("suite %s has no images to run here (%s)", suite.Name, strings.Join(why, "; "))
	}
	presets := req.Presets
	if len(presets) == 0 {
		presets = suite.Presets
	}
	provider, err := b.check(ctx, suite, presets)
	if err != nil {
		return nil, err
	}
	seed := 42
	if suite.Seed != nil {
		seed = *suite.Seed
	}
	repeat := max(1, req.Repeat)

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != nil {
		return nil, ErrBusy
	}
	runCtx, cancel := context.WithCancel(context.Background())
	r := &run{cancel: cancel, done: make(chan struct{}), res: &Result{
		Schema: ResultSchema, ID: ulid.Make().String(), Label: req.Label, Status: Running, Phase: "measuring",
		Suite:   SuiteRef{Name: suite.Name, Version: suite.Version, Path: suite.Path, Project: suite.Project},
		Options: Options{Presets: presets, Cold: req.Cold, Batch: req.Batch, Repeat: repeat, Seed: seed},
		Forja:   build(b.Version), Device: Device{Provider: provider}, StartedAt: time.Now(),
		Total: len(images) * len(presets) * repeat, Skipped: skipped, Jobs: []JobResult{},
	}}
	if !req.Cold {
		r.res.Phase = "warm-up"
	}
	b.cur = r
	go b.run(runCtx, r, suite, images)
	snap := r.snapshot()
	b.Hub.Publish(EvUpdated, snap)
	return snap, nil
}

// check refuses presets that are unknown or cannot run now, and says which provider runs the
// first model stage: its device is the one the run measures.
func (b *Bench) check(ctx context.Context, suite *Suite, presets []string) (string, error) {
	known, err := b.Runner.Presets(suite.Project)
	if err != nil {
		return "", err
	}
	device := ""
	for _, id := range presets {
		p, ok := known[id]
		if !ok {
			return "", fmt.Errorf("unknown preset %q", id)
		}
		picks, err := b.Runner.Resolve(ctx, suite.Project, p, nil)
		if err != nil {
			return "", fmt.Errorf("preset %s cannot run: %w", id, err)
		}
		for _, pick := range picks {
			if pick.Missing && !pick.Skipped {
				return "", fmt.Errorf("preset %s cannot run: no %s model is ready; pin one in the suite's project or install one",
					id, pick.Use)
			}
			if device == "" && pick.Model != "" {
				device = pick.Provider
			}
		}
	}
	if device == "" {
		device = jobs.LocalProvider
	}
	return device, nil
}

func (b *Bench) run(ctx context.Context, r *run, suite *Suite, images []Image) {
	release := b.Runner.Hold()
	defer release()
	opts := r.snapshot().Options
	b.describe(ctx, r)
	if !opts.Cold {
		for _, preset := range opts.Presets {
			if ctx.Err() != nil {
				break
			}
			// Loads the preset's models; what it measures is thrown away.
			if job, ch, err := b.submit(ctx, suite, images[0], preset, opts.Seed); err == nil {
				r.update(func(res *Result) { res.WarmUp = job.ID })
				b.Hub.Publish(EvUpdated, r.snapshot())
				b.wait(ctx, job, ch)
			}
		}
		r.update(func(res *Result) { res.Phase, res.WarmUp = "measuring", "" })
	}
	started := time.Now()
	for _, preset := range opts.Presets {
		if opts.Batch {
			b.batch(ctx, r, suite, images, preset, opts)
			continue
		}
		for rep := 1; rep <= opts.Repeat; rep++ {
			for _, img := range images {
				if ctx.Err() != nil {
					break
				}
				if opts.Cold {
					b.unload(ctx)
				}
				job, ch, at := b.begin(ctx, r, suite, img, preset, rep, opts.Seed)
				if job != nil {
					b.end(r, at, b.wait(ctx, job, ch))
				}
			}
		}
	}
	b.finish(ctx, r, suite, time.Since(started))
}

// batch queues every sample of a preset at once and waits for them all.
func (b *Bench) batch(ctx context.Context, r *run, suite *Suite, images []Image, preset string, opts Options) {
	if opts.Cold {
		b.unload(ctx)
	}
	type pending struct {
		job *library.Job
		ch  <-chan jobs.Sample
		at  int
	}
	var queued []pending
	for rep := 1; rep <= opts.Repeat; rep++ {
		for _, img := range images {
			if ctx.Err() != nil {
				break
			}
			if job, ch, at := b.begin(ctx, r, suite, img, preset, rep, opts.Seed); job != nil {
				queued = append(queued, pending{job, ch, at})
			}
		}
	}
	for _, p := range queued {
		b.end(r, p.at, b.wait(ctx, p.job, p.ch))
	}
}

func (b *Bench) submit(ctx context.Context, suite *Suite, img Image, preset string, seed int) (*library.Job, <-chan jobs.Sample, error) {
	return b.Runner.Measure(ctx, jobs.SubmitRequest{
		Path: img.Path, Name: "bench-" + img.Name, Preset: preset, Project: suite.Project,
		// A benchmark never holds up work someone is waiting for.
		Priority: "batch", Seed: &seed, Sheet: library.SheetKeep,
	})
}

// begin submits one sample and lists it in the result; at is its place there. A sample that
// cannot even be submitted is listed as failed, and job is nil.
func (b *Bench) begin(ctx context.Context, r *run, suite *Suite, img Image, preset string, rep, seed int,
) (*library.Job, <-chan jobs.Sample, int) {
	jr := JobResult{Image: img.Name, Path: img.Path, Category: img.Category, Preset: preset, Repeat: rep,
		Stages: []StageResult{}, Status: library.Running}
	job, ch, err := b.submit(ctx, suite, img, preset, seed)
	if err != nil {
		jr.Status, jr.Error = library.Failed, err.Error()
	} else {
		jr.Job, jr.Input = job.ID, job.Input
	}
	at := 0
	r.update(func(res *Result) {
		at = len(res.Jobs)
		res.Jobs = append(res.Jobs, jr)
	})
	b.Hub.Publish(EvUpdated, r.snapshot())
	return job, ch, at
}

func (b *Bench) end(r *run, at int, s jobs.Sample) {
	r.update(func(res *Result) { res.Jobs[at] = jobResult(res.Jobs[at], s) })
	b.Hub.Publish(EvUpdated, r.snapshot())
}

// wait waits for a sample to end; a cancelled run cancels it first.
func (b *Bench) wait(ctx context.Context, job *library.Job, ch <-chan jobs.Sample) jobs.Sample {
	select {
	case s := <-ch:
		return s
	case <-ctx.Done():
		b.Runner.Cancel(job.ID)
		return <-ch
	}
}

func (b *Bench) unload(ctx context.Context) {
	if b.Unload == nil {
		return
	}
	if err := b.Unload(ctx); err != nil {
		b.Log.Warn("could not unload the models before a cold sample", "err", err)
	}
}

// describe records the device the models run on and the engine's version, asking the
// provider (which may start this computer's engine).
func (b *Bench) describe(ctx context.Context, r *run) {
	id := r.snapshot().Device.Provider
	p, ok := b.Providers.Get(id)
	if !ok {
		return
	}
	ictx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	info, err := p.Info(ictx)
	if err != nil {
		b.Log.Warn("could not describe the benchmark's device", "provider", id, "err", err)
		return
	}
	r.update(func(res *Result) {
		res.Forja.Engine, res.Forja.API = info.Version, info.APIVersion
		res.Device = deviceOf(id, info.System)
	})
}

func (b *Bench) finish(ctx context.Context, r *run, suite *Suite, wall time.Duration) {
	now := time.Now()
	r.update(func(res *Result) {
		res.Phase, res.FinishedAt = "", &now
		res.Status = Succeeded
		for _, j := range res.Jobs {
			if j.Status != library.Succeeded {
				res.Status = Failed
			}
		}
		if ctx.Err() != nil {
			res.Status = Cancelled
		} else if len(res.Jobs) < res.Total {
			res.Status, res.Error = Failed, "the run stopped before every sample ran"
		}
		res.Summary = summarize(res, suite, wall)
		res.Report = filepath.Join(b.Dir, res.ID+".html")
	})
	res := r.snapshot()
	if err := b.save(res); err != nil {
		b.Log.Error("saving the benchmark result", "err", err)
		r.update(func(res *Result) { res.Report, res.Error = "", "could not save the result: "+err.Error() })
		res = r.snapshot()
	}
	b.mu.Lock()
	b.cur = nil
	b.mu.Unlock()
	close(r.done)
	b.Hub.Publish(EvFinished, res)
}

func (b *Bench) save(res *Result) error {
	if err := os.MkdirAll(b.Dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.Dir, res.ID+".json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	page, err := b.Report(res)
	if err != nil {
		return err
	}
	return os.WriteFile(res.Report, page, 0o644)
}

// Cancel stops the running benchmark with this id; false when none runs.
func (b *Bench) Cancel(id string) bool {
	b.mu.Lock()
	r := b.cur
	b.mu.Unlock()
	if r == nil || r.snapshot().ID != id {
		return false
	}
	r.cancel()
	return true
}

// Wait blocks until the running benchmark, if any, has finished: tests and shutdown.
func (b *Bench) Wait() {
	b.mu.Lock()
	r := b.cur
	b.mu.Unlock()
	if r != nil {
		<-r.done
	}
}

var idPattern = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// Get is a result by id, the running one included.
func (b *Bench) Get(id string) (*Result, error) {
	b.mu.Lock()
	r := b.cur
	b.mu.Unlock()
	if r != nil {
		if snap := r.snapshot(); snap.ID == id {
			return snap, nil
		}
	}
	if !idPattern.MatchString(id) {
		return nil, ErrNotFound
	}
	res, err := ReadFile(filepath.Join(b.Dir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return res, err
}

// Load is a result by id, or a result file by path: what Compare reads.
func (b *Bench) Load(ref string) (*Result, error) {
	if idPattern.MatchString(ref) {
		return b.Get(ref)
	}
	if !filepath.IsAbs(ref) {
		return nil, fmt.Errorf("%q is neither a result id nor a full path to a result file", ref)
	}
	return ReadFile(ref)
}

// ReadFile reads a result file, refusing other schema versions.
func ReadFile(path string) (*Result, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%s is not a benchmark result: %w", path, err)
	}
	if res.Schema != ResultSchema {
		return nil, fmt.Errorf("%s has result schema %d; this forja reads %d", path, res.Schema, ResultSchema)
	}
	return &res, nil
}

// Entry is a result as a list shows it.
type Entry struct {
	ID           string          `json:"id"`
	Label        string          `json:"label,omitempty"`
	Status       string          `json:"status"`
	Suite        SuiteRef        `json:"suite"`
	Options      Options         `json:"options"`
	Forja        Build           `json:"forja"`
	Device       Device          `json:"device"`
	StartedAt    time.Time       `json:"started_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	Samples      int             `json:"samples"`
	Failed       int             `json:"failed"`
	Presets      []PresetSummary `json:"presets"`
	CoreOverhead Spread          `json:"core_overhead"`
	Report       string          `json:"report,omitempty"`
}

// List is the running benchmark, if any, and the results kept, newest first.
type List struct {
	Running *Result `json:"running"`
	Results []Entry `json:"results"`
}

func (b *Bench) List() (List, error) {
	out := List{Results: []Entry{}}
	b.mu.Lock()
	if b.cur != nil {
		out.Running = b.cur.snapshot()
	}
	b.mu.Unlock()
	files, err := filepath.Glob(filepath.Join(b.Dir, "*.json"))
	if err != nil {
		return out, err
	}
	for _, f := range files {
		res, err := ReadFile(f)
		if err != nil {
			b.Log.Warn("skipping a benchmark result", "file", f, "err", err)
			continue
		}
		e := Entry{ID: res.ID, Label: res.Label, Status: res.Status, Suite: res.Suite, Options: res.Options,
			Forja: res.Forja, Device: res.Device, StartedAt: res.StartedAt, FinishedAt: res.FinishedAt,
			Samples: len(res.Jobs), Presets: []PresetSummary{}, Report: res.Report}
		for _, j := range res.Jobs {
			if j.Status != library.Succeeded {
				e.Failed++
			}
		}
		if res.Summary != nil {
			e.Presets, e.CoreOverhead = res.Summary.Presets, res.Summary.CoreOverhead
		}
		out.Results = append(out.Results, e)
	}
	sort.Slice(out.Results, func(i, j int) bool { return out.Results[i].StartedAt.After(out.Results[j].StartedAt) })
	return out, nil
}

// build is what this core is: its version, and the commit it was built from when the build
// recorded one (`go build` inside a checkout does).
func build(version string) Build {
	b := Build{Version: version}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				b.Commit = s.Value
			case "vcs.modified":
				b.Modified = s.Value == "true"
			}
		}
	}
	return b
}

// deviceOf reads the device a provider's models run on from its system report: its first
// GPU, else its CPU.
func deviceOf(provider string, system map[string]any) Device {
	d := Device{Provider: provider}
	d.OS, _ = system["os"].(string)
	d.Arch, _ = system["arch"].(string)
	if ram, ok := system["ram_total_bytes"].(float64); ok {
		d.RAMBytes = int64(ram)
	}
	list, _ := system["devices"].([]any)
	var gpu map[string]any
	for _, raw := range list {
		dev, _ := raw.(map[string]any)
		if dev == nil {
			continue
		}
		if dev["backend"] == "cpu" {
			d.CPU, _ = dev["name"].(string)
		} else if gpu == nil {
			gpu = dev
		}
	}
	if gpu != nil {
		d.GPU, _ = gpu["name"].(string)
		d.Backend, _ = gpu["backend"].(string)
		d.Driver, _ = gpu["driver"].(string)
		if v, ok := gpu["vram_bytes"].(float64); ok {
			d.VRAMBytes = int64(v)
		}
		d.ID = slug(d.GPU)
	} else {
		d.Backend = "cpu"
		d.ID = slug("cpu " + d.CPU)
	}
	if d.ID == "" {
		d.ID = "unknown"
	}
	return d
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
