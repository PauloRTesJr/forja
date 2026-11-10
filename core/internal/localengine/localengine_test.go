package localengine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forja/internal/config"
)

// The test binary stands in for the engine: started with fakeEngineEnv set, it behaves as that
// mode says instead of running tests.
const (
	fakeEngineEnv = "FORJA_TEST_FAKE_ENGINE"
	fakeStartsEnv = "FORJA_TEST_FAKE_ENGINE_STARTS"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEngineEnv); mode != "" {
		fakeEngine(mode)
		return
	}
	os.Exit(m.Run())
}

// fakeEngine listens, records its port in the starts file (one line per process), then:
//
//	serve   announces the port, as the engine does once it is ready
//	slow    announces it after half a second
//	silent  never announces it
//	crash   says why on stdout and stderr, then exits with status 3
//	tree    starts a "silent" child, as `uv run` starts the interpreter, then serves
//
// GET /die makes it exit as a crash would.
func fakeEngine(mode string) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(9)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	f, err := os.OpenFile(os.Getenv(fakeStartsEnv), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		os.Exit(9)
	}
	fmt.Fprintln(f, port)
	f.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/die", func(http.ResponseWriter, *http.Request) { os.Exit(7) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	go func() { _ = http.Serve(ln, mux) }()
	switch mode {
	case "crash":
		fmt.Println("loading manifests")
		// Long enough for callers to join the start, and for the core to copy the line above into
		// the log before the line below lands there.
		time.Sleep(300 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "ModuleNotFoundError: No module named 'forja_engine'")
		os.Exit(3)
	case "slow":
		time.Sleep(500 * time.Millisecond)
	case "silent":
		select {}
	case "tree":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), fakeEngineEnv+"=silent")
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
	}
	fmt.Println(listeningPrefix + fmt.Sprint(port))
	select {}
}

type fake struct {
	t      *testing.T
	m      *Manager
	starts string
	log    string
	said   *messages
}

// messages records what the manager logged.
type messages struct {
	slog.Handler
	mu   sync.Mutex
	msgs []string
}

func (m *messages) Handle(_ context.Context, r slog.Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, r.Message)
	return nil
}

func (m *messages) count(msg string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.msgs {
		if s == msg {
			n++
		}
	}
	return n
}

// newFake is a manager whose engine is the test binary in mode, with a start timeout.
func newFake(t *testing.T, mode, timeout string) *fake {
	t.Helper()
	dir := t.TempDir()
	f := &fake{t: t, starts: filepath.Join(dir, "starts"), log: filepath.Join(dir, "logs", "engine.log"),
		said: &messages{Handler: slog.NewTextHandler(io.Discard, nil)}}
	t.Setenv(fakeEngineEnv, mode)
	t.Setenv(fakeStartsEnv, f.starts)
	cfg := &config.Loaded{}
	cfg.Engine.Command = []string{os.Args[0]}
	cfg.Engine.StartTimeout = timeout
	f.m = New(func() (*config.Loaded, error) { return cfg, nil }, filepath.Join(dir, "store"), filepath.Join(dir, "logs"),
		slog.New(f.said))
	t.Cleanup(f.m.Stop)
	return f
}

// ports are the ports of the engines started so far, one per process.
func (f *fake) ports() []string {
	raw, _ := os.ReadFile(f.starts)
	return strings.Fields(string(raw))
}

// waitStarts waits until n engine processes have started.
func (f *fake) waitStarts(n int) []string {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if p := f.ports(); len(p) >= n {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("%d engine processes started, want %d", len(f.ports()), n)
	return nil
}

// gone waits until nothing listens on a fake engine's port: its process ended.
func (f *fake) gone(port string) {
	f.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, time.Second)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatalf("the engine process on port %s is still running", port)
}

func TestTheEngineIsToldWhichBlenderTheUserSet(t *testing.T) {
	cfg := &config.Loaded{}
	if env := Env(cfg); len(env) != 0 {
		t.Fatalf("without blender.path the engine looks for Blender itself, got %v", env)
	}
	cfg.Blender.Path = "/opt/blender/blender"
	if env := Env(cfg); !slices.Equal(env, []string{"FORJA_BLENDER=/opt/blender/blender"}) {
		t.Fatalf("env %v", env)
	}
}

