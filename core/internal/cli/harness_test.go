package cli

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forja/internal/api"
	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/events"
	"forja/internal/glb"
	"forja/internal/installs"
	"forja/internal/ipc"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/provider"
	"forja/internal/secrets"
	"forja/internal/store"
	"forja/internal/sweep"
)

// fakeEngine is this computer's engine: it installs models at once and "runs" every stage by
// storing a blob made from its inputs. Export writes a real GLB, and views.detect reports
// the sheet a test sets.
type fakeEngine struct {
	st        *store.Store
	mu        sync.Mutex
	installed map[string]bool
	tasks     map[string]engineapi.Task
	sheet     string
	// ran lists the stages run, in order, and measured whether each run asked to measure.
	ran      []string
	measured []bool
	// notes are what a stage type says about its first output, such as a stand-in it made.
	notes map[string]string
}

// fakeMetrics is what an engine measures of a stage: a model's load, memory, and a mesh's stats.
func fakeMetrics(s engineapi.StageSpec, outs []engineapi.Output) *engineapi.StageMetrics {
	ram := int64(512 << 20)
	m := &engineapi.StageMetrics{Device: "cpu-pool", PeakRAMBytes: &ram, Outputs: []engineapi.OutputStats{}}
	if s.Model != "" {
		load, util := 0.25, 80.0
		m.Device, m.LoadSeconds, m.GPUUtil = "cuda:0", &load, &util
	}
	for _, o := range outs {
		if o.Kind == "mesh/glb" {
			cover := 0.6
			m.Outputs = append(m.Outputs, engineapi.OutputStats{Hash: o.Hash, Triangles: 100, Vertices: 80, Watertight: true,
				UVCoverage: &cover})
		}
	}
	return m
}

var gpu = "cuda:0"

func (e *fakeEngine) ID() string                   { return jobs.LocalProvider }
func (e *fakeEngine) Kind() provider.Kind          { return provider.KindLocal }
func (e *fakeEngine) Paid() bool                   { return false }
func (e *fakeEngine) SharesStore() bool            { return true }
func (e *fakeEngine) Health(context.Context) error { return nil }
func (e *fakeEngine) HasArtifact(_ context.Context, ref string) (bool, error) {
	return e.st.Has(ref), nil
}
func (e *fakeEngine) Upload(context.Context, string) error   { return nil }
func (e *fakeEngine) Download(context.Context, string) error { return nil }
func (e *fakeEngine) Cancel(context.Context, string) error   { return nil }

func (e *fakeEngine) Info(context.Context) (*engineapi.Info, error) {
	stages := map[string]string{}
	for _, use := range []string{"framing", "mesh.cleanup", "mesh.decimate", "mesh.uv", "texture.match", "bake",
		"mesh.lods", "export", "views.detect", "views.split"} {
		stages[use] = "1"
	}
	// Like an engine on a computer without Blender.
	noBlender := "Blender 4.2 or newer was not found on the computer that runs the engine"
	return &engineapi.Info{Stages: stages, Unavailable: map[string]string{"mesh.remesh": noBlender, "mesh.uv.smart": noBlender},
		System: map[string]any{"os": "TestOS",
			"devices":         []any{map[string]any{"id": "cuda:0", "backend": "cuda", "name": "Test GPU", "vram_bytes": 12 << 30}},
			"disk_free_bytes": 500 << 30}}, nil
}

func (e *fakeEngine) Models(context.Context) ([]engineapi.Model, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	model := func(id, task string, rank int, lic engineapi.License) engineapi.Model {
		m := engineapi.Model{ID: id, Name: strings.ToUpper(id[:1]) + id[1:], Tasks: []string{task}, Rank: rank, Fit: "fits",
			DiskGB: 1.5, License: lic, Status: "not-installed", RunsOn: &gpu, AdapterVersion: "1"}
		if e.installed[id] {
			m.Status, m.Device = "ready", &gpu
		}
		return m
	}
	mit := engineapi.License{SPDX: "MIT", Commercial: true}
	restricted := engineapi.License{SPDX: "LicenseRef-Test", Commercial: true, Restrictions: []string{"not in the EU"}}
	mv := model("shape-mv", "shape", 1, mit)
	mv.Views = engineapi.Views
	return []engineapi.Model{
		model("bg", "background-removal", 10, mit),
		model("shape", "shape", 10, mit),
		mv,
		model("paint", "texture", 10, restricted),
		model("rig", "rig", 10, mit),
	}, nil
}

func (e *fakeEngine) set(fn func(e *fakeEngine)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e)
}

