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
	"time"

	"forja/internal/config"
)

const listeningPrefix = "FORJA_ENGINE_LISTENING "

type Manager struct {
	// load reads the configuration each time the engine starts, so settings apply on restart.
	load     func() (*config.Loaded, error)
	storeDir string
	logPath  string
	log      *slog.Logger
	// Started runs in its own goroutine each time the engine comes up; set before first use.
	Started func()

	mu       sync.Mutex
	stopping bool
	cmd      *exec.Cmd
	group    *procGroup
	url      string
	token    string
	share    Share
	exit     chan struct{}
}

func New(load func() (*config.Loaded, error), storeDir, logDir string, log *slog.Logger) *Manager {
	return &Manager{load: load, storeDir: storeDir, logPath: filepath.Join(logDir, "engine.log"), log: log}
}

// Endpoint returns the running engine's URL and token, starting it if needed.
func (m *Manager) Endpoint(ctx context.Context) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running() {
		return m.url, m.token, nil
	}
	m.stopping = false
	if err := m.start(ctx); err != nil {
		return "", "", err
	}
	return m.url, m.token, nil
}

func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running()
}

func (m *Manager) running() bool {
	if m.cmd == nil || m.exit == nil {
		return false
	}
	select {
	case <-m.exit:
		return false
	default:
		return true
	}
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

func (m *Manager) start(ctx context.Context) error {
	cfg, err := m.load()
	if err != nil {
		return err
	}
	base, err := Command(cfg)
	if err != nil {
		return err
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
		args = append(args, "--manifests", d)
	}
	sharing := Share{}
	if cfg.Engine.Share {
		if share, tok, err := m.prepareShare(cfg.Engine.SharePort); err != nil {
			m.log.Warn("not sharing this GPU", "err", err)
		} else {
			token, sharing = tok, share
			args = append(args, "--share-host", "0.0.0.0", "--share-port", strconv.FormatInt(share.Port, 10),
				"--tls-cert", share.cert, "--tls-key", share.key)
		}
	}
	cmd := exec.Command(base[0], args...)
	cmd.Env = append(os.Environ(), "FORJA_ENGINE_TOKEN="+token, "PYTHONUNBUFFERED=1")
	if err := os.MkdirAll(filepath.Dir(m.logPath), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(m.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	cmd.Stderr = logFile
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("starting engine: %w", err)
	}
	group, err := attach(cmd)
	if group == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		logFile.Close()
		return fmt.Errorf("starting engine: %w", err)
	}
	if err != nil {
		m.log.Warn(err.Error())
	}
	m.log.Info("engine starting", "pid", cmd.Process.Pid, "cmd", strings.Join(base, " "))

	portCh := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if p, ok := strings.CutPrefix(line, listeningPrefix); ok {
				if port, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
					portCh <- port
				}
				continue
			}
			fmt.Fprintln(logFile, line)
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	exit := make(chan struct{})
	go func() {
		err := cmd.Wait()
		group.close()
		logFile.Close()
		if m.stopping {
			m.log.Info("engine stopped")
		} else {
			m.log.Warn("engine exited unexpectedly; it restarts on the next local job", "err", err, "log", m.logPath)
		}
		close(exit)
	}()

	timeout, _ := time.ParseDuration(cfg.Engine.StartTimeout)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	select {
	case port := <-portCh:
		m.cmd, m.group, m.exit, m.token, m.share = cmd, group, exit, token, sharing
		m.url = "http://127.0.0.1:" + strconv.Itoa(port)
		m.log.Info("engine ready", "url", m.url)
		if m.Started != nil {
			go m.Started()
		}
		return nil
	case <-exit:
		return fmt.Errorf("engine exited during startup; see %s", m.logPath)
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return fmt.Errorf("engine did not start within %s; see %s", timeout, m.logPath)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}

// Stop asks the engine to exit, then kills it after a grace period.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running() {
		return
	}
	m.stopping = true
	m.group.interrupt()
	select {
	case <-m.exit:
	case <-time.After(10 * time.Second):
		_ = m.cmd.Process.Kill()
		<-m.exit
	}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
