// Package jobs owns a job from submission to export: plan, cache lookups, routing,
// provider runs, cache records and the finished asset.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

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
	// Exactly one of Path and Input.
	Path      string         `json:"path,omitempty"`
	Input     string         `json:"input,omitempty"`
	Name      string         `json:"name,omitempty"`
	Preset    string         `json:"preset,omitempty"`
	Project   string         `json:"project,omitempty"`
	Priority  string         `json:"priority,omitempty"`
	Seed      *int           `json:"seed,omitempty"`
	Overrides map[string]any `json:"overrides,omitempty"`
}

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

	mu     sync.Mutex
	active map[string]*active
	// lastRuns is each provider's latest answer to "what do you run?", for keying jobs while
	// it is unreachable.
	lastRuns map[string]*runs
}

// runs is what one provider runs: built-in stage versions and its models.
type runs struct {
	stages map[string]string
	models map[string]engineapi.Model
}

type active struct {
	cancel context.CancelFunc
	job    *library.Job
	mu     sync.Mutex
}

func NewRunner(db *library.DB, st *store.Store, hub *events.Hub, providers *provider.Registry,
	cfg func(project string) (*config.Loaded, error), log *slog.Logger, version string, maxInFlight int,
) *Runner {
	return &Runner{
		db: db, store: st, hub: hub, providers: providers, config: cfg, log: log, version: version,
		slots: make(chan struct{}, maxInFlight), active: map[string]*active{}, lastRuns: map[string]*runs{},
	}
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
	if name == "" {
		name = "asset"
	}
	presetID := req.Preset
	if presetID == "" {
		presetID = cfg.Project.DefaultPreset
	}
	all, err := r.Presets(project)
	if err != nil {
		return nil, err
	}
	base, ok := all[presetID]
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", presetID)
	}
	overrides := map[string]any{}
	if po, ok := cfg.Presets[presetID]; ok {
		for k, v := range po.Overrides {
			overrides[k] = v
		}
	}
	for k, v := range req.Overrides {
		overrides[k] = v
	}
	preset, err := base.WithOverrides(overrides)
	if err != nil {
		return nil, err
	}
	seed := 42
	if req.Seed != nil {
		seed = *req.Seed
	}
	routes := map[string]string{}
	for _, s := range preset.Stages {
		if routes[s.ID], err = r.route(cfg, s); err != nil {
			return nil, err
		}
	}
	priority := req.Priority
	if priority != "interactive" {
		priority = "batch"
	}
	job := &library.Job{
		ID: ulid.Make().String(), Name: export.Slug(name), Preset: presetID, Input: input, Project: project,
		Priority: priority, Overrides: req.Overrides, Status: library.Queued, CreatedAt: time.Now(),
	}
	for _, s := range preset.Stages {
		job.Stages = append(job.Stages, library.Stage{
			ID: s.ID, Use: s.Use, Model: s.Model, Status: library.Queued, Provider: routes[s.ID],
			Outputs: []library.Output{},
		})
		job.EstimateSec += r.db.EstimateSeconds(ctx, s.Use, s.Model, routes[s.ID])
	}
	if err := r.db.SaveJob(ctx, job); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	a := &active{cancel: cancel, job: job}
	r.mu.Lock()
	r.active[job.ID] = a
	r.mu.Unlock()
	r.hub.Publish(EvJobCreated, snapshot(a))
	go r.run(runCtx, a, preset, seed, routes, cfg)
	return snapshot(a), nil
}

