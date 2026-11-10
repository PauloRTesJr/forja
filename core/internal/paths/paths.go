// Package paths resolves per-OS application directories. Every path is overridable by config.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

const app = "forja"

// DefaultConfigDir is where the config folder is when nothing moves it.
func DefaultConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	return filepath.Join(base, app)
}

// ConfigDir holds config.toml and user presets.
func ConfigDir() string {
	if d := os.Getenv("FORJA_CONFIG_DIR"); d != "" {
		return d
	}
	return DefaultConfigDir()
}

// DataDir holds the library database and the object store.
func DataDir() string {
	if d := os.Getenv("FORJA_DATA_DIR"); d != "" {
		return d
	}
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, app)
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", app)
		}
	default:
		if d := os.Getenv("XDG_DATA_HOME"); d != "" {
			return filepath.Join(d, app)
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", app)
		}
	}
	return filepath.Join(".", "."+app)
}

// SystemConfigFile is the managed-install config layer.
func SystemConfigFile() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("PROGRAMDATA"), app, "config.toml")
	}
	return filepath.Join("/etc", app, "config.toml")
}
