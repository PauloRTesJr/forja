package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/export"
	"forja/internal/jobs"
	"forja/internal/paths"
	"forja/internal/plain"
)

// EvSettingsChanged tells clients to re-read settings and anything that depends on them.
const EvSettingsChanged = "settings.changed"

// settingKinds are the settings the app edits, with the type each holds. Only settings that
// something reads are here; the rest of config.toml stays for hand editing.
var settingKinds = map[string]string{
	"engine.idle_unload_minutes":   "int",
	"routing.budget_usd.per_job":   "float",
	"routing.budget_usd.per_day":   "float",
	"routing.budget_usd.per_month": "float",
	"engine.vram_reserve_gb":       "float",
	"engine.fit_mode":              "string",
	"routing.policy":               "string",
	"routing.allow_paid_fallback":  "bool",
	"project.export_dir":           "string",
	"project.default_preset":       "string",
	"models.stubs":                 "bool",
	"blender.path":                 "string",
}

type SettingView struct {
	Key     string `json:"key"`
	Value   any    `json:"value"`
	Default any    `json:"default"`
	// Source is the layer that set the value: default, system, user, project, env or flag.
	Source string `json:"source"`
	// SourceDetail names where it comes from: the environment variable, or the file.
	SourceDetail string `json:"source_detail,omitempty"`
	// Min and Max bound a number setting; Max is absent when nothing bounds it.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
}

type SettingsView struct {
	Settings []SettingView     `json:"settings"`
	Files    map[string]string `json:"files"`
	DataDir  string            `json:"data_dir"`
	// Engine says what became of engine settings: "saved" (no engine runs; it reads them when it
	// starts), "restarted" (an idle engine restarts with them now) or "pending" (a job or an
	// install uses it; it restarts when that ends). GET says "pending" while that restart waits.
	Engine string `json:"engine,omitempty"`
}

// lookup walks a dotted path through decoded JSON or TOML.
func lookup(tree map[string]any, key string) any {
	var cur any = tree
	for _, p := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	return cur
}

// sourceDetail is the real place a setting's value comes from: the environment variable that
// sets it, or the file that does.
func sourceDetail(source, key, project string) string {
	switch source {
	case "env":
		return "FORJA_" + strings.ToUpper(strings.ReplaceAll(key, ".", "__"))
	case "user":
		return config.UserFile()
	case "system":
		return paths.SystemConfigFile()
	case "project":
		return filepath.Join(project, "forja.toml")
	}
	return ""
}

func (s *Server) settingsFor(project string) (*SettingsView, error) {
	cfg, err := s.Runner.Config(project)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(cfg.Config)
	effective := map[string]any{}
	_ = json.Unmarshal(raw, &effective)
	view := &SettingsView{Files: map[string]string{"user": config.UserFile()}, DataDir: s.DataDir}
	if s.Engine != nil && s.Engine.State().RestartPending {
		view.Engine = "pending"
	}
	if project != "" {
		view.Files["project"] = filepath.Join(project, "forja.toml")
	}
	for key := range settingKinds {
		src := cfg.Sources[key]
		if src == "" {
			src = "default"
		}
		sv := SettingView{Key: key, Value: lookup(effective, key), Default: lookup(config.Defaults(), key), Source: src}
		sv.SourceDetail = sourceDetail(src, key, project)
		if _, lo, hi, ok := s.limits(key); ok {
			sv.Min, sv.Max = &lo, &hi
		}
		view.Settings = append(view.Settings, sv)
	}
	sort.Slice(view.Settings, func(i, j int) bool { return view.Settings[i].Key < view.Settings[j].Key })
	return view, nil
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	view, err := s.settingsFor(r.URL.Query().Get("project"))
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	writeJSON(w, 200, view)
}

// coerce turns a JSON value into the setting's type; nil means "remove, use the default".
func (s *Server) coerce(key string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch settingKinds[key] {
	case "int":
		f, err := s.number(key, v, true)
		if err != nil {
			return nil, err
		}
		return int64(f), nil
	case "float":
		return s.number(key, v, false)
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s: want true or false", key)
		}
		return b, nil
	case "string":
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s: want text", key)
		}
		return strings.TrimSpace(str), nil
	}
	return nil, fmt.Errorf("%s is not a setting the app edits; change it in config.toml", key)
}

