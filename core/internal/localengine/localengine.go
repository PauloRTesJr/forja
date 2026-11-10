// Package localengine starts and supervises the engine on this machine.
//
// The engine starts lazily on the first local job, listens on loopback with a random port
// and a per-session token, shares the core's object store, and restarts after a crash.
package localengine

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"forja/internal/config"
)

const listeningPrefix = "FORJA_ENGINE_LISTENING "

// ErrClosed answers a caller that needs the engine after the core began to stop.
var ErrClosed = errors.New("Forja is closing, so the engine was not started")

type Manager struct {
	// load reads the configuration each time the engine starts, so settings apply on restart.
	load     func() (*config.Loaded, error)
	storeDir string
	logPath  string
	log      *slog.Logger
	// Started runs in its own goroutine each time the engine comes up; set before first use.
	Started func()
	// Exited runs in its own goroutine each time an engine that was up ends, asked to or not.
	Exited func()
	// Changed runs in its own goroutine whenever what State reports changes.
	Changed func()
	// WhenIdle runs fn only while nothing uses the engine (no job, no install), holding off
	// whatever would start meanwhile, and says whether it ran. Without it the engine counts as idle.
	WhenIdle func(fn func()) bool
	// ApplyDelay is how long a burst of settings changes may go on before the engine restarts
	// once for all of them; zero means DefaultApplyDelay.
	ApplyDelay time.Duration

	// mu guards the fields below. It is never held while an engine starts or stops: asking
	// whether the engine runs must not wait for either.
	mu     sync.Mutex
	closed bool
	// proc is the engine that came up, until it exits; pending is a start under way.
	proc    *process
	pending *launch
	// failure is why the engine is not up, until it starts or is stopped on purpose.
	failure *startError
	// restartPending: settings changed that the running engine has not read; settle is the
	// timer that restarts it.
	restartPending bool
	settle         *time.Timer
	// pairProc is the engine the pairing code on show was given to, and pairExpires its end.
	pairProc    *process
	pairExpires time.Time
}

// process is one engine process.
type process struct {
	group *procGroup
	url   string
	token string
	share Share
	exit  chan struct{}
	// asked is set before the core ends the process, so its exit is not reported as a crash.
	asked atomic.Bool
}

func (p *process) alive() bool {
	select {
	case <-p.exit:
		return false
	default:
		return true
	}
}

// launch is one start of the engine, shared by everyone who needs the engine meanwhile.
type launch struct {
	done   chan struct{}
	cancel context.CancelFunc
	proc   *process
	err    error
}

func New(load func() (*config.Loaded, error), storeDir, logDir string, log *slog.Logger) *Manager {
	return &Manager{load: load, storeDir: storeDir, logPath: filepath.Join(logDir, "engine.log"), log: log}
}

// Endpoint returns the running engine's URL and token, starting it if needed. Callers that
// arrive while it starts wait for that one start and share its outcome. A start belongs to
// the manager, not to whoever asked first: a caller that gives up only stops waiting, and the
// engine keeps starting for the next one.
func (m *Manager) Endpoint(ctx context.Context) (string, string, error) {
	m.mu.Lock()
	if p := m.proc; p != nil && p.alive() {
		m.mu.Unlock()
		return p.url, p.token, nil
	}
	if m.closed {
		m.mu.Unlock()
		return "", "", ErrClosed
	}
	// A caller that already gave up (a request the app dropped) must not start an engine.
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return "", "", err
	}
	l := m.pending
	if l == nil {
		l = m.launch()
	}
	m.mu.Unlock()
	select {
	case <-l.done:
		if l.err != nil {
			return "", "", l.err
		}
		return l.proc.url, l.proc.token, nil
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// launch begins a start; the caller holds mu.
func (m *Manager) launch() *launch {
	ctx, cancel := context.WithCancel(context.Background())
	l := &launch{done: make(chan struct{}), cancel: cancel}
	m.pending = l
	m.failure = nil
	m.changed()
	go func() {
		defer cancel()
		p, err := m.start(ctx)
		m.mu.Lock()
		l.proc, l.err = p, err
		m.pending = nil
		switch {
		case err == nil:
			m.proc = p
		case ctx.Err() == nil:
			m.failure = asStartError(err)
		}
		m.mu.Unlock()
		close(l.done)
		m.changed()
		if err != nil {
			m.log.Warn("engine did not start", "err", err)
			return
		}
		m.log.Info("engine ready", "url", p.url)
		if m.Started != nil {
			go m.Started()
		}
	}()
	return l
}

// Running reports whether an engine is up. It never waits, not even for a start under way.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proc != nil && m.proc.alive()
}

