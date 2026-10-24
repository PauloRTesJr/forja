// Package jobs owns a job from submission to export: plan, cache lookups, routing,
// provider runs, cache records and the finished asset.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/export"
	"forja/internal/library"
	"forja/internal/paths"
	"forja/internal/planner"
	"forja/internal/presets"
	"forja/internal/provider"
	"forja/internal/store"
)

const LocalProvider = "local"

// Event types published on the hub.
const (
	EvJobCreated    = "job.created"
	EvJobUpdated    = "job.updated"
	EvStageProgress = "stage.progress"
	EvArtifact      = "artifact.ready"
	EvJobFinished   = "job.finished"
	EvAssetCreated  = "asset.created"
)

type SubmitRequest struct {
	// Exactly one of Path and Input: the front view of the subject.
	Path  string `json:"path,omitempty"`
	Input string `json:"input,omitempty"`
	// Views are other views of the same subject ("left", "back", "right"), each a file path or
	// a "blake3:" ref. They reach the stages whose model reads them.
	Views map[string]string `json:"views,omitempty"`
	// Sheet says what to do when the image holds several views of its subject side by side:
	// library.SheetAsk, SheetSplit or SheetKeep (the default). Ignored when Views are given.
	Sheet     string         `json:"sheet,omitempty"`
	Name      string         `json:"name,omitempty"`
	Preset    string         `json:"preset,omitempty"`
	Project   string         `json:"project,omitempty"`
	Priority  string         `json:"priority,omitempty"`
	Seed      *int           `json:"seed,omitempty"`
	Overrides map[string]any `json:"overrides,omitempty"`
}

// SheetAnswer answers a job waiting on a sheet: the views to cut out of it, or Keep to use
// the image whole.
type SheetAnswer struct {
	Keep  bool                `json:"keep,omitempty"`
	Views []library.SheetPart `json:"views,omitempty"`
}

// errWaiting ends a run that needs the user's answer before it can go on.
var errWaiting = errors.New("waiting for an answer")

type Runner struct {
	db        *library.DB
	store     *store.Store
	hub       *events.Hub
	providers *provider.Registry
	config    func(project string) (*config.Loaded, error)
	log       *slog.Logger
	version   string

	// Bounds jobs in flight so the engine has work to batch by model without the core
	// holding hundreds of open event streams.
	slots chan struct{}
	// Bounds the jobs looking for a sheet of views, which runs before a job takes a slot.
	looking chan struct{}

	// catalog is what each provider runs: it keys stages and picks models.
	catalog *catalog.Catalog

	mu     sync.Mutex
	active map[string]*active
	// claimed are waiting jobs being answered or cancelled, so only one of those happens.
	claimed map[string]bool

	// gate keeps eviction away from running jobs: each run holds it shared, and WhenIdle
	// takes it only when no run does.
	gate sync.RWMutex
	// OnIdle, when set, is called after the last running job ends.
	OnIdle func()

	// meters are the paid batches running now, with what they owe so far (under mu).
	meters map[*meter]bool
}

type active struct {
	cancel context.CancelFunc
	job    *library.Job
	mu     sync.Mutex
	// picks are where each stage runs now, owed what its paid stages are expected to cost
	// until they are billed, and avoid the providers stages failed on. Under mu.
	picks map[string]Pick
	owed  map[string]float64
	avoid failed
}

// set takes on how a job runs now.
func (a *active) set(prep *prepared) {
	a.mu.Lock()
	a.picks, a.owed = prep.picks, prep.owed
	a.mu.Unlock()
}

func NewRunner(db *library.DB, st *store.Store, hub *events.Hub, providers *provider.Registry, cat *catalog.Catalog,
	cfg func(project string) (*config.Loaded, error), log *slog.Logger, version string, maxInFlight int,
) *Runner {
	return &Runner{
		db: db, store: st, hub: hub, providers: providers, catalog: cat, config: cfg, log: log, version: version,
		slots: make(chan struct{}, maxInFlight), looking: make(chan struct{}, max(1, maxInFlight/4)),
		active: map[string]*active{}, claimed: map[string]bool{}, meters: map[*meter]bool{},
	}
}

// Config loads the configuration a job in project would see.
func (r *Runner) Config(project string) (*config.Loaded, error) { return r.config(project) }

// WhenIdle runs fn only while no job runs, holding off jobs that start meanwhile, and says
// whether it ran. Eviction uses it so it never removes what a running job reads.
func (r *Runner) WhenIdle(fn func()) bool {
	if !r.gate.TryLock() {
		return false
	}
	defer r.gate.Unlock()
	fn()
	return true
}

// Busy reports whether any job is queued or running.
func (r *Runner) Busy() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active) > 0
}

func (r *Runner) Presets(project string) (map[string]*presets.Preset, error) {
	dirs := []string{filepath.Join(paths.ConfigDir(), "presets")}
	if project != "" {
		dirs = append(dirs, filepath.Join(project, "forja", "presets"))
	}
	return presets.Load(dirs...)
}