func (e *fakeEngine) Run(_ context.Context, req engineapi.RunRequest, evs chan<- engineapi.Event) (*engineapi.Run, error) {
	run := &engineapi.Run{ID: "r", Status: "succeeded", Stages: map[string]engineapi.StageState{}}
	produced := map[string]string{}
	rigged := slices.ContainsFunc(req.Stages, func(s engineapi.StageSpec) bool { return s.Use == "rig" })
	for _, s := range req.Stages {
		var inputs []string
		for _, in := range s.Inputs {
			if id, ok := strings.CutPrefix(in, "stage:"); ok {
				in = produced[id]
			}
			inputs = append(inputs, in)
		}
		blob := []byte(s.ID + s.Model + strings.Join(inputs, ",") + strings.Join(s.Views, ","))
		outs := []engineapi.Output{{Kind: "mesh/glb"}}
		switch s.Use {
		case "views.detect":
			e.mu.Lock()
			blob = []byte(e.sheet)
			e.mu.Unlock()
			if len(blob) == 0 {
				blob = []byte(`{"sheet": false, "size": [100, 100], "parts": []}`)
			}
			outs[0].Kind = "application/json"
		case "views.split":
			views, _ := s.Params["views"].([]map[string]any)
			outs = nil
			for _, v := range views {
				ref, _ := e.st.PutBytes(append(blob, fmt.Sprint(v["view"])...))
				outs = append(outs, engineapi.Output{Hash: ref, Kind: "image/png"})
			}
		case "export":
			blob = chestGLB(len(inputs), rigged)
		case "rig":
			profile, _ := e.st.PutBytes([]byte(`{"profile": "humanoid"}`))
			outs = append(outs, engineapi.Output{Hash: profile, Kind: "application/json"})
		}
		if len(outs) > 0 && outs[0].Hash == "" {
			outs[0].Hash, _ = e.st.PutBytes(blob)
		}
		if len(outs) > 0 {
			produced[s.ID] = outs[0].Hash
			e.mu.Lock()
			outs[0].Note = e.notes[s.Use]
			e.mu.Unlock()
		}
		secs := 0.01
		evs <- engineapi.Event{Type: "stage.started", Data: map[string]any{"stage": s.ID}}
		st := engineapi.StageState{Status: "succeeded", Seconds: &secs, Outputs: outs}
		if req.Measure {
			st.Metrics = fakeMetrics(s, outs)
			if s.Model != "" {
				vram := int64(2 << 30)
				st.PeakVRAMBytes = &vram
			}
		}
		run.Stages[s.ID] = st
		e.mu.Lock()
		e.ran = append(e.ran, s.ID)
		e.measured = append(e.measured, req.Measure)
		e.mu.Unlock()
	}
	return run, nil
}

func (e *fakeEngine) Checks(context.Context, []string, []string) (*engineapi.Checks, error) {
	return &engineapi.Checks{Lane: "cuda", Checks: []engineapi.Check{
		{ID: "uv", Label: "uv", OK: true, Found: "0.9", Need: "0.5 or newer"},
		{ID: "msvc", Label: "C++ build tools", Found: "none", Need: "MSVC 14", Fix: &engineapi.Fix{
			Label: "Install the build tools", Runnable: true, Steps: []string{"Open the Visual Studio Installer"}}},
	}}, nil
}

func (e *fakeEngine) task(kind, target string) *engineapi.Task {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := engineapi.Task{ID: fmt.Sprintf("t%d", len(e.tasks)+1), Kind: kind, Target: target, Status: "running", Version: 1,
		Steps: []engineapi.TaskStep{{ID: "go", Label: kind + " " + target, Status: "running"}}, CreatedAt: float64(time.Now().Unix())}
	e.tasks[t.ID] = t
	return &t
}

func (e *fakeEngine) Install(_ context.Context, model string, _ engineapi.InstallRequest) (*engineapi.Task, error) {
	e.set(func(e *fakeEngine) { e.installed[model] = true })
	return e.task("install", model), nil
}

func (e *fakeEngine) Remove(_ context.Context, model string) (*engineapi.Task, error) {
	e.set(func(e *fakeEngine) { delete(e.installed, model) })
	return e.task("remove", model), nil
}

func (e *fakeEngine) Fix(_ context.Context, check string) (*engineapi.Task, error) {
	return e.task("fix", check), nil
}

func (e *fakeEngine) CancelTask(context.Context, string) error { return nil }

func (e *fakeEngine) FollowTask(_ context.Context, id string, states chan<- engineapi.Task) error {
	e.mu.Lock()
	t := e.tasks[id]
	e.mu.Unlock()
	t.Status, t.Version = "succeeded", 2
	t.Steps = []engineapi.TaskStep{{ID: "go", Label: t.Steps[0].Label, Status: "done"}}
	t.Log = []string{"all done"}
	states <- t
	return nil
}

