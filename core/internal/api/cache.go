package api

import (
	"errors"
	"net/http"

	"forja/internal/cache"
)

// CacheCleared is what clearing the cache removed, and how the store divides after it.
type CacheCleared struct {
	Freed cache.Freed `json:"freed"`
	Usage cache.Usage `json:"usage"`
}

func (s *Server) cacheGet(w http.ResponseWriter, r *http.Request) {
	u, err := s.Cache.Usage(r.Context())
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, u)
}

// cacheClear removes every reusable stage output it may now. The library keeps its files;
// jobs that ran before simply run their stages again.
func (s *Server) cacheClear(w http.ResponseWriter, r *http.Request) {
	freed, u, err := s.Cache.Clear(r.Context())
	if errors.Is(err, cache.ErrBusy) {
		problem(w, 409, err.Error())
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, CacheCleared{Freed: freed, Usage: u})
}
