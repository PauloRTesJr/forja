package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"forja/internal/api"
	"forja/internal/config"
	"forja/internal/ipc"
	"forja/internal/secrets"
)

var pipes atomic.Int64

func address(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\forja-daemon-test-%d-%d`, os.Getpid(), pipes.Add(1))
	}
	// Unix sockets have a short path limit, which t.TempDir can exceed on macOS.
	dir, err := os.MkdirTemp("", "fj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "core.sock")
}

// world gives the test its own data and config folders, and an engine that cannot start.
func world(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", filepath.Join(dir, "config"))
	t.Setenv("FORJA_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("FORJA_SOCKET", "")
	secrets.InMemory()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.UserFile(), []byte("[engine]\ncommand = [\"forja-test-no-such-engine\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Options{Version: "test", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func started(t *testing.T, opts Options) *Core {
	t.Helper()
	opts.Address = address(t)
	c, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

// The app keeps an event stream open for as long as it runs. Stopping waited five seconds
// for it to end, holding the data folder's lock meanwhile.
func TestStoppingDoesNotWaitForOpenEventStreams(t *testing.T) {
	c := started(t, world(t))
	resp, err := ipc.HTTPClient(c.Address).Get(ipc.BaseURL + "/v1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Reading, as the app does: the stream's last bytes go out only to a client that takes them.
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		ended <- err
	}()
	began := time.Now()
	c.Stop()
	if took := time.Since(began); took > 2*time.Second {
		t.Fatalf("stopping took %s with an event stream open", took.Round(time.Millisecond))
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the event stream outlived the core")
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Done is not closed after Stop")
	}
}

// Closing the app and opening it again at once: the new core found the old one still
// letting go of the data folder, and gave up with "another core is running".
func TestACoreStartsOnceTheOneBeforeItHasStopped(t *testing.T) {
	opts := world(t)
	first := started(t, opts)
	go func() {
		time.Sleep(300 * time.Millisecond)
		first.Stop()
	}()
	second := started(t, opts)
	if !Reachable(second.Address) {
		t.Fatal("the second core does not answer")
	}
}

func TestASecondCoreIsRefusedWhileOneRuns(t *testing.T) {
	old := lockWait
	lockWait = 200 * time.Millisecond
	t.Cleanup(func() { lockWait = old })
	opts := world(t)
	started(t, opts)
	opts.Address = address(t)
	c, err := Start(opts)
	if !errors.Is(err, ErrAlreadyRunning) {
		if c != nil {
			c.Stop()
		}
		t.Fatalf("got %v, want ErrAlreadyRunning", err)
	}
}

// The app shows the engine's state from /v1/info, and asks again only when told.
func TestEveryChangeOfTheEnginesStateIsPublished(t *testing.T) {
	c := started(t, world(t))
	evs, stop := c.hub.Subscribe()
	defer stop()
	if _, _, err := c.engine.Endpoint(context.Background()); err == nil {
		t.Fatal("an engine that does not exist started")
	}
	got := 0
	for deadline := time.After(5 * time.Second); got < 2; {
		select {
		case ev := <-evs:
			if ev.Type == api.EvProvidersChanged {
				got++
			}
		case <-deadline:
			t.Fatalf("heard of %d changes (starting, failed), want 2", got)
		}
	}
	if st := c.engine.State(); st.State != "failed" || st.Error == "" {
		t.Fatalf("%+v", st)
	}
}

// The core logged only to stderr, which nobody keeps once the app that started it is closed.
func TestTheCoreKeepsItsLogInTheDataFolder(t *testing.T) {
	opts := world(t)
	c := started(t, opts)
	path := filepath.Join(os.Getenv("FORJA_DATA_DIR"), "logs", "core.log")
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "core listening") {
		t.Fatalf("core.log: %v\n%s", err, raw)
	}
	c.Stop()
}

func TestTheLogFileIsRedactedAndRotatedBySize(t *testing.T) {
	secrets.InMemory()
	path := filepath.Join(t.TempDir(), "logs", "core.log")
	f, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := secrets.Set("provider-x", "tok-supersecret-9876"); err != nil {
		t.Fatal(err)
	}
	log := slog.New(secrets.Redacting(tee{slog.NewTextHandler(io.Discard, nil), slog.NewTextHandler(f, nil)}))
	log.Info("calling with tok-supersecret-9876", "token", "tok-supersecret-9876")
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "supersecret") || !strings.Contains(string(raw), "calling with") {
		t.Fatalf("log:\n%s", raw)
	}
	line := strings.Repeat("x", 1<<16)
	for i := 0; i < logLimit/len(line)+2; i++ {
		_, _ = f.Write([]byte(line))
	}
	if st, err := os.Stat(path + ".1"); err != nil || st.Size() == 0 {
		t.Fatalf("no rotated file: %v", err)
	}
	if st, _ := os.Stat(path); st.Size() > logLimit {
		t.Fatalf("the log grew to %d", st.Size())
	}
}