// Submit validates, plans and persists a job, then runs it in the background.
func (r *Runner) Submit(ctx context.Context, req SubmitRequest) (*library.Job, error) {
	project := req.Project
	if project != "" {
		project, _ = filepath.Abs(project)
	}
	cfg, err := r.config(project)
	if err != nil {
		return nil, err
	}
	input := req.Input
	name := req.Name
	if req.Path != "" {
		if input, err = r.store.PutFile(req.Path); err != nil {
			return nil, fmt.Errorf("reading %s: %w", req.Path, err)
		}
		if name == "" {
			name = req.Path
		}
	}
	if input == "" || !r.store.Has(input) {
		return nil, fmt.Errorf("input %q is not in the object store", input)
	}
	views, err := r.putViews(req.Views)
	if err != nil {
		return nil, err
	}
	mode := req.Sheet
	switch mode {
	case "":
		mode = library.SheetKeep
	case library.SheetAsk, library.SheetSplit, library.SheetKeep:
	default:
		return nil, fmt.Errorf("sheet %q: want ask, split or keep", req.Sheet)
	}
	if name == "" {
		name = "asset"
	}
	presetID := req.Preset
	if presetID == "" {
		presetID = cfg.Project.DefaultPreset
	}
	seed := 42
	if req.Seed != nil {
		seed = *req.Seed
	}
	priority := req.Priority
	if priority != "interactive" {
		priority = "batch"
	}
	job := &library.Job{
		ID: ulid.Make().String(), Name: export.Slug(name), Preset: presetID, Input: input, Views: views, Seed: seed,
		Project: project, Priority: priority, Overrides: req.Overrides, Status: library.Queued, CreatedAt: time.Now(),
	}
	if len(views) == 0 {
		job.Sheet = &library.Sheet{Mode: mode}
	}
	prep, err := r.prepare(ctx, cfg, job, nil)
	if err != nil {
		return nil, err
	}
	r.setStages(ctx, job, prep)
	if err := r.db.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	a := r.start(job, prep, cfg)
	r.hub.Publish(EvJobCreated, snapshot(a))
	return snapshot(a), nil
}

// start makes a job active and runs it in the background.
func (r *Runner) start(job *library.Job, prep *prepared, cfg *config.Loaded) *active {
	runCtx, cancel := context.WithCancel(context.Background())
	a := &active{cancel: cancel, job: job, avoid: failed{}}
	a.set(prep)
	r.mu.Lock()
	r.active[job.ID] = a
	r.mu.Unlock()
	go r.run(runCtx, a, prep, cfg)
	return a
}

// putViews stores a job's other views and returns their refs by view.
func (r *Runner) putViews(views map[string]string) (map[string]string, error) {
	if len(views) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(views))
	for v, src := range views {
		if v == "front" || !slices.Contains(engineapi.Views, v) {
			return nil, fmt.Errorf("view %q: want left, back or right (the front is the job's image)", v)
		}
		ref := src
		if !strings.HasPrefix(src, "blake3:") {
			var err error
			if ref, err = r.store.PutFile(src); err != nil {
				return nil, fmt.Errorf("reading the %s view %s: %w", v, src, err)
			}
		}
		if !r.store.Has(ref) {
			return nil, fmt.Errorf("the %s view %q is not in the object store", v, ref)
		}
		out[v] = ref
	}
	return out, nil
}

// prepared is how a job runs: its preset with a model on every stage, the stages that bring
// its other views, and the provider of each stage.
type prepared struct {
	preset *presets.Preset
	routes map[string]string
	// picks are the stages' picks by stage id, and owed what each paid one is expected to cost.
	picks map[string]Pick
	owed  map[string]float64
}

// prepare resolves a job's preset against what the providers run now. A job whose views
// change (cut out of a sheet) is prepared again, since its views can change the models picked,
// and so is one whose stage fails over to another provider. avoid holds the providers its
// stages failed on.
func (r *Runner) prepare(ctx context.Context, cfg *config.Loaded, job *library.Job, avoid failed) (*prepared, error) {
	all, err := r.Presets(job.Project)
	if err != nil {
		return nil, err
	}
	base, ok := all[job.Preset]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", job.Preset)
	}
	overrides := map[string]any{}
	if po, ok := cfg.Presets[job.Preset]; ok {
		for k, v := range po.Overrides {
			overrides[k] = v
		}
	}
	for k, v := range job.Overrides {
		overrides[k] = v
	}
	withOverrides, err := base.WithOverrides(overrides)
	if err != nil {
		return nil, err
	}
	views := ordered(job.SideViews())
	picks, preset, err := r.resolve(ctx, cfg, withOverrides.Active(), views, avoid)
	if err != nil {
		return nil, err
	}
	if err := missingError(picks); err != nil {
		return nil, err
	}
	for _, p := range picks {
		if len(p.NeedsViews) > 0 {
			return nil, fmt.Errorf("%s needs the %s view; add it or choose another %s model",
				p.ModelName, strings.Join(p.NeedsViews, " and "), p.Use)
		}
	}
	if err := r.withinBudget(ctx, cfg, picks, job.ID); err != nil {
		return nil, err
	}
	byStage := map[string]Pick{}
	for _, p := range picks {
		byStage[p.Stage] = p
	}
	routes := map[string]string{}
	for i := range preset.Stages {
		p := byStage[preset.Stages[i].ID]
		preset.Stages[i].Model = p.Model
		routes[preset.Stages[i].ID] = p.Provider
	}
	if len(views) > 0 {
		preset = preset.WithViews(views, func(s presets.Stage) []string { return byStage[s.ID].Views })
		// A view's own background removal and framing run where the front's do.
		for _, s := range preset.Stages {
			for _, v := range views {
				if orig, ok := strings.CutSuffix(s.ID, "-"+v); ok && routes[s.ID] == "" {
					routes[s.ID] = routes[orig]
					copied := byStage[orig]
					copied.Stage = s.ID
					byStage[s.ID] = copied
				}
			}
		}
	}
	prep := &prepared{preset: preset, routes: routes, picks: map[string]Pick{}, owed: map[string]float64{}}
	for _, s := range preset.Stages {
		p := byStage[s.ID]
		prep.picks[s.ID] = p
		if c := r.chargeOf(ctx, cfg, p); c.USD() > 0 {
			prep.owed[s.ID] = c.USD()
		}
	}
	return prep, nil
}