// A request the app dropped still reached the engine manager, which started an engine for
// it and killed it at once: eight dropped requests logged eight starts and eight "exited
// unexpectedly" within 150 ms on a fresh data folder.
func TestACallerThatGaveUpStartsNoEngine(t *testing.T) {
	f := newFake(t, "serve", "10s")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 8 {
		if _, _, err := f.m.Endpoint(ctx); err == nil {
			t.Fatal("a cancelled caller got an engine")
		}
	}
	if n := f.said.count("engine starting"); n != 0 {
		t.Fatalf("%d engine processes were started for callers that had given up", n)
	}
	if n := f.said.count("engine exited unexpectedly; it restarts on the next local job"); n != 0 {
		t.Fatalf("%d crashes were logged for an engine nobody ran", n)
	}
	if f.m.Running() {
		t.Fatal("the engine runs")
	}
}

func TestACallerGivingUpLeavesTheStartForTheNext(t *testing.T) {
	f := newFake(t, "slow", "10s")
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, _, err := f.m.Endpoint(ctx)
		first <- err
	}()
	f.waitStarts(1)
	cancel()
	if err := <-first; err == nil {
		t.Fatal("the caller that gave up got an engine")
	}
	url, _, err := f.m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ports := f.ports()
	if len(ports) != 1 {
		t.Fatalf("%d engine processes started, want the one the first caller began", len(ports))
	}
	if want := "http://127.0.0.1:" + ports[0]; url != want {
		t.Fatalf("engine at %s, want %s", url, want)
	}
}

func TestEveryoneWaitingSharesOneStart(t *testing.T) {
	f := newFake(t, "slow", "10s")
	urls := make([]string, 8)
	var wg sync.WaitGroup
	for i := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			urls[i], _, _ = f.m.Endpoint(context.Background())
		}()
	}
	wg.Wait()
	ports := f.ports()
	if len(ports) != 1 {
		t.Fatalf("%d engine processes started for 8 callers, want 1", len(ports))
	}
	for _, u := range urls {
		if u != "http://127.0.0.1:"+ports[0] {
			t.Fatalf("callers got %v", urls)
		}
	}
	if !f.m.Running() || f.m.Starting() {
		t.Fatalf("running %v, starting %v", f.m.Running(), f.m.Starting())
	}
}

func TestAFailedStartIsToldToEveryoneWaitingWithTheEnginesLastWords(t *testing.T) {
	f := newFake(t, "crash", "10s")
	errs := make([]error, 5)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i > 0 {
				f.waitStarts(1) // join the start the first caller began
			}
			_, _, errs[i] = f.m.Endpoint(context.Background())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil || !strings.Contains(err.Error(), "No module named 'forja_engine'") || !strings.Contains(err.Error(), f.log) {
			t.Fatalf("want the engine's last words and its log's path, got: %v", err)
		}
	}
	if n := len(f.ports()); n != 1 {
		t.Fatalf("%d engine processes started, want 1: a failed start is not repeated for each caller that waited", n)
	}
	raw, _ := os.ReadFile(f.log)
	if !strings.Contains(string(raw), "loading manifests") {
		t.Fatalf("the engine's output is missing from its log:\n%s", raw)
	}
	// The next caller tries again.
	if _, _, err := f.m.Endpoint(context.Background()); err == nil {
		t.Fatal("a crashing engine came up")
	}
	if n := len(f.ports()); n != 2 {
		t.Fatalf("%d engine processes started, want 2", n)
	}
}

func TestAskingWhetherTheEngineRunsNeverWaitsForAStart(t *testing.T) {
	f := newFake(t, "silent", "30s")
	started := make(chan error, 1)
	go func() {
		_, _, err := f.m.Endpoint(context.Background())
		started <- err
	}()
	port := f.waitStarts(1)[0]
	answered := make(chan bool, 1)
	go func() { answered <- f.m.Running() }()
	select {
	case running := <-answered:
		if running {
			t.Fatal("an engine that has not answered yet counts as running")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Running waited for the engine to start")
	}
	if !f.m.Starting() {
		t.Fatal("the engine does not show as starting")
	}
	// Stopping calls the start off, and leaves no process behind.
	stopped := make(chan struct{})
	go func() { f.m.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for the engine to start")
	}
	if err := <-started; err == nil {
		t.Fatal("the stopped start gave an engine")
	}
	f.gone(port)
}

func TestAnEngineThatNeverAnswersIsEndedAfterTheStartTimeout(t *testing.T) {
	f := newFake(t, "silent", "400ms")
	_, _, err := f.m.Endpoint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not start within 400ms") {
		t.Fatalf("got %v", err)
	}
	f.gone(f.waitStarts(1)[0])
	if f.m.Running() || f.m.Starting() {
		t.Fatal("the engine shows as running or starting after its start timed out")
	}
}

