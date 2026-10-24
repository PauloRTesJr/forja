// Package api is the core's HTTP API, served over the local socket to the UI and CLI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/events"
	"forja/internal/glb"
	"forja/internal/installs"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/presets"
	"forja/internal/provider"
	"forja/internal/store"
)

// EvModelsChanged tells clients that a provider's models changed: re-read models and presets.
const EvModelsChanged = "models.changed"

type Server struct {
	Version   string
	DataDir   string
	DB        *library.DB
	Store     *store.Store
	Hub       *events.Hub
	Runner    *jobs.Runner
	Providers *provider.Registry
	Catalog   *catalog.Catalog
	Installs  *installs.Tracker
	Engine    *localengine.Manager
	Config    func(project string) (*config.Loaded, error)
	Shutdown  func()
	Log       *slog.Logger
}

type route struct {
	pattern string
	handler http.HandlerFunc
}

func (s *Server) routes() []route {
	return []route{
		{"GET /v1/health", s.health},
		{"GET /v1/info", s.info},
		{"GET /v1/config/effective", s.configEffective},
		{"GET /v1/settings", s.settingsGet},
		{"PATCH /v1/settings", s.settingsPatch},
		{"POST /v1/settings/file", s.settingsFile},
		{"GET /v1/diagnostics", s.diagnostics},
		{"GET /v1/presets", s.presets},
		{"GET /v1/providers", s.providers},
		{"GET /v1/provider-kinds", s.providerKinds},
		{"POST /v1/providers/test", s.providerTest},
		{"POST /v1/providers", s.providerAdd},
		{"DELETE /v1/providers/{id}", s.providerRemove},
		{"PUT /v1/providers/{id}/token", s.providerToken},
		{"GET /v1/providers/{id}/catalog", s.hubCatalog},
		{"GET /v1/providers/{id}/thumbnail", s.hubThumbnail},
		{"POST /v1/providers/{id}/models", s.hubModels},
		{"DELETE /v1/providers/{id}/models", s.hubModels},
		{"GET /v1/spend", s.spend},
		{"GET /v1/share", s.shareGet},
		{"PUT /v1/share", s.sharePut},
		{"POST /v1/share/code", s.shareCode},
		{"GET /v1/models", s.models},
		{"POST /v1/models/{id}/install", s.modelInstall},
		{"DELETE /v1/models/{id}", s.modelRemove},
		{"PUT /v1/models/stages/{use}", s.modelStage},
		{"GET /v1/checks", s.checks},
		{"POST /v1/checks/{id}/fix", s.checkFix},
		{"GET /v1/installs", s.installList},
		{"DELETE /v1/installs/{provider}/{id}", s.installCancel},
		{"GET /v1/setup", s.setup},
		{"POST /v1/setup/test", s.setupTest},
		{"POST /v1/engine/unload", s.engineUnload},
		{"POST /v1/jobs", s.jobCreate},
		{"GET /v1/jobs", s.jobList},
		{"GET /v1/jobs/{id}", s.jobGet},
		{"DELETE /v1/jobs/{id}", s.jobCancel},
		{"POST /v1/jobs/{id}/sheet", s.jobSheet},
		{"GET /v1/assets", s.assetList},
		{"GET /v1/assets/{id}", s.assetGet},
		{"DELETE /v1/assets/{id}", s.assetDelete},
		{"PUT /v1/assets/{id}/thumbnail", s.assetThumbnail},
		{"POST /v1/assets/{id}/blender", s.assetBlender},
		{"GET /v1/export/formats", s.exportFormats},
		{"POST /v1/export", s.exportAssets},
		{"GET /v1/artifacts/{ref}", s.artifactGet},
		{"PUT /v1/artifacts", s.artifactPut},
		{"POST /v1/artifacts/import", s.artifactImport},
		{"GET /v1/events", s.eventsWS},
		{"GET /v1/events/stream", s.eventsSSE},
		{"POST /v1/shutdown", s.shutdown},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes() {
		mux.HandleFunc(r.pattern, r.handler)
	}
	return mux
}

// Routes lists every endpoint as its pattern, such as "GET /v1/jobs/{id}", so the CLI can
// check that it reaches each one (docs/decisions.md, decision 18).
func Routes() []string {
	all := (&Server{}).routes()
	out := make([]string, len(all))
	for i, r := range all {
		out[i] = r.pattern
	}
	return out
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) shutdown(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusAccepted)
	go s.Shutdown()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// problem writes an RFC 9457 problem document.
func problem(w http.ResponseWriter, status int, detail string) {
	problemWith(w, status, detail, nil)
}

// problemWith adds extension members, such as the license a request must accept.
func problemWith(w http.ResponseWriter, status int, detail string, extra map[string]any) {
	doc := map[string]any{"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": detail}
	for k, v := range extra {
		doc[k] = v
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(doc)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config("")
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	out := map[string]any{
		"version": s.Version, "data_dir": s.DataDir, "config_files": cfg.Files, "user_config": config.UserFile(),
		"engine": map[string]any{"running": s.Engine.Running()},
	}
	// The hardware the engine last reported, so Settings shows it without starting the engine.
	if e, ok := s.Catalog.Last(jobs.LocalProvider); ok {
		out["system"] = e.System
	}
	if s.Engine.Running() {
		if p, ok := s.Providers.Get(jobs.LocalProvider); ok {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			if info, err := p.Info(ctx); err == nil {
				out["engine"] = map[string]any{"running": true, "info": info}
			}
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) configEffective(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config(r.URL.Query().Get("project"))
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, cfg)
}

// PresetView is a preset plus how it would run right now: the model and provider per stage.
type PresetView struct {
	*presets.Preset
	Builtin bool        `json:"builtin"`
	Plan    []jobs.Pick `json:"plan"`
	Ready   bool        `json:"ready"`
	Error   string      `json:"error,omitempty"`
}

// fresh is ?fresh=1: ask providers now, even if that starts the local engine, instead of
// answering from what they said last.
func fresh(r *http.Request) bool { return r.URL.Query().Get("fresh") == "1" }

// askAll asks every provider what it runs, in parallel; the catalog keeps the answers.
func (s *Server) askAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range s.Providers.All() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Catalog.Ask(ctx, p.ID())
		}()
	}
	wg.Wait()
}