// setStages lists the stages a job will run, all queued.
func (r *Runner) setStages(ctx context.Context, j *library.Job, prep *prepared) {
	j.Stages, j.EstimateSec = nil, 0
	for _, s := range prep.preset.Stages {
		j.Stages = append(j.Stages, library.Stage{
			ID: s.ID, Use: s.Use, Model: s.Model, Status: library.Queued, Provider: prep.routes[s.ID],
			Outputs: []library.Output{},
		})
		j.EstimateSec += r.db.EstimateSeconds(ctx, s.Use, s.Model, prep.routes[s.ID])
	}
}

// ordered puts views in the order of engineapi.Views.
func ordered(views []string) []string {
	var out []string
	for _, v := range engineapi.Views {
		if slices.Contains(views, v) {
			out = append(out, v)
		}
	}
	return out
}

// inputs maps what a job's stages read to its images.
func inputs(j *library.Job) map[string]string {
	in := map[string]string{presets.InputImage: j.Front()}
	for v, ref := range j.Views {
		if v != "front" {
			in[presets.InputView(v)] = ref
		}
	}
	return in
}

// fingerprints asks each provider the job routes to what it runs, once per job: keys depend on
// it, so a new adapter or new weights on that provider never reuse outputs cached before.
func (r *Runner) fingerprints(ctx context.Context, routes map[string]string) planner.Fingerprints {
	asked := map[string]*catalog.Entry{}
	return func(s presets.Stage) (planner.Fingerprint, error) {
		id := routes[s.ID]
		got, ok := asked[id]
		if !ok {
			var err error
			if got, err = r.askRuns(ctx, id); err != nil {
				// Nothing ran there yet in this job (it would have answered), so every stage it
				// was to run may move.
				var on []string
				for stage, p := range routes {
					if p == id {
						on = append(on, stage)
					}
				}
				return planner.Fingerprint{}, &stageError{provider: id, stages: on, down: true, err: err}
			}
			asked[id] = got
		}
		if s.Model == "" {
			return planner.Fingerprint{Adapter: got.Stages[s.Use]}, nil
		}
		m, ok := got.Model(s.Model)
		if !ok {
			return planner.Fingerprint{}, fmt.Errorf("provider %s has no model %q", id, s.Model)
		}
		return planner.Fingerprint{Adapter: m.AdapterVersion, Weights: m.Weights.Revision, Upstream: m.Upstream}, nil
	}
}

// askRuns asks a provider what it runs. One that cannot answer now (a LAN box that is off) is
// keyed by its last answer in this session, so a job whose stages are all cached still finishes.
func (r *Runner) askRuns(ctx context.Context, id string) (*catalog.Entry, error) {
	e, err := r.catalog.Ask(ctx, id)
	if err == nil {
		return e, nil
	}
	if last, ok := r.catalog.Live(id); ok && ctx.Err() == nil {
		r.log.Warn("keying stages with the provider's last answer", "provider", id, "err", err)
		return last, nil
	}
	return nil, err
}

func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	a, ok := r.active[id]
	r.mu.Unlock()
	if ok {
		a.cancel()
		return true
	}
	// A waiting job runs nowhere: cancelling it only records that.
	if !r.claim(id) {
		return false
	}
	defer r.unclaim(id)
	ctx := context.Background()
	job, err := r.db.Job(ctx, id)
	if err != nil || job.Status != library.Waiting {
		return false
	}
	now := time.Now()
	job.Status, job.Error, job.FinishedAt = library.Cancelled, "cancelled", &now
	for i := range job.Stages {
		if !job.Stages[i].Status.Done() {
			job.Stages[i].Status = library.Cancelled
		}
	}
	if err := r.db.SaveJob(ctx, job); err != nil {
		r.log.Error("saving job", "job", id, "err", err)
		return false
	}
	r.hub.Publish(EvJobFinished, job)
	return true
}

// claim reserves a job that is not active, so one answer or cancel acts on it at a time.
func (r *Runner) claim(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[id] != nil || r.claimed[id] {
		return false
	}
	r.claimed[id] = true
	return true
}

func (r *Runner) unclaim(id string) {
	r.mu.Lock()
	delete(r.claimed, id)
	r.mu.Unlock()
}