// settingsPatch writes settings to the user's config.toml, or to a project's forja.toml.
func (s *Server) settingsPatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project string         `json:"project"`
		Set     map[string]any `json:"set"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	changes := map[string]any{}
	engine := false
	for k, v := range body.Set {
		val, err := s.coerce(k, v)
		if err != nil {
			problem(w, 422, plain.Err(err))
			return
		}
		changes[k] = val
		// The local engine reads blender.path when it starts, for the stages Blender does.
		engine = engine || strings.HasPrefix(k, "engine.") || k == "blender.path"
	}
	if dir, ok := changes["project.export_dir"].(string); ok {
		if dir == "" || filepath.IsAbs(dir) || slices.Contains(strings.Split(filepath.ToSlash(dir), "/"), "..") {
			problem(w, 422, "the export folder must be a folder inside the project, such as Assets/Generated")
			return
		}
	}
	if path, ok := changes["blender.path"].(string); ok && path == "" {
		changes["blender.path"] = nil // empty means find it automatically
	} else if ok {
		if err := export.CheckBlenderPath(path); err != nil {
			problem(w, 422, plain.Err(err))
			return
		}
		if _, err := export.FindBlender(path); err != nil {
			problem(w, 422, plain.Err(err))
			return
		}
	}
	if id, ok := changes["project.default_preset"].(string); ok {
		all, err := s.Runner.Presets(body.Project)
		if err != nil {
			problem(w, 422, plain.Err(err))
			return
		}
		if _, ok := all[id]; !ok {
			problem(w, 422, "no preset "+id)
			return
		}
	}
	path := config.UserFile()
	if body.Project != "" {
		path = filepath.Join(body.Project, "forja.toml")
	}
	err := config.Edit(path, changes, func() error {
		_, err := config.Load(body.Project, nil)
		return err
	})
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	view, err := s.settingsFor(body.Project)
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	if engine && body.Project == "" && s.Engine != nil {
		view.Engine = s.Engine.Apply()
	}
	s.Hub.Publish(EvSettingsChanged, map[string]string{"project": body.Project})
	writeJSON(w, 200, view)
}

// settingsFile makes sure the user's config.toml exists and returns its path, for opening
// it in an editor.
func (s *Server) settingsFile(w http.ResponseWriter, _ *http.Request) {
	path := config.UserFile()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		header := "# Forja settings. The app edits the keys it shows and keeps everything else,\n" +
			"# comments included. Every key is described in docs/configuration.md.\n"
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			problem(w, 500, plain.Err(err))
			return
		}
		if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
			problem(w, 500, plain.Err(err))
			return
		}
	}
	writeJSON(w, 200, map[string]string{"path": path, "data_dir": s.DataDir})
}

// diagnostics is a plain-text report for bug reports: versions, hardware, settings, models,
// providers and the end of the engine log. The home folder is replaced by ~ and nothing in
// it is a secret: config holds secret names, never values.
func (s *Server) diagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	fmt.Fprintf(&b, "Forja %s · %s/%s · %s\n\n", s.Version, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339))
	section := func(title string, v any) {
		raw, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintf(&b, "## %s\n%s\n\n", title, raw)
	}
	if e, ok := s.Catalog.Last(jobs.LocalProvider); ok {
		section("This computer", e.System)
		type model struct{ ID, Status, Fit string }
		var ms []model
		for _, m := range e.Models {
			ms = append(ms, model{m.ID, m.Status, m.Fit})
		}
		section("Models", ms)
	}
	if view, err := s.settingsFor(""); err == nil {
		section("Settings", view.Settings)
	}
	if cfg, err := s.Runner.Config(""); err == nil {
		section("Config files", cfg.Files)
		type provider struct {
			ID, Kind, Vendor, Label string
			Paid                    bool
		}
		var ps []provider
		for _, p := range cfg.Providers {
			ps = append(ps, provider{p.ID, p.Kind, p.Vendor, p.Label, p.Paid})
		}
		section("Providers (addresses and endpoints left out)", ps)
	}
	var rows []map[string]any
	for _, p := range s.Providers.All() {
		row := map[string]any{"id": p.ID(), "kind": p.Kind()}
		if p.ID() != jobs.LocalProvider || s.Engine == nil || s.Engine.Running() {
			hctx, hcancel := context.WithTimeout(ctx, 3*time.Second)
			if err := p.Health(hctx); err != nil {
				row["error"] = err.Error()
			} else {
				row["healthy"] = true
			}
			hcancel()
		}
		rows = append(rows, row)
	}
	section("Provider health", rows)
	b.WriteString("## Engine log (last 80 lines)\n")
	b.WriteString(tail(filepath.Join(s.DataDir, "logs", "engine.log"), 80))
	out := b.String()
	out = maskPrivate(out)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, out)
}

var (
	address  = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b|https?://[^\s"')]+`)
	loopback = "127.0.0.1"
)

// maskPrivate hides what a bug report must not carry: the home folder (any case on Windows, where
// paths are case-insensitive), and the addresses of other computers.
func maskPrivate(out string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, form := range []string{home, filepath.ToSlash(home), strings.ReplaceAll(home, `\`, `\\`)} {
			if runtime.GOOS == "windows" {
				out = regexp.MustCompile("(?i)"+regexp.QuoteMeta(form)).ReplaceAllLiteralString(out, "~")
			} else {
				out = strings.ReplaceAll(out, form, "~")
			}
		}
	}
	return address.ReplaceAllStringFunc(out, func(a string) string {
		if strings.Contains(a, loopback) || strings.Contains(a, "localhost") {
			return a
		}
		return "<address>"
	})
}

func tail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no engine log yet)\n"
	}
	defer f.Close()
	lines := make([]string, 0, n)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if len(lines) == n {
			lines = lines[1:]
		}
		lines = append(lines, sc.Text())
	}
	return strings.Join(lines, "\n") + "\n"
}