// fingerprints asks each provider the job routes to what it runs, once per job: keys depend on
// it, so a new adapter or new weights on that provider never reuse outputs cached before.
func (r *Runner) fingerprints(ctx context.Context, routes map[string]string) planner.Fingerprints {
	asked := map[string]*runs{}
	return func(s presets.Stage) (planner.Fingerprint, error) {
		id := routes[s.ID]
		got, ok := asked[id]
		if !ok {
			var err error
			if got, err = r.askRuns(ctx, id); err != nil {
				return planner.Fingerprint{}, err
			}
			asked[id] = got
		}
		if s.Model == "" {
			return planner.Fingerprint{Adapter: got.stages[s.Use]}, nil
		}
		m, ok := got.models[s.Model]
		if !ok {
			return planner.Fingerprint{}, fmt.Errorf("provider %s has no model %q", id, s.Model)
		}
		return planner.Fingerprint{Adapter: m.AdapterVersion, Weights: m.Weights.Revision, Upstream: m.Upstream}, nil
	}
}

// askRuns asks a provider what it runs. One that cannot answer now (a LAN box that is off) is
// keyed by its last answer, so a job whose stages are all cached still finishes.
func (r *Runner) askRuns(ctx context.Context, id string) (*runs, error) {
	p, ok := r.providers.Get(id)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", id)
	}
	info, err := p.Info(ctx)
	var models []engineapi.Model
	if err == nil {
		models, err = p.Models(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		if last, ok := r.lastRuns[id]; ok && ctx.Err() == nil {
			r.log.Warn("keying stages with the provider's last answer", "provider", id, "err", err)
			return last, nil
		}
		return nil, fmt.Errorf("provider %s did not say what it runs: %w", id, err)
	}
	got := &runs{stages: info.Stages, models: map[string]engineapi.Model{}}
	for _, m := range models {
		got.models[m.ID] = m
	}
	r.lastRuns[id] = got
	return got, nil
}

// route picks a provider for a stage: explicit, then project routing, then the priority list.
func (r *Runner) route(cfg *config.Loaded, s presets.Stage) (string, error) {
	want := s.Provider
	if want == "auto" {
		want = ""
	}
	if want == "" {
		want = cfg.Routing.Stages[s.Use]
	}
	if want == "" {
		for _, id := range cfg.Routing.Priority {
			p, ok := r.providers.Get(id)
			if !ok || (p.Paid() && !cfg.Routing.AllowPaidFallback) {
				continue
			}
			want = id
			break
		}
	}
	if want == "" {
		want = LocalProvider
	}
	p, ok := r.providers.Get(want)
	if !ok {
		return "", fmt.Errorf("stage %s routes to provider %q, which is not configured", s.ID, want)
	}
	if cfg.Routing.Policy == "offline-only" && p.Kind() != provider.KindLocal && p.Kind() != provider.KindEngine {
		return "", fmt.Errorf("stage %s: policy offline-only forbids provider %q", s.ID, want)
	}
	return want, nil
}

func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	a, ok := r.active[id]
	r.mu.Unlock()
	if ok {
		a.cancel()
	}
	return ok
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
	return &cp
}

func (r *Runner) update(a *active, fn func(j *library.Job)) {
	a.mu.Lock()
	fn(a.job)
	a.mu.Unlock()
}

