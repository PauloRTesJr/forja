package sweep

import (
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

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
// answer, stage outputs of the finished job, a leftover nothing points at, and a file stored
// a minute ago.
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
	for _, b := range []struct {
		name string
		ago  time.Duration
	}{
		{"glb", 900 * time.Hour}, {"thumb", 900 * time.Hour}, {"input", 900 * time.Hour},
		{"leftover", 800 * time.Hour}, {"shape", 700 * time.Hour}, {"texture", 600 * time.Hour}, {"recent", time.Minute},
		{"waiting-input", 900 * time.Hour}, {"cutout", 900 * time.Hour}, {"waiting-shape", 900 * time.Hour},
	} {
		ref, err := st.PutBytes([]byte(b.name))
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
		Stages: []library.Stage{{ID: "shape", Outputs: out("shape")}, {ID: "paint", Outputs: out("texture")},
			{ID: "export", Outputs: out("glb")}}}))
	must(t, db.SaveAsset(ctx, &library.Asset{ID: "a", JobID: "done", Name: "chest", Preset: "draft", GLB: w.refs["glb"],
		Thumbnail: w.refs["thumb"], Input: w.refs["input"], CreatedAt: now}))
	must(t, db.SaveJob(ctx, &library.Job{ID: "asking", Name: "sheet", Preset: "draft", Input: w.refs["waiting-input"],
		Status: library.Waiting, CreatedAt: now, Sheet: &library.Sheet{Cutout: w.refs["cutout"]},
		Stages: []library.Stage{{ID: "shape", Outputs: out("waiting-shape")}}}))
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

var (
	keptNames = []string{"glb", "thumb", "input", "waiting-input", "cutout", "waiting-shape"}
	all       = append(slices.Clone(keptNames), "leftover", "shape", "texture", "recent")
)

func TestASweepKeepsTheLibraryAndWhatWasJustUsed(t *testing.T) {
	w := newWorld(t)
	s, err := leftovers(context.Background(), w.db, w.st, time.Now())
	if err != nil || s.Objects != 3 {
		t.Fatalf("swept %+v, %v", s, err)
	}
	if got := w.has(all...); !slices.Equal(got, append(keptNames, "recent")) {
		t.Fatalf("left %v", got)
	}
}

func TestTheSweeperWaitsForJobsAndRunsWhenNudged(t *testing.T) {
	w := newWorld(t)
	g := &gate{busy: true}
	s := New(w.db, w.st, g, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.sweep()
	if got := w.has("leftover"); len(got) != 1 {
		t.Fatal("swept while a job ran")
	}
	g.busy = false
	s.Start()
	s.Nudge() // merges with the one Start sends
	deadline := time.Now().Add(5 * time.Second)
	for len(w.has("leftover", "shape", "texture")) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	s.Close()
	if got := w.has(all...); !slices.Equal(got, append(keptNames, "recent")) {
		t.Fatalf("left %v", got)
	}
}
