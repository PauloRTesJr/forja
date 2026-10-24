package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"forja/internal/bench"
)

// benches is the benchmark runner, made on first use from what the server already has.
func (s *Server) benches() *bench.Bench {
	s.benchOnce.Do(func() {
		s.bench = &bench.Bench{
			Runner: s.Runner, Store: s.Store, Providers: s.Providers, Hub: s.Hub,
			Dir: filepath.Join(s.DataDir, "bench"), Version: s.Version, Unload: s.unloadModels, Log: s.Log,
		}
	})
	return s.bench
}

// benchStart runs a suite: {"suite": "<file>", "presets": [...], "cold", "batch", "repeat"}.
// The run goes on in the background, announced by bench.updated and bench.finished events.
func (s *Server) benchStart(w http.ResponseWriter, r *http.Request) {
	var req bench.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	res, err := s.benches().Start(r.Context(), req)
	if errors.Is(err, bench.ErrBusy) {
		problem(w, 409, err.Error())
		return
	}
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 202, res)
}

func (s *Server) benchList(w http.ResponseWriter, _ *http.Request) {
	list, err := s.benches().List()
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) benchResult(w http.ResponseWriter, r *http.Request) (*bench.Result, bool) {
	res, err := s.benches().Get(r.PathValue("id"))
	if errors.Is(err, bench.ErrNotFound) {
		problem(w, 404, "no benchmark "+r.PathValue("id"))
		return nil, false
	}
	if err != nil {
		problem(w, 500, err.Error())
		return nil, false
	}
	return res, true
}

func (s *Server) benchGet(w http.ResponseWriter, r *http.Request) {
	if res, ok := s.benchResult(w, r); ok {
		writeJSON(w, 200, res)
	}
}

func (s *Server) benchCancel(w http.ResponseWriter, r *http.Request) {
	if !s.benches().Cancel(r.PathValue("id")) {
		problem(w, 409, "benchmark "+r.PathValue("id")+" is not running")
		return
	}
	w.WriteHeader(202)
}

// benchReport is the run as one HTML page, pictures included, to keep or send.
func (s *Server) benchReport(w http.ResponseWriter, r *http.Request) {
	res, ok := s.benchResult(w, r)
	if !ok {
		return
	}
	page, err := s.benches().Report(res)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// CompareRequest names two results, each by id or by the full path of a result file. A
// threshold left at 0 takes the bench section of the config.
type CompareRequest struct {
	Baseline   string            `json:"baseline"`
	Current    string            `json:"current"`
	Thresholds *bench.Thresholds `json:"thresholds,omitempty"`
}

func (s *Server) benchCompare(w http.ResponseWriter, r *http.Request) {
	var req CompareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	cfg, err := s.Config("")
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	t := bench.Thresholds{Pct: cfg.Bench.ThresholdPct, MinSeconds: cfg.Bench.MinSeconds, MinVRAMMB: cfg.Bench.MinVRAMMB}
	if q := req.Thresholds; q != nil {
		if q.Pct != 0 {
			t.Pct = q.Pct
		}
		if q.MinSeconds != 0 {
			t.MinSeconds = q.MinSeconds
		}
		if q.MinVRAMMB != 0 {
			t.MinVRAMMB = q.MinVRAMMB
		}
	}
	var sides [2]*bench.Result
	for i, ref := range []string{req.Baseline, req.Current} {
		if sides[i], err = s.benches().Load(ref); err != nil {
			status := 422
			if errors.Is(err, bench.ErrNotFound) {
				status = 404
			}
			problem(w, status, err.Error())
			return
		}
	}
	c, err := bench.Compare(sides[0], sides[1], t)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, c)
}
