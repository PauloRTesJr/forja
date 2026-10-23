// Package secrets stores API keys and tokens in the OS keychain. Config refers to them by name.
//
// Values never leave the core: there is no API that returns one, the UI sees a masked hint,
// and the core's log redacts every value this process has read or stored.
package secrets

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
)

const service = "forja"

var ErrNotFound = errors.New("secret not found")

// Name is what a secret may be called: short, lowercase, no spaces.
var Name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Get checks FORJA_SECRET_<NAME> first (headless servers, CI), then the OS keychain.
func Get(name string) (string, error) {
	if v := os.Getenv(envName(name)); v != "" {
		remember(v)
		return v, nil
	}
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err == nil {
		remember(v)
	}
	return v, err
}

func Set(name, value string) error {
	remember(value)
	return keyring.Set(service, name, value)
}

func Delete(name string) error {
	err := keyring.Delete(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Hint shows enough of a stored secret to recognize it: its last four characters.
func Hint(name string) (string, bool) {
	v, err := Get(name)
	if err != nil || v == "" {
		return "", false
	}
	if len(v) <= 8 {
		return "••••", true
	}
	return "…" + v[len(v)-4:], true
}

// Store names where secrets live on this OS, for the UI.
func Store() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows Credential Manager"
	case "darwin":
		return "macOS Keychain"
	default:
		return "the system keyring"
	}
}

func envName(name string) string {
	return "FORJA_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}

var (
	mu    sync.RWMutex
	known = map[string]bool{}
)

// remember records a value so logs redact it; values too short to be secrets are skipped.
func remember(v string) {
	if len(v) < 8 {
		return
	}
	mu.Lock()
	known[v] = true
	mu.Unlock()
}

// Redact replaces every secret value this process has seen.
func Redact(s string) string {
	mu.RLock()
	defer mu.RUnlock()
	for v := range known {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}

// Redacting wraps a log handler so no record carries a secret value.
func Redacting(h slog.Handler) slog.Handler { return redacting{h} }

type redacting struct{ slog.Handler }

func (r redacting) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, Redact(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return r.Handler.Handle(ctx, out)
}

func redactAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindGroup:
		attrs := a.Value.Group()
		out := make([]any, len(attrs))
		for i, g := range attrs {
			out[i] = redactAttr(g)
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, Redact(err.Error()))
		}
	}
	return a
}

func (r redacting) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return redacting{r.Handler.WithAttrs(out)}
}

func (r redacting) WithGroup(name string) slog.Handler { return redacting{r.Handler.WithGroup(name)} }

// InMemory keeps secrets in this process instead of the keychain, for tests.
func InMemory() { keyring.MockInit() }
