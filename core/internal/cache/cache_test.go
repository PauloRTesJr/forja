package cache

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/store"
)

type gate struct{ busy bool }

func (g *gate) WhenIdle(fn func()) bool {
	if g.busy {
		return false
	}
	fn()
	return true
}

// world is a library and its store: a finished job with its asset, a job waiting for an
// answer, cached stage outputs of several ages, and a leftover nothing points at.
type world struct {
	db   *library.DB
	st   *store.Store
	refs map[string]string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	dir := t.TempDir()
	db, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, _ := store.Open(dir)
	w := &world{db: db, st: st, refs: map[string]string{}}
	now := time.Now()
	// name, size, last used
	for _, b := range []struct {
		name string
		size int
		ago  time.Duration
	}{
		{"glb", 100, 900 * time.Hour}, {"thumb", 10, 900 * time.Hour}, {"input", 20, 900 * time.Hour},
		{"leftover", 50, 800 * time.Hour}, {"shape", 300, 700 * time.Hour}, {"texture", 200, 600 * time.Hour},
		{"texture-2", 40, 590 * time.Hour}, {"recent", 400, time.Minute},
		{"waiting-input", 30, 900 * time.Hour}, {"cutout", 60, 900 * time.Hour}, {"waiting-shape", 70, 900 * time.Hour},
	} {
		content := make([]byte, b.size)
		copy(content, b.name)
		ref, err := st.PutBytes(content)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := st.Path(ref)
		at := now.Add(-b.ago)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		w.refs[b.name] = ref
	}
	ctx := context.Background()
	out := func(names ...string) []library.Output {
		var o []library.Output
		for _, n := range names {
			o = append(o, library.Output{Hash: w.refs[n], Kind: "mesh/glb"})
		}
		return o
	}
	must(t, db.SaveJob(ctx, &library.Job{ID: "done", Name: "chest", Preset: "draft", Input: w.refs["input"],
		Status: library.Succeeded, CreatedAt: now, AssetID: "a",
		Stages: []library.Stage{{ID: "shape", Outputs: out("shape")}, {ID: "paint", Outputs: out("texture", "texture-2")},
			{ID: "export", Outputs: out("glb")}}}))
	must(t, db.SaveAsset(ctx, &library.Asset{ID: "a", JobID: "done", Name: "chest", Preset: "draft", GLB: w.refs["glb"],
		Thumbnail: w.refs["thumb"], Input: w.refs["input"], CreatedAt: now}))
	must(t, db.SaveJob(ctx, &library.Job{ID: "asking", Name: "sheet", Preset: "draft", Input: w.refs["waiting-input"],
		Status: library.Waiting, CreatedAt: now, Sheet: &library.Sheet{Cutout: w.refs["cutout"]},
		Stages: []library.Stage{{ID: "shape", Outputs: out("waiting-shape")}}}))
	must(t, db.CachePut(ctx, "k-shape", out("shape"), 1))
	must(t, db.CachePut(ctx, "k-paint", out("texture", "texture-2"), 1))
	must(t, db.CachePut(ctx, "k-export", out("glb"), 1))
	must(t, db.CachePut(ctx, "k-recent", out("recent"), 1))
	return w
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (w *world) has(names ...string) []string {
	var out []string
	for _, n := range names {
		if w.st.Has(w.refs[n]) {
			out = append(out, n)
		}
	}
	return out
}

func (w *world) cached(t *testing.T, keys ...string) []string {
	var out []string
	for _, k := range keys {
		if _, ok, err := w.db.CacheGet(context.Background(), k); err != nil {
			t.Fatal(err)
		} else if ok {
			out = append(out, k)
		}
	}
	return out
}

var keptNames = []string{"glb", "thumb", "input", "waiting-input", "cutout", "waiting-shape"}

func TestTheLibraryCountsApartFromTheCache(t *testing.T) {
	w := newWorld(t)
	u, reusable, err := scan(context.Background(), w.db, w.st)
	if err != nil {
		t.Fatal(err)
	}
	if u.Objects != 11 || u.LibraryBytes != 100+10+20+30+60+70 || u.CacheBytes != 50+300+200+40+400 {
		t.Fatalf("usage %+v", u)
	}
	var order []string
	for _, o := range reusable {
		for n, r := range w.refs {
			if r == o.Ref {
				order = append(order, n)
			}
		}
	}
	if order[0] != "leftover" || order[1] != "shape" || order[len(order)-1] != "recent" {
		t.Fatalf("least recently used first: %v", order)
	}
}

func TestEvictionRemovesTheLeastRecentlyUsedAndForgetsTheirEntries(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	// 990 cache bytes; down to 640 takes the leftover (50) and the shape (300).
	f, u, err := evict(ctx, w.db, w.st, 640, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.Objects != 2 || f.Bytes != 350 || f.Entries != 1 || u.CacheBytes != 640 {
		t.Fatalf("freed %+v, usage %+v", f, u)
	}
	if got := w.has("leftover", "shape", "texture"); !slices.Equal(got, []string{"texture"}) {
		t.Fatalf("left %v", got)
	}
	if got := w.cached(t, "k-shape", "k-paint", "k-export", "k-recent"); !slices.Equal(got, []string{"k-paint", "k-export", "k-recent"}) {
		t.Fatalf("entries left %v", got)
	}

	// Clearing takes all it may: the library stays, and so does what was used a minute ago.
	f, u, err = evict(ctx, w.db, w.st, 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if f.Objects != 2 || f.Entries != 1 || u.CacheBytes != 400 {
		t.Fatalf("freed %+v, usage %+v", f, u)
	}
	if got := w.has(append(keptNames, "recent", "texture", "texture-2")...); !slices.Equal(got, append(keptNames, "recent")) {
		t.Fatalf("left %v", got)
	}
	// The export's entry points at the asset's GLB, which stays: a new job can still reuse it.
	if got := w.cached(t, "k-paint", "k-export", "k-recent"); !slices.Equal(got, []string{"k-export", "k-recent"}) {
		t.Fatalf("entries left %v", got)
	}
}

func TestTheKeeperTrimsPastTheLimitAndNeverWhileJobsRun(t *testing.T) {
	w := newWorld(t)
	hub := events.NewHub()
	evs, stop := hub.Subscribe()
	defer stop()
	g := &gate{busy: true}
	limit := int64(1000)
	k := New(w.db, w.st, g, func() int64 { return limit }, hub, slog.New(slog.NewTextHandler(io.Discard, nil)))

	k.trim() // 990 bytes is under 1000
	if got := w.has("leftover"); len(got) != 1 {
		t.Fatal("trimmed under the limit")
	}
	limit = 720
	k.trim() // past it, but a job runs
	if got := w.has("leftover"); len(got) != 1 {
		t.Fatal("trimmed while a job ran")
	}
	if _, _, err := k.Clear(context.Background()); err != ErrBusy {
		t.Fatalf("clearing while a job ran: %v", err)
	}
	g.busy = false
	k.trim() // to nine tenths of the limit, 648: the leftover and the shape go
	if got := w.has("leftover", "shape", "texture"); !slices.Equal(got, []string{"texture"}) {
		t.Fatalf("left %v", got)
	}
	select {
	case ev := <-evs:
		u, ok := ev.Data.(Usage)
		if ev.Type != EvChanged || !ok || u.CacheBytes != 640 || u.LimitBytes != 720 {
			t.Fatalf("event %+v", ev)
		}
	default:
		t.Fatal("no event after a trim")
	}

	u, err := k.Usage(context.Background())
	if err != nil || u.CacheBytes != 640 || u.LimitBytes != 720 {
		t.Fatalf("usage %+v, %v", u, err)
	}
	f, u, err := k.Clear(context.Background())
	if err != nil || f.Bytes != 240 || u.CacheBytes != 400 {
		t.Fatalf("cleared %+v, usage %+v, %v", f, u, err)
	}
}

func TestNudgesWakeTheKeeperAndCloseStopsIt(t *testing.T) {
	w := newWorld(t)
	k := New(w.db, w.st, &gate{}, func() int64 { return 100 }, events.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	k.Start()
	k.Nudge()
	k.Nudge() // merges with the first
	deadline := time.Now().Add(5 * time.Second)
	for len(w.has("texture")) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	k.Close()
	if got := w.has("leftover", "shape", "texture", "texture-2", "recent"); !slices.Equal(got, []string{"recent"}) {
		t.Fatalf("left %v", got)
	}
}