// AnswerSheet resumes a job waiting on a sheet, with the views to cut out of it or as one
// image. Models are picked again: what is installed may have changed while it waited.
func (r *Runner) AnswerSheet(ctx context.Context, id string, ans SheetAnswer) (*library.Job, error) {
	if !r.claim(id) {
		return nil, fmt.Errorf("job %s is not waiting for an answer", id)
	}
	defer r.unclaim(id)
	job, err := r.db.Job(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status != library.Waiting || job.Sheet == nil || !job.Sheet.Found {
		return nil, fmt.Errorf("job %s is not waiting for an answer", id)
	}
	if ans.Keep {
		job.Sheet.Answer, job.Sheet.Chosen = library.SheetKeep, nil
	} else {
		if err := checkViews(ans.Views, job.Sheet.Size); err != nil {
			return nil, err
		}
		job.Sheet.Answer, job.Sheet.Chosen = library.SheetSplit, ans.Views
	}
	cfg, err := r.config(job.Project)
	if err != nil {
		return nil, err
	}
	prep, err := r.prepare(ctx, cfg, job, nil)
	if err != nil {
		return nil, err
	}
	job.Status, job.Error = library.Queued, ""
	r.setStages(ctx, job, prep)
	if err := r.db.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	a := r.start(job, prep, cfg)
	r.hub.Publish(EvJobUpdated, snapshot(a))
	return snapshot(a), nil
}

// checkViews accepts a front and at most one part per view, each inside the sheet.
func checkViews(parts []library.SheetPart, size [2]int) error {
	seen := map[string]bool{}
	for _, p := range parts {
		if !slices.Contains(engineapi.Views, p.View) {
			return fmt.Errorf("view %q: want front, left, back or right", p.View)
		}
		if seen[p.View] {
			return fmt.Errorf("the %s view is chosen twice", p.View)
		}
		seen[p.View] = true
		x0, y0, x1, y1 := p.Box[0], p.Box[1], p.Box[2], p.Box[3]
		if x0 < 0 || y0 < 0 || x1 <= x0 || y1 <= y0 || x1 > size[0] || y1 > size[1] {
			return fmt.Errorf("the %s view's box %v is outside the %dx%d image", p.View, p.Box, size[0], size[1])
		}
	}
	if !seen["front"] {
		return errors.New("choose which part is the front view")
	}
	return nil
}

// Job returns the live state of an active job, falling back to the library.
func (r *Runner) Job(ctx context.Context, id string) (*library.Job, error) {
	r.mu.Lock()
	a, ok := r.active[id]
	r.mu.Unlock()
	if ok {
		return snapshot(a), nil
	}
	return r.db.Job(ctx, id)
}

func snapshot(a *active) *library.Job {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := *a.job
	cp.Stages = append([]library.Stage(nil), a.job.Stages...)
	for i := range cp.Stages {
		cp.Stages[i].Outputs = append([]library.Output{}, a.job.Stages[i].Outputs...)
	}
	cp.Views = maps.Clone(a.job.Views)
	if a.job.Sheet != nil {
		sh := *a.job.Sheet
		sh.Parts, sh.Chosen = slices.Clone(sh.Parts), slices.Clone(sh.Chosen)
		cp.Sheet = &sh
	}
	return &cp
}

func (r *Runner) update(a *active, fn func(j *library.Job)) {
	a.mu.Lock()
	fn(a.job)
	a.mu.Unlock()
}

func (r *Runner) run(ctx context.Context, a *active, prep *prepared, cfg *config.Loaded) {
	r.gate.RLock()
	defer r.ended()
	defer r.leave(a)
	plan, err := r.runJob(ctx, a, prep, cfg)
	switch {
	case errors.Is(err, errWaiting):
		r.wait(a)
	case errors.Is(err, context.Canceled):
		r.finish(a, library.Cancelled, "cancelled")
	case err != nil:
		r.finish(a, library.Failed, err.Error())
	default:
		if err := r.createAsset(context.Background(), a, plan, cfg); err != nil {
			r.finish(a, library.Failed, "export: "+err.Error())
			return
		}
		r.finish(a, library.Succeeded, "")
	}
}

// runJob looks for a sheet of views first when the job asks for that, then takes a job slot,
// cuts the views out once they are chosen, and runs the plan.
func (r *Runner) runJob(ctx context.Context, a *active, prep *prepared, cfg *config.Loaded) (*planner.Plan, error) {
	if sh := snapshot(a).Sheet; sh != nil && !sh.Checked {
		if err := r.withFallback(ctx, a, cfg, &prep, func(p *prepared) error { return r.look(ctx, a, p) }); err != nil {
			return nil, err
		}
		sh := snapshot(a).Sheet
		if sh.Found && sh.Answer == "" && sh.Mode == library.SheetAsk {
			return nil, errWaiting
		}
	}
	release, err := r.slot(ctx, a)
	if err != nil {
		return nil, err
	}
	defer release()
	if job := snapshot(a); job.Sheet != nil && job.Sheet.Answer == library.SheetSplit && job.Views["front"] == "" {
		if err := r.withFallback(ctx, a, cfg, &prep, func(p *prepared) error { return r.split(ctx, a, p) }); err != nil {
			return nil, err
		}
		var err error
		if prep, err = r.prepare(ctx, cfg, snapshot(a), a.avoid); err != nil {
			return nil, err
		}
		r.update(a, func(j *library.Job) {
			before := slices.Clone(j.Stages)
			r.setStages(ctx, j, prep)
			for _, o := range before {
				if s := stage(j, o.ID); s != nil {
					s.Fallbacks = o.Fallbacks
				}
			}
		})
		a.set(prep)
		r.hub.Publish(EvJobUpdated, snapshot(a))
	}
	// Keyed here, not at submit: asking a provider what it runs may start the local engine.
	var plan *planner.Plan
	err = r.withFallback(ctx, a, cfg, &prep, func(p *prepared) error {
		var err error
		if plan, err = r.plan(ctx, p.preset, p.routes, snapshot(a)); err != nil {
			return err
		}
		_, err = r.execute(ctx, a, plan)
		return err
	})
	return plan, err
}

// withFallback runs step with how the job runs now. When a provider is down or runs out of
// memory, the stages it failed go to the next place that can run them and step runs again,
// until it succeeds or no provider can take over.
func (r *Runner) withFallback(ctx context.Context, a *active, cfg *config.Loaded, prep **prepared, step func(*prepared) error) error {
	for {
		err := step(*prep)
		if err == nil {
			return nil
		}
		next, ok := r.fallback(ctx, a, cfg, *prep, err)
		if !ok {
			return err
		}
		*prep = next
	}
}

// look looks for a sheet of views under its own limit, not a job slot: the app asks about a
// sheet while the user is still on the screen they dropped it on, so finding one must not wait
// for other jobs to finish whole runs.
func (r *Runner) look(ctx context.Context, a *active, prep *prepared) error {
	select {
	case r.looking <- struct{}{}:
		defer func() { <-r.looking }()
	case <-ctx.Done():
		return ctx.Err()
	}
	r.setStatus(a, library.Running)
	err := r.inspect(ctx, a, prep)
	r.hub.Publish(EvJobUpdated, snapshot(a))
	return err
}

// slot waits for one of the job slots, which bound the jobs in flight; a job shows as queued
// only while it waits for one.
func (r *Runner) slot(ctx context.Context, a *active) (func(), error) {
	select {
	case r.slots <- struct{}{}:
	default:
		r.setStatus(a, library.Queued)
		select {
		case r.slots <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.setStatus(a, library.Running)
	return func() { <-r.slots }, nil
}

func (r *Runner) setStatus(a *active, s library.Status) {
	if snapshot(a).Status == s {
		return
	}
	r.update(a, func(j *library.Job) { j.Status = s })
	r.hub.Publish(EvJobUpdated, snapshot(a))
}

func (r *Runner) plan(ctx context.Context, preset *presets.Preset, routes map[string]string, job *library.Job) (*planner.Plan, error) {
	plan, err := planner.Build(preset, inputs(job), job.Seed, r.fingerprints(ctx, routes))
	if err != nil {
		return nil, err
	}
	for i := range plan.Stages {
		plan.Stages[i].Provider = routes[plan.Stages[i].ID]
	}
	return plan, nil
}

// sheetStage is the job's background removal, when the views of a sheet can be found after
// it: it reads the image, and its provider has the views stages.
func (r *Runner) sheetStage(ctx context.Context, prep *prepared) (presets.Stage, bool) {
	if len(prep.preset.Stages) == 0 {
		return presets.Stage{}, false
	}
	bg := prep.preset.Stages[0]
	if bg.Use != "background-removal" || len(bg.Inputs) != 1 || bg.Inputs[0] != presets.InputImage {
		return presets.Stage{}, false
	}
	e, err := r.catalog.Get(ctx, prep.routes[bg.ID])
	if err != nil {
		return presets.Stage{}, false
	}
	_, detect := e.Stages["views.detect"]
	_, split := e.Stages["views.split"]
	return bg, detect && split
}

// inspect looks for several views of one subject in the job's image. It runs the job's own
// background removal (so the job then finds it cached) and views.detect on the cut-out.
func (r *Runner) inspect(ctx context.Context, a *active, prep *prepared) error {
	bg, ok := r.sheetStage(ctx, prep)
	if !ok {
		r.update(a, func(j *library.Job) { j.Sheet.Checked = true })
		return nil
	}
	detect := presets.Stage{ID: "sheet", Use: "views.detect", Inputs: []string{bg.ID}}
	routes := map[string]string{bg.ID: prep.routes[bg.ID], detect.ID: prep.routes[bg.ID]}
	plan, err := r.plan(ctx, &presets.Preset{ID: prep.preset.ID, Stages: []presets.Stage{bg, detect}}, routes, snapshot(a))
	if err != nil {
		return err
	}
	outs, err := r.execute(ctx, a, plan)
	if err != nil {
		return err
	}
	var found struct {
		Sheet bool                `json:"sheet"`
		Size  [2]int              `json:"size"`
		Parts []library.SheetPart `json:"parts"`
	}
	if err := r.readJSON(outs[detect.ID], &found); err != nil {
		return fmt.Errorf("reading what views.detect found: %w", err)
	}
	r.update(a, func(j *library.Job) {
		sh := j.Sheet
		sh.Checked, sh.Found, sh.Cutout, sh.Size, sh.Parts = true, found.Sheet, outs[bg.ID][0].Hash, found.Size, found.Parts
		if !found.Sheet || sh.Answer != "" {
			return
		}
		switch sh.Mode {
		case library.SheetSplit:
			sh.Answer = library.SheetSplit
			for _, p := range found.Parts {
				if p.View != "" {
					sh.Chosen = append(sh.Chosen, p)
				}
			}
		case library.SheetKeep:
			sh.Answer = library.SheetKeep
		}
	})
	return nil
}

func (r *Runner) readJSON(outs []library.Output, v any) error {
	if len(outs) == 0 {
		return errors.New("no output")
	}
	f, err := r.store.Open(outs[0].Hash)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(v)
}

// split cuts the chosen views out of the job's sheet; they become the job's views, the front
// among them, so the rest of the job runs as if they had been given one by one.
func (r *Runner) split(ctx context.Context, a *active, prep *prepared) error {
	bg, ok := r.sheetStage(ctx, prep)
	if !ok {
		return fmt.Errorf("provider %s cannot cut views out of a sheet; update its engine", prep.routes[prep.preset.Stages[0].ID])
	}
	chosen := snapshot(a).Sheet.Chosen
	wanted := make([]map[string]any, len(chosen))
	for i, p := range chosen {
		wanted[i] = map[string]any{"view": p.View, "box": p.Box[:]}
	}
	// The sheet's cut-out, cached while looking for the views. Under another id it leaves the
	// job's own background removal alone: that one will read the front once it is cut out.
	where := prep.routes[bg.ID]
	bg.ID = "sheet-bg"
	cut := presets.Stage{ID: "split", Use: "views.split", Inputs: []string{presets.InputImage, bg.ID},
		Params: map[string]any{"views": wanted}}
	routes := map[string]string{bg.ID: where, cut.ID: where}
	plan, err := r.plan(ctx, &presets.Preset{ID: prep.preset.ID, Stages: []presets.Stage{bg, cut}}, routes, snapshot(a))
	if err != nil {
		return err
	}
	outs, err := r.execute(ctx, a, plan)
	if err != nil {
		return err
	}
	if len(outs[cut.ID]) != len(chosen) {
		return fmt.Errorf("views.split made %d images for %d views", len(outs[cut.ID]), len(chosen))
	}
	views := make(map[string]string, len(chosen))
	for i, p := range chosen {
		views[p.View] = outs[cut.ID][i].Hash
	}
	r.update(a, func(j *library.Job) { j.Views = views })
	return nil
}

// ended lets eviction in once the last running job is gone.
func (r *Runner) ended() {
	r.gate.RUnlock()
	if r.OnIdle != nil && !r.Busy() {
		r.OnIdle()
	}
}

// leave drops a run from the active jobs, unless an answer already started the job again.
func (r *Runner) leave(a *active) {
	r.mu.Lock()
	if r.active[a.job.ID] == a {
		delete(r.active, a.job.ID)
	}
	r.mu.Unlock()
}

// wait parks a job until the user answers; it keeps no slot and survives restarts. It is no
// longer active by the time anyone hears it waits, so an answer can resume it at once.
func (r *Runner) wait(a *active) {
	r.update(a, func(j *library.Job) { j.Status = library.Waiting })
	snap := snapshot(a)
	if err := r.db.SaveJob(context.Background(), snap); err != nil {
		r.log.Error("saving job", "job", snap.ID, "err", err)
	}
	r.leave(a)
	r.hub.Publish(EvJobUpdated, snap)
}

func (r *Runner) finish(a *active, status library.Status, msg string) {
	now := time.Now()
	r.update(a, func(j *library.Job) {
		j.Status, j.Error, j.FinishedAt = status, msg, &now
		for i := range j.Stages {
			if !j.Stages[i].Status.Done() {
				j.Stages[i].Status = library.Cancelled
			}
		}
	})
	snap := snapshot(a)
	if err := r.db.SaveJob(context.Background(), snap); err != nil {
		r.log.Error("saving job", "job", snap.ID, "err", err)
	}
	r.hub.Publish(EvJobFinished, snap)
}

// stage is the job's stage with id, or nil for a stage the job does not list (the steps that
// look for a sheet of views).
func stage(j *library.Job, id string) *library.Stage {
	for i := range j.Stages {
		if j.Stages[i].ID == id {
			return &j.Stages[i]
		}
	}
	return nil
}

// execute resolves cache hits, then runs the remaining stages in batches of consecutive
// stages that share a provider, so intermediates stay on that provider. It returns every
// stage's outputs.
func (r *Runner) execute(ctx context.Context, a *active, plan *planner.Plan) (map[string][]library.Output, error) {
	outputs := map[string]string{} // input name or stage id -> primary output ref
	for name, ref := range plan.Inputs {
		outputs[name] = ref
	}
	all := map[string][]library.Output{}
	var pending []int
	for i, s := range plan.Stages {
		outs, hit, err := r.db.CacheGet(ctx, s.Key)
		if err != nil {
			return nil, err
		}
		if hit && len(outs) > 0 && r.store.Has(outs[0].Hash) {
			for _, o := range outs {
				r.store.Touch(o.Hash) // used now: eviction removes the least recently used
			}
			outputs[s.ID], all[s.ID] = outs[0].Hash, outs
			r.update(a, func(j *library.Job) {
				st := stage(j, s.ID)
				if st == nil {
					return
				}
				// Run earlier in this job (while looking for a sheet), not cached before it.
				ranHere := st.Status == library.Succeeded && st.Key == s.Key
				st.Status, st.Key, st.Outputs = library.Succeeded, s.Key, outs
				st.Cached = st.Cached || !ranHere
			})
			continue
		}
		if hit {
			_ = r.db.CacheForget(ctx, s.Key)
		}
		r.update(a, func(j *library.Job) {
			if st := stage(j, s.ID); st != nil {
				st.Key = s.Key
			}
		})
		pending = append(pending, i)
	}
	r.hub.Publish(EvJobUpdated, snapshot(a))

	for start := 0; start < len(pending); {
		end := start + 1
		prov := plan.Stages[pending[start]].Provider
		for end < len(pending) && plan.Stages[pending[end]].Provider == prov {
			end++
		}
		if err := r.runBatch(ctx, a, plan, pending[start:end], outputs, all); err != nil {
			return nil, err
		}
		start = end
	}
	return all, nil
}

func (r *Runner) runBatch(ctx context.Context, a *active, plan *planner.Plan, idx []int, outputs map[string]string,
	all map[string][]library.Output,
) error {
	p, _ := r.providers.Get(plan.Stages[idx[0]].Provider)
	inBatch := map[string]bool{}
	for _, i := range idx {
		inBatch[plan.Stages[i].ID] = true
	}
	// The engine answers a repeated client id with the run it already has, so a retry is safe.
	// The first stage's key tells apart batches that start with the same stage: background
	// removal runs on the sheet while looking for views, then on the front cut out of it.
	first := plan.Stages[idx[0]]
	req := engineapi.RunRequest{
		ClientRunID: a.job.ID + "-" + first.ID + "-" + strings.TrimPrefix(first.Key, "stage:")[:8],
		Priority:    a.job.Priority, Seed: plan.Seed,
	}
	for _, i := range idx {
		s := plan.Stages[i]
		spec := engineapi.StageSpec{ID: s.ID, Use: s.Use, Model: s.Model, Params: s.Params, Views: s.Views}
		for _, in := range s.Inputs {
			if inBatch[in] {
				spec.Inputs = append(spec.Inputs, "stage:"+in)
				continue
			}
			ref, ok := outputs[in]
			if !ok {
				return fmt.Errorf("stage %s: input %s has no output", s.ID, in)
			}
			if err := p.Upload(ctx, ref); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				err = fmt.Errorf("uploading %s to %s: %w", ref, p.ID(), err)
				return &stageError{provider: p.ID(), stages: idsAt(plan, idx), down: unreachable(err), err: err}
			}
			spec.Inputs = append(spec.Inputs, ref)
		}
		req.Stages = append(req.Stages, spec)
	}

	// A paid batch starts only within the spending limits, and one billed by the second is
	// stopped once it would pass one.
	batch := make([]planner.Stage, len(idx))
	for n, i := range idx {
		batch[n] = plan.Stages[i]
	}
	charges := r.chargesFor(ctx, a, p, batch)
	bctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if p.Paid() {
		release, err := r.admit(ctx, a.job.Project, a.job.ID, charges, stop)
		if err != nil {
			return err
		}
		defer release()
	}

	evs := make(chan engineapi.Event, 64)
	consumed := make(chan struct{})
	billed := map[string]bool{}
	go func() {
		defer close(consumed)
		for ev := range evs {
			if ev.Type == "stage.billed" {
				id, _ := ev.Data["stage"].(string)
				billed[id] = true
				continue
			}
			r.onEngineEvent(ctx, a, p, ev)
		}
	}()
	started := time.Now()
	run, err := p.Run(bctx, req, evs)
	close(evs)
	<-consumed
	if p.Paid() {
		r.bill(a, p, batch, charges, run, billed, time.Since(started).Seconds())
	}
	if over, ok := context.Cause(bctx).(*BudgetError); ok && ctx.Err() == nil {
		return over
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fmt.Errorf("provider %s: %w", p.ID(), err)
		return &stageError{provider: p.ID(), stages: idsAt(plan, idx), down: unreachable(err), err: err}
	}

	for _, i := range idx {
		s := plan.Stages[i]
		st := run.Stages[s.ID]
		var outs []library.Output
		for _, o := range st.Outputs {
			if err := p.Download(ctx, o.Hash); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return &stageError{provider: p.ID(), stages: idsAt(plan, idx), down: true,
					err: fmt.Errorf("downloading %s: %w", o.Hash, err)}
			}
			outs = append(outs, library.Output{Hash: o.Hash, Kind: o.Kind, Preview: o.Preview})
		}
		seconds := 0.0
		if st.Seconds != nil {
			seconds = *st.Seconds
		}
		errMsg := ""
		if st.Error != nil {
			errMsg = *st.Error
		}
		r.update(a, func(j *library.Job) {
			js := stage(j, s.ID)
			if js == nil {
				return
			}
			js.Seconds, js.Error = seconds, errMsg
			if st.PeakVRAMBytes != nil {
				js.PeakVRAM = *st.PeakVRAMBytes
			}
			if outs != nil {
				js.Outputs = outs
			}
			switch st.Status {
			case "succeeded":
				js.Status = library.Succeeded
			case "cancelled":
				js.Status = library.Cancelled
			case "failed":
				js.Status = library.Failed
			}
		})
		if st.Status == "succeeded" && len(outs) > 0 {
			outputs[s.ID], all[s.ID] = outs[0].Hash, outs
			_ = r.db.CachePut(ctx, s.Key, outs, seconds)
			_ = r.db.RecordTiming(ctx, s.Use, s.Model, p.ID(), seconds)
		}
	}
	r.hub.Publish(EvJobUpdated, snapshot(a))
	if run.Status != "succeeded" {
		msg := run.Status
		if run.Error != nil {
			msg = *run.Error
		}
		if run.Status == "cancelled" {
			return context.Canceled
		}
		// The stage that failed may fit on another provider; the ones after it never ran here.
		failure := &stageError{provider: p.ID(), err: errors.New(msg), oom: outOfMemory.MatchString(msg)}
		for _, s := range batch {
			if st := run.Stages[s.ID]; st.Status == "failed" {
				failure.stages = append(failure.stages, s.ID)
				failure.oom = failure.oom || (st.Error != nil && outOfMemory.MatchString(*st.Error))
			}
		}
		return failure
	}
	return nil
}

// idsAt are the ids of a plan's stages at idx.
func idsAt(plan *planner.Plan, idx []int) []string {
	out := make([]string, 0, len(idx))
	for _, i := range idx {
		out = append(out, plan.Stages[i].ID)
	}
	return out
}

// unreachable reports a run that failed for want of its provider: no connection, a timeout, a
// server error. A provider that answered and refused the run would refuse it again.
func unreachable(err error) bool {
	var answered interface{ StatusCode() int }
	if errors.As(err, &answered) {
		return answered.StatusCode() >= 500
	}
	return true
}

func (r *Runner) onEngineEvent(ctx context.Context, a *active, p provider.Provider, ev engineapi.Event) {
	stageID, _ := ev.Data["stage"].(string)
	idx := -1
	r.update(a, func(j *library.Job) {
		for i := range j.Stages {
			if j.Stages[i].ID == stageID {
				idx = i
			}
		}
	})
	if idx < 0 {
		return
	}
	switch ev.Type {
	case "stage.started":
		r.update(a, func(j *library.Job) { j.Stages[idx].Status = library.Running })
		r.hub.Publish(EvJobUpdated, snapshot(a))
	case "stage.progress":
		step, _ := ev.Data["step"].(float64)
		total, _ := ev.Data["total"].(float64)
		r.update(a, func(j *library.Job) { j.Stages[idx].Step, j.Stages[idx].Total = int(step), int(total) })
		r.hub.Publish(EvStageProgress, map[string]any{
			"job": a.job.ID, "stage": stageID, "step": int(step), "total": int(total),
		})
	case "artifact.ready":
		hash, _ := ev.Data["hash"].(string)
		kind, _ := ev.Data["kind"].(string)
		preview, _ := ev.Data["preview"].(bool)
		if preview {
			// Fetch previews right away so the viewer can show the shape before texturing ends.
			if err := p.Download(ctx, hash); err == nil {
				r.hub.Publish(EvArtifact, map[string]any{
					"job": a.job.ID, "stage": stageID, "hash": hash, "kind": kind, "preview": true,
				})
			}
		}
	case "stage.log":
		// A hosted model's place in its queue: what the user sees while nothing else moves.
		msg, _ := ev.Data["message"].(string)
		r.update(a, func(j *library.Job) { j.Stages[idx].Note = msg })
		r.hub.Publish(EvJobUpdated, snapshot(a))
	case "stage.finished":
		secs, _ := ev.Data["seconds"].(float64)
		r.update(a, func(j *library.Job) {
			j.Stages[idx].Status, j.Stages[idx].Seconds, j.Stages[idx].Note = library.Succeeded, secs, ""
		})
		r.hub.Publish(EvJobUpdated, snapshot(a))
	case "stage.failed":
		msg, _ := ev.Data["error"].(string)
		r.update(a, func(j *library.Job) {
			j.Stages[idx].Status, j.Stages[idx].Error, j.Stages[idx].Note = library.Failed, msg, ""
		})
		r.hub.Publish(EvJobUpdated, snapshot(a))
	}
}

func (r *Runner) createAsset(ctx context.Context, a *active, plan *planner.Plan, cfg *config.Loaded) error {
	job := snapshot(a)
	last := job.Stages[len(job.Stages)-1]
	if len(last.Outputs) == 0 {
		return errors.New("the last stage produced no output")
	}
	asset := &library.Asset{
		ID: ulid.Make().String(), JobID: job.ID, Name: job.Name, Preset: job.Preset, GLB: last.Outputs[0].Hash,
		Input: job.Input, Project: job.Project, CreatedAt: time.Now(),
	}
	glbPath, err := r.store.Path(asset.GLB)
	if err != nil {
		return err
	}
	asset.Stats = AssetStats(glbPath, r.log)
	if job.Project != "" {
		side := export.Sidecar{
			Generator: "forja " + r.version, JobID: job.ID, AssetID: asset.ID, Preset: job.Preset,
			Input: job.Input, CreatedAt: asset.CreatedAt,
		}
		for i, s := range plan.Stages {
			side.Stages = append(side.Stages, export.SidecarStage{
				ID: s.ID, Use: s.Use, Model: s.Model, Params: s.Params, Provider: job.Stages[i].Provider,
				License: r.license(ctx, job.Stages[i].Provider, s.Model),
			})
		}
		if asset.ExportPath, err = export.ToProject(job.Project, cfg.Project.ExportDir, job.Name, glbPath, side); err != nil {
			return err
		}
	}
	if err := r.db.SaveAsset(ctx, asset); err != nil {
		return err
	}
	r.update(a, func(j *library.Job) { j.AssetID = asset.ID })
	r.hub.Publish(EvAssetCreated, asset)
	return nil
}

func (r *Runner) license(ctx context.Context, providerID, model string) string {
	if model == "" {
		return ""
	}
	e, err := r.catalog.Get(ctx, providerID)
	if err != nil {
		return ""
	}
	if m, ok := e.Model(model); ok {
		return m.License.SPDX
	}
	return ""
}
