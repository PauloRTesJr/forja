// Package api is the core's HTTP API, served over the local socket to the UI and CLI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/events"
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

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/info", s.info)
	mux.HandleFunc("GET /v1/config/effective", s.configEffective)
	mux.HandleFunc("GET /v1/settings", s.settingsGet)
	mux.HandleFunc("PATCH /v1/settings", s.settingsPatch)
	mux.HandleFunc("POST /v1/settings/file", s.settingsFile)
	mux.HandleFunc("GET /v1/diagnostics", s.diagnostics)
	mux.HandleFunc("GET /v1/presets", s.presets)
	mux.HandleFunc("GET /v1/providers", s.providers)
	mux.HandleFunc("GET /v1/provider-kinds", s.providerKinds)
	mux.HandleFunc("POST /v1/providers/test", s.providerTest)
	mux.HandleFunc("POST /v1/providers", s.providerAdd)
	mux.HandleFunc("DELETE /v1/providers/{id}", s.providerRemove)
	mux.HandleFunc("PUT /v1/providers/{id}/token", s.providerToken)
	mux.HandleFunc("GET /v1/share", s.shareGet)
	mux.HandleFunc("PUT /v1/share", s.sharePut)
	mux.HandleFunc("POST /v1/share/code", s.shareCode)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/models/{id}/install", s.modelInstall)
	mux.HandleFunc("DELETE /v1/models/{id}", s.modelRemove)
	mux.HandleFunc("PUT /v1/models/stages/{use}", s.modelStage)
	mux.HandleFunc("GET /v1/checks", s.checks)
	mux.HandleFunc("POST /v1/checks/{id}/fix", s.checkFix)
	mux.HandleFunc("GET /v1/installs", s.installList)
	mux.HandleFunc("DELETE /v1/installs/{provider}/{id}", s.installCancel)
	mux.HandleFunc("GET /v1/setup", s.setup)
	mux.HandleFunc("POST /v1/setup/test", s.setupTest)
	mux.HandleFunc("POST /v1/engine/unload", s.engineUnload)
	mux.HandleFunc("POST /v1/jobs", s.jobCreate)
	mux.HandleFunc("GET /v1/jobs", s.jobList)
	mux.HandleFunc("GET /v1/jobs/{id}", s.jobGet)
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.jobCancel)
	mux.HandleFunc("GET /v1/assets", s.assetList)
	mux.HandleFunc("GET /v1/assets/{id}", s.assetGet)
	mux.HandleFunc("PUT /v1/assets/{id}/thumbnail", s.assetThumbnail)
	mux.HandleFunc("PATCH /v1/assets/{id}", s.assetPatch)
	mux.HandleFunc("GET /v1/artifacts/{ref}", s.artifactGet)
	mux.HandleFunc("PUT /v1/artifacts", s.artifactPut)
	mux.HandleFunc("GET /v1/events", s.eventsWS)
	mux.HandleFunc("GET /v1/events/stream", s.eventsSSE)
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		go s.Shutdown()
	})
	return mux
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

// presetView is a preset plus how it would run right now: the model and provider per stage.
type presetView struct {
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
	list := make([]presetView, len(ordered))
	for i, p := range ordered {
		v := presetView{Preset: p, Builtin: p.Builtin(), Plan: []jobs.Pick{}}
		plan, err := s.Runner.Resolve(ctx, project, p)
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

// assetPatch accepts viewer-computed stats (triangles, materials, texture sizes).
func (s *Server) assetPatch(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	var patch struct {
		Stats map[string]any `json:"stats"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&patch); err != nil {
		problem(w, 400, err.Error())
		return
	}
	if patch.Stats != nil {
		a.Stats = patch.Stats
	}
	if err := s.DB.SaveAsset(r.Context(), a); err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a)
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