func TestTheEngineStartsAgainAfterItDies(t *testing.T) {
	f := newFake(t, "serve", "10s")
	exited := make(chan struct{}, 1)
	f.m.Exited = func() { exited <- struct{}{} }
	url, _, err := f.m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.Get(url + "/die"); err == nil {
		resp.Body.Close()
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatal("nobody was told the engine died")
	}
	if f.m.Running() {
		t.Fatal("a dead engine shows as running")
	}
	again, _, err := f.m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if again == url || len(f.ports()) != 2 {
		t.Fatalf("want a second engine, got %s after %s (%d started)", again, url, len(f.ports()))
	}
}

func TestStoppingEndsEverythingTheEngineStarted(t *testing.T) {
	f := newFake(t, "tree", "10s")
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	ports := f.waitStarts(2) // the launcher and the child it started
	f.m.Stop()
	for _, p := range ports {
		f.gone(p)
	}
	if f.m.Running() {
		t.Fatal("the engine shows as running after Stop")
	}
}

func TestAClosedManagerStartsNoEngine(t *testing.T) {
	f := newFake(t, "serve", "10s")
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	port := f.ports()[0]
	f.m.Close()
	f.gone(port)
	// A job still running when the core stops asks for the engine again.
	if _, _, err := f.m.Endpoint(context.Background()); err != ErrClosed {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	if n := len(f.ports()); n != 1 {
		t.Fatalf("%d engine processes started, want only the first", n)
	}
}

// changes counts the times the manager said its state changed.
func changes(f *fake) *atomic.Int32 {
	var n atomic.Int32
	f.m.Changed = func() { n.Add(1) }
	return &n
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Changing VRAM reserve while a job ran was saved and never applied, and changing it while
// idle stopped a warm engine at once, again for every edit.
func TestAnIdleEngineRestartsOnceForABurstOfSettingChanges(t *testing.T) {
	f := newFake(t, "serve", "10s")
	f.m.ApplyDelay = 150 * time.Millisecond
	n := changes(f)
	if got := f.m.Apply(); got != "saved" {
		t.Fatalf("with no engine running Apply said %q", got)
	}
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if got := f.m.Apply(); got != "restarted" {
			t.Fatalf("an idle engine: %q", got)
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !f.m.State().RestartPending || !f.m.Running() {
		t.Fatalf("the restart is not waiting for the burst to end: %+v", f.m.State())
	}
	waitFor(t, "the restart", func() bool { return !f.m.Running() })
	if st := f.m.State(); st.State != "stopped" || st.RestartPending {
		t.Fatalf("after the restart: %+v", st)
	}
	time.Sleep(300 * time.Millisecond)
	if got := f.said.count("engine stopped"); got != 1 {
		t.Fatalf("the engine was stopped %d times for one burst", got)
	}
	// It starts again, with whatever the settings are then, when something needs it.
	if _, _, err := f.m.Endpoint(context.Background()); err != nil || len(f.ports()) != 2 {
		t.Fatalf("%v, %d starts", err, len(f.ports()))
	}
	waitFor(t, "the app to hear of the changes", func() bool { return n.Load() >= 4 })
}

func TestABusyEngineRestartsWhenTheLastUseEnds(t *testing.T) {
	f := newFake(t, "serve", "10s")
	f.m.ApplyDelay = 20 * time.Millisecond
	var busy atomic.Bool
	busy.Store(true)
	f.m.WhenIdle = func(fn func()) bool {
		if busy.Load() {
			return false
		}
		fn()
		return true
	}
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.m.Apply(); got != "pending" {
		t.Fatalf("a busy engine: %q", got)
	}
	time.Sleep(200 * time.Millisecond)
	if st := f.m.State(); st.State != "running" || !st.RestartPending {
		t.Fatalf("a job still runs, the engine must keep running with the restart pending: %+v", st)
	}
	busy.Store(false)
	f.m.Settle() // what the core does when the last job or install ends
	waitFor(t, "the restart after the job", func() bool { return !f.m.Running() })
	if f.m.State().RestartPending {
		t.Fatal("the restart is still pending after it happened")
	}
}

func TestTheStateSaysWhyTheEngineIsNotUp(t *testing.T) {
	f := newFake(t, "crash", "10s")
	n := changes(f)
	if st := f.m.State(); st.State != "stopped" || st.Error != "" {
		t.Fatalf("at first: %+v", st)
	}
	if _, _, err := f.m.Endpoint(context.Background()); err == nil {
		t.Fatal("the crashing engine started")
	}
	st := f.m.State()
	if st.State != "failed" || st.Error != exitedWhileStarting || !strings.Contains(st.Detail, "ModuleNotFoundError") {
		t.Fatalf("after a failed start: %+v", st)
	}
	waitFor(t, "the app to hear of the start and of the failure", func() bool { return n.Load() >= 2 })
	f.m.Stop()
	if st := f.m.State(); st.State != "stopped" || st.Error != "" {
		t.Fatalf("a stop on purpose is not a failure: %+v", st)
	}

	// An engine that dies on its own is a failure too, until it starts again.
	f = newFake(t, "serve", "10s")
	url, _, err := f.m.Endpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st := f.m.State(); st.State != "running" {
		t.Fatalf("%+v", st)
	}
	if resp, err := http.Get(url + "/die"); err == nil {
		resp.Body.Close()
	}
	waitFor(t, "the crash to show", func() bool { return f.m.State().State == "failed" })
	if st := f.m.State(); !strings.Contains(st.Error, "stopped unexpectedly") {
		t.Fatalf("%+v", st)
	}
	if _, _, err := f.m.Endpoint(context.Background()); err != nil || f.m.State().State != "running" {
		t.Fatalf("%v, %+v", err, f.m.State())
	}
}

func TestTheStateShowsAStartUnderWay(t *testing.T) {
	f := newFake(t, "slow", "10s")
	go func() { _, _, _ = f.m.Endpoint(context.Background()) }()
	waitFor(t, "starting", func() bool { return f.m.State().State == "starting" })
	waitFor(t, "running", func() bool { return f.m.State().State == "running" })
}

// A pairing code stays on the screen after it stopped working.
func TestAPairingCodeDiesWithItsTimeOrItsEngine(t *testing.T) {
	f := newFake(t, "serve", "10s")
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.m.PairingExpires().IsZero() {
		t.Fatal("a code nobody asked for")
	}
	// Pairing itself needs a sharing engine; what it records is what matters here.
	f.m.mu.Lock()
	f.m.pairProc, f.m.pairExpires = f.m.proc, time.Now().Add(time.Hour)
	f.m.mu.Unlock()
	if f.m.PairingExpires().IsZero() {
		t.Fatal("the code has gone before its time")
	}
	f.m.mu.Lock()
	f.m.pairExpires = time.Now().Add(-time.Second)
	f.m.mu.Unlock()
	if !f.m.PairingExpires().IsZero() {
		t.Fatal("an expired code is still reported")
	}
	f.m.mu.Lock()
	f.m.pairExpires = time.Now().Add(time.Hour)
	f.m.mu.Unlock()
	f.m.Stop()
	if !f.m.PairingExpires().IsZero() {
		t.Fatal("the code outlived its engine")
	}
}

// The app ends a core that has not exited 10 s after asking, so closing must not take the
// engine's whole grace period.
func TestClosingTheManagerTakesTheEngineDownWithinAFewSeconds(t *testing.T) {
	f := newFake(t, "tree", "10s")
	if _, _, err := f.m.Endpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	ports := f.waitStarts(2)
	start := time.Now()
	f.m.Close()
	if took := time.Since(start); took > closeGrace+2*time.Second {
		t.Fatalf("Close took %v", took)
	}
	for _, p := range ports {
		f.gone(p)
	}
}