func (s *Server) presets(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	all, err := s.Runner.Presets(project)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	ordered := make([]*presets.Preset, 0, len(all))
	for _, id := range []string{"draft", "mobile-prop", "environment-piece", "hero-character"} {
		if p, ok := all[id]; ok {
			ordered = append(ordered, p)
			delete(all, id)
		}
	}
	rest := make([]*presets.Preset, 0, len(all))
	for _, p := range all {
		rest = append(rest, p)
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	ordered = append(ordered, rest...)

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if fresh(r) {
		s.askAll(ctx)
	}
	// ?views=left,back: the plan for a job that brings those views too, so the app can say
	// which model would read them.
	var views []string
	if q := r.URL.Query().Get("views"); q != "" {
		views = strings.Split(q, ",")
	}
	list := make([]PresetView, len(ordered))
	for i, p := range ordered {
		v := PresetView{Preset: p, Builtin: p.Builtin(), Plan: []jobs.Pick{}}
		plan, err := s.Runner.Resolve(ctx, project, p, views)
		if err != nil {
			v.Error = err.Error()
		} else {
			v.Plan, v.Ready = plan, true
			for _, pick := range plan {
				v.Ready = v.Ready && !pick.Missing
			}
		}
		list[i] = v
	}
	writeJSON(w, 200, list)
}

// engineUnload frees all VRAM now, for the "pause engine" toggle while gaming or in Unity.
func (s *Server) engineUnload(w http.ResponseWriter, r *http.Request) {
	if !s.Engine.Running() {
		w.WriteHeader(204)
		return
	}
	base, token, err := s.Engine.Endpoint(r.Context())
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/v1/models/unload-all", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	resp.Body.Close()
	w.WriteHeader(204)
}

func (s *Server) jobCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		jobs.SubmitRequest
		Paths []string `json:"paths"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	reqs := []jobs.SubmitRequest{body.SubmitRequest}
	if len(body.Paths) > 0 {
		if len(body.Views) > 0 {
			problem(w, 422, "views belong to one image: submit them with path, not paths")
			return
		}
		reqs = reqs[:0]
		for _, p := range body.Paths {
			req := body.SubmitRequest
			req.Path = p
			reqs = append(reqs, req)
		}
	}
	created := make([]*library.Job, 0, len(reqs))
	for _, req := range reqs {
		job, err := s.Runner.Submit(r.Context(), req)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		created = append(created, job)
	}
	writeJSON(w, 202, created)
}

// jobSheet answers a job waiting on a sheet of views: {"views": [{"view", "box"}...]} to cut
// them out, or {"keep": true} to use the image whole.
func (s *Server) jobSheet(w http.ResponseWriter, r *http.Request) {
	var ans jobs.SheetAnswer
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&ans); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	job, err := s.Runner.AnswerSheet(r.Context(), r.PathValue("id"), ans)
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no job "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 202, job)
}

func page(r *http.Request) (int, time.Time) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	before := time.Now().Add(time.Minute)
	if ms, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
		before = time.UnixMilli(ms)
	}
	return limit, before
}

func (s *Server) jobList(w http.ResponseWriter, r *http.Request) {
	limit, before := page(r)
	list, err := s.DB.Jobs(r.Context(), limit, before)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	// Active jobs carry live progress the database has not seen yet.
	for i, j := range list {
		if !j.Status.Done() {
			if live, err := s.Runner.Job(r.Context(), j.ID); err == nil {
				list[i] = live
			}
		}
	}
	if list == nil {
		list = []*library.Job{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) jobGet(w http.ResponseWriter, r *http.Request) {
	job, err := s.Runner.Job(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no job "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, job)
}

func (s *Server) jobCancel(w http.ResponseWriter, r *http.Request) {
	if !s.Runner.Cancel(r.PathValue("id")) {
		problem(w, 409, "job is not running")
		return
	}
	w.WriteHeader(202)
}

func (s *Server) assetList(w http.ResponseWriter, r *http.Request) {
	limit, before := page(r)
	list, err := s.DB.Assets(r.Context(), limit, before, r.URL.Query().Get("q"))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*library.Asset{}
	}
	for _, a := range list {
		s.withStats(r.Context(), a)
	}
	writeJSON(w, 200, list)
}

func (s *Server) assetGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.withStats(r.Context(), a)
	writeJSON(w, 200, a)
}

// assetThumbnail stores a thumbnail the UI rendered once, off-screen.
func (s *Server) assetThumbnail(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	ref, err := s.Store.Put(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	a.Thumbnail = ref
	if err := s.DB.SaveAsset(r.Context(), a); err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.Hub.Publish(jobs.EvAssetCreated, a)
	writeJSON(w, 200, a)
}

// withStats reads the stats of an asset made before the core read them from its GLB. One
// it cannot read gets empty stats, so it is tried once.
func (s *Server) withStats(ctx context.Context, a *library.Asset) {
	if a.Stats != nil {
		return
	}
	a.Stats = map[string]any{}
	if path, err := s.Store.Path(a.GLB); err == nil {
		if st, err := glb.ReadFile(path); err == nil {
			a.Stats = st.Map()
		}
	}
	if err := s.DB.SaveAsset(ctx, a); err != nil {
		s.Log.Warn("saving asset stats", "asset", a.ID, "err", err)
	}
}

func (s *Server) artifactGet(w http.ResponseWriter, r *http.Request) {
	f, err := s.Store.Open(r.PathValue("ref"))
	if err != nil {
		problem(w, 404, "no artifact "+r.PathValue("ref"))
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	// Content-addressed: the bytes behind a ref never change.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("ETag", `"`+r.PathValue("ref")+`"`)
	if ct := r.URL.Query().Get("type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) artifactPut(w http.ResponseWriter, r *http.Request) {
	ref, err := s.Store.Put(io.LimitReader(r.Body, 512<<20))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"hash": ref})
}

// artifactImport copies a file the user picked into the store, so the app can show it and
// submit it by ref (the views of one subject, before they become a job).
func (s *Server) artifactImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Path == "" {
		problem(w, 400, "want {\"path\": \"<file>\"}")
		return
	}
	if st, err := os.Stat(body.Path); err != nil || !st.Mode().IsRegular() {
		problem(w, 422, "cannot read "+body.Path)
		return
	}
	ref, err := s.Store.PutFile(body.Path)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 201, map[string]string{"hash": ref})
}

// eventsSSE is the same stream as eventsWS for clients that cannot speak WebSocket over a
// Unix socket or named pipe (the Electron main process).
func (s *Server) eventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher.Flush()
	ch, unsubscribe := s.Hub.Subscribe()
	defer unsubscribe()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			if _, err := io.WriteString(w, "data: "+string(raw)+"\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) eventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ch, unsubscribe := s.Hub.Subscribe()
	defer unsubscribe()
	ctx := conn.CloseRead(r.Context())
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := wsjson.Write(wctx, conn, ev)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