// chestGLB is a GLB with a full-detail mesh of 100 triangles and two LODs; rigged, every
// level is skinned to a humanoid skeleton.
func chestGLB(seed int, rigged bool) []byte {
	nodes := []any{map[string]any{"name": "chest_LOD0", "mesh": 0}, map[string]any{"name": "chest_LOD1", "mesh": 0},
		map[string]any{"name": "chest_LOD2", "mesh": 0}}
	doc := map[string]any{
		"asset":  map[string]string{"version": "2.0", "generator": "fake " + strconv.Itoa(seed)},
		"scene":  0,
		"scenes": []any{map[string]any{"nodes": []int{0, 1, 2}}},
		"meshes": []any{map[string]any{"primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": 0},
			"indices": 1, "material": 0}}}},
		"accessors": []any{map[string]int{"count": 80}, map[string]int{"count": 300}},
	}
	if rigged {
		var joints []int
		for _, name := range glb.HumanoidBones {
			joints = append(joints, len(nodes))
			nodes = append(nodes, map[string]any{"name": name})
		}
		for _, n := range nodes[:3] {
			n.(map[string]any)["skin"] = 0
		}
		doc["skins"] = []any{map[string]any{"joints": joints, "extras": map[string]string{"forja_skeleton": "humanoid"}}}
	}
	doc["nodes"] = nodes
	js, _ := json.Marshal(doc)
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [5]uint32{0x46546C67, 2, uint32(20 + len(js)), uint32(len(js)), 0x4E4F534A})
	b.Write(js)
	return b.Bytes()
}

// harness is a core serving the real API over a private socket, with fakeEngine as this
// computer's engine, and the CLI pointed at it.
type harness struct {
	t        *testing.T
	addr     string
	dir      string
	store    *store.Store
	eng      *fakeEngine
	reg      *provider.Registry
	stopped  chan struct{}
	stopOnce sync.Once
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("FORJA_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("FORJA_SOCKET", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	secrets.InMemory()
	// Nothing in a test may start a real engine, which the repository's checkout would find.
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserFile(), []byte("[engine]\ncommand = [\"forja-test-no-such-engine\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := library.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{st: st, installed: map[string]bool{"bg": true, "shape": true, "paint": true},
		tasks: map[string]engineapi.Task{}}
	reg := provider.NewRegistry()
	reg.Add(eng)
	hub := events.NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cat := catalog.New(reg, db, nil, func(id string) { hub.Publish(api.EvModelsChanged, map[string]string{"provider": id}) })
	cfg := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	runner := jobs.NewRunner(db, st, hub, reg, cat, cfg, log, "test", 4)
	sweeper := sweep.New(db, st, runner, log)
	runner.OnIdle = sweeper.Nudge
	sweeper.Start()
	h := &harness{t: t, dir: dir, store: st, eng: eng, reg: reg, stopped: make(chan struct{})}
	s := &api.Server{
		Version: "test", DataDir: filepath.Join(dir, "data"), DB: db, Store: st, Hub: hub, Providers: reg, Catalog: cat,
		Log: log, Config: cfg, Runner: runner, Sweeper: sweeper, Installs: installs.New(reg, cat, hub, log),
		Engine:   localengine.New(func() (*config.Loaded, error) { return config.Load("", nil) }, st.Root, dir, log),
		Shutdown: func() { h.stopOnce.Do(func() { close(h.stopped) }) },
	}
	h.addr = testAddress(t)
	ln, err := ipc.Listen(h.addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		for deadline := time.Now().Add(5 * time.Second); runner.Busy() && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		_ = srv.Close()
		sweeper.Close()
		db.Close()
	})
	return h
}

var pipes atomic.Int64

func testAddress(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\forja-test-%d-%d`, os.Getpid(), pipes.Add(1))
	}
	// Unix sockets have a short path limit, which t.TempDir can exceed on macOS.
	dir, err := os.MkdirTemp("", "fj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "core.sock")
}

type result struct {
	out, err string
	code     int
}

// cli runs the forja command line against the harness's core, with stdin as given.
func (h *harness) cli(stdin string, args ...string) result {
	return h.cliAt(false, stdin, args...)
}

// cliAt is cli as if someone at a terminal typed stdin.
func (h *harness) cliAt(tty bool, stdin string, args ...string) result {
	h.t.Helper()
	var out, errOut bytes.Buffer
	a := &app{version: "test", in: strings.NewReader(stdin), out: &out, errOut: &errOut, tty: tty}
	code := a.run(append([]string{"--socket", h.addr}, args...))
	if a.core != nil {
		h.t.Fatal("the command started its own core instead of using the harness's")
	}
	return result{out.String(), errOut.String(), code}
}

// ok runs the command line and fails the test unless it exits 0.
func (h *harness) ok(args ...string) string {
	h.t.Helper()
	r := h.cli("", args...)
	if r.code != 0 {
		h.t.Fatalf("forja %s: exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), r.code, r.out, r.err)
	}
	return r.out
}

// okJSON runs the command line with --json and decodes what it prints.
func (h *harness) okJSON(out any, args ...string) {
	h.t.Helper()
	raw := h.ok(append([]string{"--json"}, args...)...)
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		h.t.Fatalf("forja --json %s: %v\n%s", strings.Join(args, " "), err, raw)
	}
}

// syncBuffer is a buffer a command can write while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// image writes a reference image the core can read by path.
func (h *harness) image(name string) string {
	h.t.Helper()
	path := filepath.Join(h.dir, name)
	if err := os.WriteFile(path, []byte("fake image "+name), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return path
}