// Starting reports whether the engine is being started now.
func (m *Manager) Starting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending != nil
}

// Command resolves how to launch the engine: config, then forja-engine on PATH, then the
// repository's engine/ folder through uv (development checkouts).
func Command(cfg *config.Loaded) ([]string, error) {
	if len(cfg.Engine.Command) > 0 {
		return cfg.Engine.Command, nil
	}
	if p, err := exec.LookPath("forja-engine"); err == nil {
		return []string{p}, nil
	}
	if dir := findRepoEngine(); dir != "" {
		if uv, err := exec.LookPath("uv"); err == nil {
			return []string{uv, "run", "--quiet", "--project", dir, "forja-engine"}, nil
		}
	}
	return nil, errors.New("cannot find forja-engine: install it, or set [engine] command in config.toml")
}

// Env is what the engine reads from its environment besides its token: the Blender the user set
// (blender.path) for the stages Blender does. Without it the engine looks where the core would.
func Env(cfg *config.Loaded) []string {
	if cfg.Blender.Path != "" {
		return []string{"FORJA_BLENDER=" + cfg.Blender.Path}
	}
	return nil
}

func findRepoEngine() string {
	starts := []string{}
	if exe, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(exe))
	}
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	for _, dir := range starts {
		for i := 0; i < 6; i++ {
			candidate := filepath.Join(dir, "engine", "pyproject.toml")
			if _, err := os.Stat(candidate); err == nil {
				return filepath.Dir(candidate)
			}
			dir = filepath.Dir(dir)
		}
	}
	return ""
}

// start runs the engine and waits until it listens. ctx is the manager's own: it ends when
// Stop or Close calls the start off.
func (m *Manager) start(ctx context.Context) (*process, error) {
	cfg, err := m.load()
	if err != nil {
		return nil, explained("Forja could not read its settings, so the engine did not start. Fix config.toml and try again.", err)
	}
	base, err := Command(cfg)
	if err != nil {
		return nil, explained("Forja could not find the engine program. Reinstall Forja, or set the engine's command in config.toml.", err)
	}
	token := randomToken()
	args := append(append([]string{}, base[1:]...),
		"serve", "--host", "127.0.0.1", "--port", "0", "--store", m.storeDir,
		"--fit-mode", cfg.Engine.FitMode,
		"--vram-reserve-gb", strconv.FormatFloat(cfg.Engine.VRAMReserveGB, 'f', -1, 64),
		"--idle-unload-minutes", strconv.FormatInt(cfg.Engine.IdleUnloadMinutes, 10),
		"--log-level", "warning",
	)
	for _, d := range cfg.Engine.ManifestDirs {
		// Absolute, since the engine runs in the data folder, not where the core was started.
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		args = append(args, "--manifests", d)
	}
	sharing := Share{}
	if cfg.Engine.Share {
		if share, tok, err := m.prepareShare(cfg.Engine.SharePort); err != nil {
			m.log.Warn("not sharing this GPU", "err", err)
			sharing = Share{Problem: shareProblem(err)}
		} else {
			token, sharing = tok, share
			args = append(args, "--share-host", "0.0.0.0", "--share-port", strconv.FormatInt(share.Port, 10),
				"--tls-cert", share.cert, "--tls-key", share.key)
		}
	}
	cmd := exec.Command(base[0], args...)
	// The engine looks programs up by name (Blender, nvidia-smi, uv). Started where a CLI user
	// stands, say an unzipped asset pack, Windows would let it run a blender.exe found there; the
	// data folder and NoDefaultCurrentDirectoryInExePath keep lookups to PATH.
	cmd.Dir = filepath.Dir(m.storeDir)
	cmd.Env = append(os.Environ(), "FORJA_ENGINE_TOKEN="+token, "PYTHONUNBUFFERED=1", "NoDefaultCurrentDirectoryInExePath=1")
	cmd.Env = append(cmd.Env, Env(cfg)...)
	if err := os.MkdirAll(filepath.Dir(m.logPath), 0o755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(m.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	// Where this start's lines begin, to quote the engine's last words if it does not come up.
	logged := int64(0)
	if st, err := logFile.Stat(); err == nil {
		logged = st.Size()
	}
	cmd.Stderr = logFile
	// A pipe of the core's own, not cmd.StdoutPipe: Wait closes that one as soon as the process
	// exits, which can lose what the engine said last, the lines that explain a failed start.
	stdout, stdoutW, err := os.Pipe()
	if err != nil {
		logFile.Close()
		return nil, err
	}
	cmd.Stdout = stdoutW
	configureProcess(cmd)
	err = cmd.Start()
	stdoutW.Close()
	if err != nil {
		stdout.Close()
		logFile.Close()
		return nil, explained("The engine program could not be run. Check that Forja is installed completely.",
			fmt.Errorf("could not run the engine (%s): %w", base[0], err))
	}
	group, err := attach(cmd)
	if group == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		stdout.Close()
		logFile.Close()
		return nil, explained("The engine program could not be run. Check that Forja is installed completely.",
			fmt.Errorf("could not run the engine: %w", err))
	}
	if err != nil {
		m.log.Warn(err.Error())
	}
	m.log.Info("engine starting", "pid", cmd.Process.Pid, "cmd", strings.Join(base, " "))

	p := &process{group: group, token: token, share: sharing, exit: make(chan struct{})}
	portCh := make(chan int, 1)
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		defer stdout.Close()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if port, ok := strings.CutPrefix(line, listeningPrefix); ok {
				if port, err := strconv.Atoi(strings.TrimSpace(port)); err == nil {
					select {
					case portCh <- port:
					default:
					}
				}
				continue
			}
			fmt.Fprintln(logFile, line)
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	var ready atomic.Bool
	go func() {
		err := cmd.Wait()
		group.close()
		// The pipe ends once everything the engine started is gone too. Whatever outlives it
		// (possible where there is no job object) must not hold the exit back.
		drained := time.NewTimer(2 * time.Second)
		select {
		case <-scanned:
		case <-drained.C:
		}
		drained.Stop()
		logFile.Close()
		switch {
		case p.asked.Load():
			m.log.Info("engine stopped")
		case ready.Load():
			m.log.Warn("engine exited unexpectedly; it restarts on the next local job", "err", err, "log", m.logPath)
		default:
			m.log.Warn("engine exited while starting", "err", err, "log", m.logPath)
		}
		close(p.exit)
		if ready.Load() {
			if !p.asked.Load() {
				m.mu.Lock()
				m.failure = explained("The engine stopped unexpectedly. It starts again when a job needs it.", fmt.Errorf("the engine exited unexpectedly: %v", err))
				m.mu.Unlock()
			}
			m.changed()
			if m.Exited != nil {
				go m.Exited()
			}
		}
	}()

	timeout, _ := time.ParseDuration(cfg.Engine.StartTimeout)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case port := <-portCh:
		p.url = "http://127.0.0.1:" + strconv.Itoa(port)
		ready.Store(true)
		return p, nil
	case <-p.exit:
		if said := lastLine(m.logPath, logged); said != "" {
			return nil, explained(exitedWhileStarting, fmt.Errorf("the engine exited while starting: %s (full log: %s)", said, m.logPath))
		}
		return nil, explained(exitedWhileStarting, fmt.Errorf("the engine exited while starting; see %s", m.logPath))
	case <-timer.C:
		end(p)
		return nil, explained("The engine did not start in time. Try again, or raise the engine's start timeout in config.toml.",
			fmt.Errorf("the engine did not start within %s; see %s, or raise engine.start_timeout in config.toml", timeout, m.logPath))
	case <-ctx.Done():
		end(p)
		return nil, errors.New("the engine was stopped while it was starting")
	}
}

