// Package export writes finished assets into a project folder (a Unity project's Assets/).
package export

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var unsafeChars = regexp.MustCompile(`[^a-z0-9]+`)

// Slug turns a file or display name into a folder- and Unity-safe asset name.
func Slug(name string) string {
	name = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	s := strings.Trim(unsafeChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "asset"
	}
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// Sidecar travels next to the GLB so provenance survives copies and commits.
type Sidecar struct {
	Generator string         `json:"generator"`
	JobID     string         `json:"job_id"`
	AssetID   string         `json:"asset_id"`
	Preset    string         `json:"preset"`
	Input     string         `json:"input"`
	Stages    []SidecarStage `json:"stages"`
	CreatedAt time.Time      `json:"created_at"`
}

type SidecarStage struct {
	ID       string         `json:"id"`
	Use      string         `json:"use"`
	Model    string         `json:"model,omitempty"`
	License  string         `json:"license,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Provider string         `json:"provider,omitempty"`
}

// ToProject copies glbPath to <root>/<exportDir>/<name>/<name>.glb plus <name>.forja.json.
// An existing asset folder of the same name is replaced atomically per file.
func ToProject(root, exportDir, name, glbPath string, sidecar Sidecar) (string, error) {
	dir := filepath.Join(root, filepath.FromSlash(exportDir), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, name+".glb")
	if err := copyAtomic(glbPath, dst); err != nil {
		return "", err
	}
	raw, _ := json.MarshalIndent(sidecar, "", "  ")
	side := filepath.Join(dir, name+".forja.json")
	if err := writeAtomic(side, raw); err != nil {
		return "", err
	}
	return dst, nil
}

func copyAtomic(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".forja-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replace(tmp.Name(), dst)
}

func writeAtomic(dst string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".forja-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replace(tmp.Name(), dst)
}

func replace(tmp, dst string) error {
	// CreateTemp makes 0600 files; exported assets are ordinary project files.
	_ = os.Chmod(tmp, 0o644)
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	return nil
}
