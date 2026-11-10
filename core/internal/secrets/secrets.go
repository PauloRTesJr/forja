// Package secrets stores API keys and tokens in the OS keychain. Config refers to them by name.
//
// Values never leave the core: there is no API that returns one, the UI sees a masked hint,
// and the core's log redacts every value this process has read or stored.
package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"

	"forja/internal/paths"
)

const baseService = "forja"

// service is the keychain entry group of this config folder. The default folder keeps "forja", so
// the keys of an ordinary install stay where they were; a folder moved by FORJA_CONFIG_DIR gets
// its own, so two data folders on one account do not read and overwrite each other's keys.
func service() string {
	dir := paths.ConfigDir()
	def := paths.DefaultConfigDir()
	if a, err := filepath.Abs(dir); err == nil {
		dir = a
	}
	if a, err := filepath.Abs(def); err == nil {
		def = a
	}
	dir, def = filepath.Clean(dir), filepath.Clean(def)
	if dir == def || runtime.GOOS == "windows" && strings.EqualFold(dir, def) {
		return baseService
	}
	key := dir
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return baseService + "-" + hex.EncodeToString(sum[:4])
}

var ErrNotFound = errors.New("secret not found")

// Name is what a secret may be called: short, lowercase, no spaces.
var Name = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Get checks FORJA_SECRET_<NAME> first (headless servers, CI), then the OS keychain. A config
// folder with a group of its own falls back to the legacy "forja" group, which every build wrote
// to before groups existed, and copies what it finds there into its own; the legacy entry stays,
// since the default folder's install may use it. A secret deleted here is not fetched back.
func Get(name string) (string, error) {
	if v := os.Getenv(envName(name)); v != "" {
		remember(v)
		return v, nil
	}
	svc := service()
	v, err := keyring.Get(svc, name)
	if errors.Is(err, keyring.ErrNotFound) && svc != baseService {
		if _, gone := keyring.Get(svc, tombstone(name)); gone == nil {
			return "", ErrNotFound
		}
		if v, err = keyring.Get(baseService, name); err == nil {
			_ = keyring.Set(svc, name, v)
		}
	}
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	if err == nil {
		remember(v)
	}
	return v, err
}

// tombstone is the entry that records a deletion in a group that falls back to the legacy one.
func tombstone(name string) string { return "~deleted:" + name }

func Set(name, value string) error {
	remember(value)
	svc := service()
	if err := keyring.Set(svc, name, value); err != nil {
		return err
	}
	if svc != baseService {
		_ = keyring.Delete(svc, tombstone(name))
	}
	return nil
}

func Delete(name string) error {
	svc := service()
	err := keyring.Delete(svc, name)
	if svc != baseService {
		// The legacy entry is not ours to remove; remember not to fall back to it.
		if terr := keyring.Set(svc, tombstone(name), "1"); terr != nil && (err == nil || errors.Is(err, keyring.ErrNotFound)) {
			err = terr
		}
	}
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

// EnvVar returns the environment variable that supplies the secret, when it is set: such a
// secret cannot be replaced from the app.
func EnvVar(name string) (string, bool) {
	v := envName(name)
	return v, os.Getenv(v) != ""
}
