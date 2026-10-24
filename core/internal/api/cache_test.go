package api

import (
	"os"
	"testing"
	"time"

	"forja/internal/cache"
)

func TestTheCacheIsMeasuredAndClearedApartFromTheLibrary(t *testing.T) {
	ts, s, _ := newServer(t)
	glb := twoChests(t, s)
	left := []byte("a stage output no job or asset uses")
	ref, _ := s.Store.PutBytes(left)
	p, _ := s.Store.Path(ref)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	glbInfo, _ := os.Stat(must(s.Store.Path(glb)))

	var u cache.Usage
	if code := call(t, ts, "GET", "/v1/cache", nil, &u); code != 200 {
		t.Fatalf("status %d", code)
	}
	if u.Objects != 2 || u.CacheBytes != int64(len(left)) || u.LibraryBytes != glbInfo.Size() {
		t.Fatalf("usage %+v", u)
	}

	// While a job runs (here, while the runner is held), clearing waits.
	s.Runner.WhenIdle(func() {
		if code := call(t, ts, "DELETE", "/v1/cache", nil, nil); code != 409 {
			t.Errorf("clearing during a job answered %d", code)
		}
	})
	evs, stop := s.Hub.Subscribe()
	defer stop()
	var res CacheCleared
	if code := call(t, ts, "DELETE", "/v1/cache", nil, &res); code != 200 {
		t.Fatalf("status %d", code)
	}
	if res.Freed.Objects != 1 || res.Freed.Bytes != int64(len(left)) || res.Usage.CacheBytes != 0 || s.Store.Has(ref) || !s.Store.Has(glb) {
		t.Fatalf("cleared %+v", res)
	}
	select {
	case ev := <-evs:
		if ev.Type != cache.EvChanged {
			t.Fatalf("event %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
}
