// Package api is the core's HTTP API, served over the local socket to the UI and CLI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"forja/internal/config"
	"forja/internal/events"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/provider"
	"forja/internal/store"
)

type Server struct {
	Version   string
	DataDir   string
	DB        *library.DB
	Store     *store.Store
	Hub       *events.Hub
	Runner    *jobs.Runner
	Providers *provider.Registry
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
	mux.HandleFunc("GET /v1/presets", s.presets)
	mux.HandleFunc("GET /v1/providers", s.providers)
	mux.HandleFunc("GET /v1/models", s.models)
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
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "about:blank", "title": http.StatusText(status), "status": status, "detail": detail,
	})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config("")
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	out := map[string]any{
		"version": s.Version, "data_dir": s.DataDir, "config_files": cfg.Files,
		"engine": map[string]any{"running": s.Engine.Running()},
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

func (s *Server) presets(w http.ResponseWriter, r *http.Request) {
	all, err := s.Runner.Presets(r.URL.Query().Get("project"))
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	list := make([]any, 0, len(all))
	for _, id := range []string{"draft", "mobile-prop", "environment-piece", "hero-character"} {
		if p, ok := all[id]; ok {
			list = append(list, p)
			delete(all, id)
		}
	}
	for _, p := range all {
		list = append(list, p)
	}
	writeJSON(w, 200, list)
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	type row struct {
		ID      string `json:"id"`
		Kind    string `json:"kind"`
		Paid    bool   `json:"paid"`
		Healthy bool   `json:"healthy"`
		Latency int64  `json:"latency_ms"`
		Error   string `json:"error,omitempty"`
	}
	all := s.Providers.All()
	rows := make([]row, len(all))
	var wg sync.WaitGroup
	for i, p := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = row{ID: p.ID(), Kind: string(p.Kind()), Paid: p.Paid()}
			// Asking about the local engine must not start it.
			if p.Kind() == provider.KindLocal && !s.Engine.Running() {
				rows[i].Error = "idle"
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			err := p.Health(ctx)
			rows[i].Latency = time.Since(start).Milliseconds()
			rows[i].Healthy = err == nil
			if err != nil {
				rows[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	writeJSON(w, 200, rows)
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("provider")
	if id == "" {
		id = jobs.LocalProvider
	}
	p, ok := s.Providers.Get(id)
	if !ok {
		problem(w, 404, "no provider "+id)
		return
	}
	models, err := p.Models(r.Context())
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, models)
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