func (r *Runner) run(ctx context.Context, a *active, preset *presets.Preset, seed int,
	routes map[string]string, cfg *config.Loaded,
) {
	defer func() {
		r.mu.Lock()
		delete(r.active, a.job.ID)
		r.mu.Unlock()
	}()
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		r.finish(a, library.Cancelled, "cancelled before it started")
		return
	}
	r.update(a, func(j *library.Job) { j.Status = library.Running })
	r.hub.Publish(EvJobUpdated, snapshot(a))

	// Keyed here, not at submit: asking a provider what it runs may start the local engine.
	plan, err := planner.Build(preset, snapshot(a).Input, seed, r.fingerprints(ctx, routes))
	if err == nil {
		for i := range plan.Stages {
			plan.Stages[i].Provider = routes[plan.Stages[i].ID]
		}
		r.update(a, func(j *library.Job) {
			for i := range j.Stages {
				j.Stages[i].Key = plan.Stages[i].Key
			}
		})
		err = r.execute(ctx, a, plan)
	}
	switch {
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

// execute resolves cache hits, then runs the remaining stages in batches of consecutive
// stages that share a provider, so intermediates stay on that provider.
func (r *Runner) execute(ctx context.Context, a *active, plan *planner.Plan) error {
	outputs := map[string]string{presets.InputImage: plan.Input} // stage id -> primary output ref
	var pending []int
	for i, s := range plan.Stages {
		outs, hit, err := r.db.CacheGet(ctx, s.Key)
		if err != nil {
			return err
		}
		if hit && len(outs) > 0 && r.store.Has(outs[0].Hash) {
			outputs[s.ID] = outs[0].Hash
			r.update(a, func(j *library.Job) {
				st := &j.Stages[i]
				st.Status, st.Cached, st.Outputs = library.Succeeded, true, outs
			})
			continue
		}
		if hit {
			_ = r.db.CacheForget(ctx, s.Key)
		}
		pending = append(pending, i)
	}
	r.hub.Publish(EvJobUpdated, snapshot(a))

	for start := 0; start < len(pending); {
		end := start + 1
		prov := plan.Stages[pending[start]].Provider
		for end < len(pending) && plan.Stages[pending[end]].Provider == prov {
			end++
		}
		if err := r.runBatch(ctx, a, plan, pending[start:end], outputs); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func (r *Runner) runBatch(ctx context.Context, a *active, plan *planner.Plan, idx []int, outputs map[string]string) error {
	p, _ := r.providers.Get(plan.Stages[idx[0]].Provider)
	inBatch := map[string]bool{}
	for _, i := range idx {
		inBatch[plan.Stages[i].ID] = true
	}
	req := engineapi.RunRequest{ClientRunID: a.job.ID + "-" + plan.Stages[idx[0]].ID, Priority: a.job.Priority, Seed: plan.Seed}
	for _, i := range idx {
		s := plan.Stages[i]
		spec := engineapi.StageSpec{ID: s.ID, Use: s.Use, Model: s.Model, Params: s.Params}
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
				return fmt.Errorf("uploading %s to %s: %w", ref, p.ID(), err)
			}
			spec.Inputs = append(spec.Inputs, ref)
		}
		req.Stages = append(req.Stages, spec)
	}

	evs := make(chan engineapi.Event, 64)
	consumed := make(chan struct{})
	go func() {
		defer close(consumed)
		for ev := range evs {
			r.onEngineEvent(ctx, a, p, ev)
		}
	}()
	run, err := p.Run(ctx, req, evs)
	close(evs)
	<-consumed
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("provider %s: %w", p.ID(), err)
	}

	for _, i := range idx {
		s := plan.Stages[i]
		st := run.Stages[s.ID]
		var outs []library.Output
		for _, o := range st.Outputs {
			if err := p.Download(ctx, o.Hash); err != nil {
				return fmt.Errorf("downloading %s: %w", o.Hash, err)
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
			js := &j.Stages[i]
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
			outputs[s.ID] = outs[0].Hash
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
		return errors.New(msg)
	}
	return nil
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
	case "stage.finished":
		secs, _ := ev.Data["seconds"].(float64)
		r.update(a, func(j *library.Job) {
			j.Stages[idx].Status, j.Stages[idx].Seconds = library.Succeeded, secs
		})
		r.hub.Publish(EvJobUpdated, snapshot(a))
	case "stage.failed":
		msg, _ := ev.Data["error"].(string)
		r.update(a, func(j *library.Job) { j.Stages[idx].Status, j.Stages[idx].Error = library.Failed, msg })
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
	if job.Project != "" {
		glbPath, err := r.store.Path(asset.GLB)
		if err != nil {
			return err
		}
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
	p, ok := r.providers.Get(providerID)
	if !ok {
		return ""
	}
	models, err := p.Models(ctx)
	if err != nil {
		return ""
	}
	for _, m := range models {
		if m.ID == model {
			return m.License.SPDX
		}
	}
	return ""
}
