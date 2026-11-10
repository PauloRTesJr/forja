// Package export writes finished assets into a project folder (a Unity project's Assets/),
// and to files in other formats, which Blender converts.
package export

import (
	"context"
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

// Item is one asset to export: its id, the name to give its files, and its GLB.
type Item struct {
	Asset string
	Name  string
	GLB   string
}

// Exported is what one asset became: the files written, or why it could not be exported.
type Exported struct {
	Asset string   `json:"asset"`
	Files []string `json:"files,omitempty"`
	Error string   `json:"error,omitempty"`
}

// Options say where an export's files go when the plain rule does not decide it.
type Options struct {
	// File makes dest the file to write, for one item: the format's extension is added when
	// dest lacks it, instead of dest becoming a folder of that name.
	File bool
	// Replace overwrites files of the same name in the folder written into. Without it, an
	// item whose name is taken there is written beside it as "name (2)". A file named by dest
	// is always replaced: whoever named it chose it.
	Replace bool
}

// ToFiles exports items in format f. dest is the file to write when there is one item and
// it ends in the format's extension (or opts.File says so); otherwise it is the folder to
// write into, each asset named after itself. Every format but GLB needs b. Blender writes
// into a hidden folder beside the files first, so a failed export leaves no half-written
// file behind.
func ToFiles(ctx context.Context, b *Blender, f Format, items []Item, dest string, opts Options) ([]Exported, error) {
	if f.Blender && b == nil {
		return nil, ErrNoBlender
	}
	if opts.File {
		if len(items) != 1 {
			return nil, fmt.Errorf("%s names one file, but %d assets were given: name a folder", dest, len(items))
		}
		if !strings.EqualFold(filepath.Ext(dest), f.Ext) {
			dest += f.Ext
		}
	}
	dir, named := dest, len(items) == 1 && strings.EqualFold(filepath.Ext(dest), f.Ext)
	if named {
		dir = filepath.Dir(dest)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := uniqueStems(items, dir, f.Ext, opts.Replace || named)
	for i := range files {
		files[i] += f.Ext
	}
	if named {
		files[0] = filepath.Base(dest)
	}
	out := make([]Exported, len(items))
	if !f.Blender {
		for i, it := range items {
			out[i].Asset = it.Asset
			path := filepath.Join(dir, files[i])
			if err := copyAtomic(it.GLB, path); err != nil {
				out[i].Error = err.Error()
			} else {
				out[i].Files = []string{path}
			}
		}
		return out, nil
	}
	staging, err := os.MkdirTemp(dir, ".forja-export-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	jobs := make([]jobItem, len(items))
	for i, it := range items {
		jobs[i] = jobItem{GLB: it.GLB, Out: filepath.Join(staging, files[i])}
	}
	done, err := b.convert(ctx, f.ID, jobs, filepath.Join(staging, "job.json"))
	if err != nil {
		return nil, err
	}
	for i, c := range done {
		out[i].Asset = items[i].Asset
		if c.Error != "" {
			out[i].Error = c.Error
			continue
		}
		for _, written := range c.Files {
			// Only names come from Blender; the folders are ours.
			name := filepath.Base(filepath.FromSlash(written))
			path := filepath.Join(dir, name)
			if err := replace(filepath.Join(staging, name), path); err != nil {
				out[i].Error = err.Error()
				break
			}
			out[i].Files = append(out[i].Files, path)
		}
	}
	return out, nil
}

// uniqueStems names each item's files after it, numbering names that repeat in one export
// and, unless replace is set, names whose file in ext is already in dir.
func uniqueStems(items []Item, dir, ext string, replace bool) []string {
	// Case-insensitive, as Windows and macOS compare file names.
	taken := map[string]bool{}
	if entries, err := os.ReadDir(dir); err == nil && !replace {
		for _, e := range entries {
			if name := e.Name(); strings.EqualFold(filepath.Ext(name), ext) {
				taken[strings.ToLower(name[:len(name)-len(ext)])] = true
			}
		}
	}
	out := make([]string, len(items))
	for i, it := range items {
		base := FileName(it.Name)
		stem := base
		for n := 2; taken[strings.ToLower(stem)]; n++ {
			stem = fmt.Sprintf("%s (%d)", base, n)
		}
		taken[strings.ToLower(stem)] = true
		out[i] = stem
	}
	return out
}

// FileName makes a name safe for a file on every OS.
func FileName(name string) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '-'
		}
		return r
	}, strings.TrimSpace(name))
	if name = strings.Trim(name, ". "); name == "" {
		return "asset"
	}
	return name
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

// imageExts are the extensions a name loses when it comes from an image file.
var imageExts = []string{".png", ".jpg", ".jpeg", ".webp", ".bmp", ".gif", ".tif", ".tiff", ".tga", ".psd", ".exr"}

// DisplayName is the name a job and its asset show: the file name the image came with, as
// typed, without its folder and image extension. Only where a file is written does a name
// become file-safe (FileName, Slug).
func DisplayName(name string) string {
	name = strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	for _, ext := range imageExts {
		if len(name) > len(ext) && strings.EqualFold(name[len(name)-len(ext):], ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, name))
	if r := []rune(name); len(r) > 120 {
		name = strings.TrimSpace(string(r[:120]))
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "asset"
	}
	return name
}