// end stops a process and everything it started, at once, and waits until it is gone.
func end(p *process) {
	p.asked.Store(true)
	p.group.kill()
	<-p.exit
}

// lastLine is the last thing the engine wrote to its log from offset on, shortened to fit a
// message; "" when it wrote nothing.
func lastLine(path string, offset int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	raw, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if r := []rune(last); len(r) > 300 {
		last = string(r[:300]) + "…"
	}
	return last
}

// Stop ends the engine, and a start under way; the next caller of Endpoint starts it again.
// It asks first, then kills after a grace period.
func (m *Manager) Stop() { m.stop(stopGrace) }

// stopGrace is how long an engine gets to finish by itself when asked to stop; closeGrace is the
// shorter wait when the core itself is going, which the app waits for before it ends the process.
const (
	stopGrace  = 10 * time.Second
	closeGrace = 3 * time.Second
)

func (m *Manager) stop(grace time.Duration) {
	m.mu.Lock()
	m.failure = nil
	m.mu.Unlock()
	m.mu.Lock()
	l := m.pending
	m.mu.Unlock()
	if l != nil {
		l.cancel()
		<-l.done
	}
	m.mu.Lock()
	p := m.proc
	m.mu.Unlock()
	if p == nil || !p.alive() {
		return
	}
	p.asked.Store(true)
	p.group.interrupt()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.exit:
	case <-timer.C:
		p.group.kill()
		<-p.exit
	}
}

// Close stops the engine for good: Endpoint starts none afterwards. The core calls it when it
// stops, so a job still running then cannot start an engine that would outlive the core.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.stop(closeGrace)
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
