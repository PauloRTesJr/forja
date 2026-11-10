package localengine

import (
	"context"
	"errors"
	"time"
)

// DefaultApplyDelay is how long Apply waits for further settings changes before it restarts
// the engine, so typing a number into a field is one restart, not one per keystroke.
const DefaultApplyDelay = time.Second

const exitedWhileStarting = "The engine stopped while starting. Its log says why."

// startError is why the engine is not up: Plain is a sentence for the person using Forja,
// Error the raw text for the log and "show details".
type startError struct {
	plain string
	err   error
}

func (e *startError) Error() string { return e.err.Error() }
func (e *startError) Unwrap() error { return e.err }

func explained(plain string, err error) *startError { return &startError{plain: plain, err: err} }

func asStartError(err error) *startError {
	var se *startError
	if errors.As(err, &se) {
		return se
	}
	return explained("The engine could not start. Its log says why; try again.", err)
}

// State is what the core can tell about the local engine without asking it anything.
type State struct {
	// State is "stopped" (not running; it starts when something needs it), "starting",
	// "running" or "failed" (the last start failed, or the engine died on its own).
	State string
	// Error is a sentence about a failure and Detail the raw text behind it; both are empty
	// unless State is "failed".
	Error, Detail string
	// RestartPending: settings changed that the running engine has not read yet.
	RestartPending bool
}

// State reports the engine's state. It never waits, not even for a start under way.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := State{State: "stopped", RestartPending: m.restartPending && (m.proc != nil && m.proc.alive() || m.pending != nil)}
	switch {
	case m.proc != nil && m.proc.alive():
		s.State = "running"
	case m.pending != nil:
		s.State = "starting"
	case m.failure != nil:
		s.State, s.Error, s.Detail = "failed", m.failure.plain, m.failure.Error()
	}
	return s
}

func (m *Manager) changed() {
	if m.Changed != nil {
		go m.Changed()
	}
}

// Apply is told that engine settings changed. It returns "saved" when no engine runs (the
// next start reads them), "restarted" when an idle engine restarts now with them, or "pending"
// when something uses it and it restarts once that ends. Changes within ApplyDelay of each
// other share one restart.
func (m *Manager) Apply() string {
	m.mu.Lock()
	if m.closed || (m.proc == nil || !m.proc.alive()) && m.pending == nil {
		m.mu.Unlock()
		return "saved"
	}
	m.restartPending = true
	delay := m.ApplyDelay
	if delay <= 0 {
		delay = DefaultApplyDelay
	}
	if m.settle != nil {
		m.settle.Stop()
	}
	m.settle = time.AfterFunc(delay, m.Settle)
	m.mu.Unlock()
	m.changed()
	if m.idle() {
		return "restarted"
	}
	return "pending"
}

// idle says whether nothing uses the engine right now.
func (m *Manager) idle() bool {
	if m.WhenIdle == nil {
		return true
	}
	return m.WhenIdle(func() {})
}

// Settle restarts the engine if settings changed and nothing uses it. The core calls it when
// the last job or install ends; Apply's timer calls it too.
func (m *Manager) Settle() {
	m.mu.Lock()
	due := m.restartPending && !m.closed
	m.mu.Unlock()
	if !due {
		return
	}
	restart := func() {
		m.mu.Lock()
		m.restartPending = false
		m.mu.Unlock()
		m.Stop()
		m.changed()
		// A shared GPU must answer even when nobody here is generating.
		if cfg, err := m.load(); err == nil && cfg.Engine.Share {
			go func() { _, _, _ = m.Endpoint(context.Background()) }()
		}
	}
	if m.WhenIdle == nil {
		restart()
		return
	}
	m.WhenIdle(restart)
}

// shareProblem says why sharing could not be set up, for the person who switched it on.
func shareProblem(err error) string {
	if errors.Is(err, errKeychain) {
		return "This computer's GPU is not shared: Forja could not use the system keychain to keep the access token. Unlock the keychain and switch sharing off and on again."
	}
	return "This computer's GPU is not shared: its secure certificate could not be made. Switch sharing off and on again; the engine log says more."
}
