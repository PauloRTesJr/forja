// Package config loads layered TOML configuration.
//
// Precedence, lowest to highest: built-in defaults, system file, user file, project
// forja.toml, FORJA_* environment variables, then per-call overrides (CLI flags, jobs).
// Every leaf remembers which layer set it, for `forja config show --effective`.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"forja/internal/paths"
)

type Config struct {
	General   General                   `toml:"general" json:"general"`
	Engine    Engine                    `toml:"engine" json:"engine"`
	Routing   Routing                   `toml:"routing" json:"routing"`
	Providers []Provider                `toml:"providers" json:"providers"`
	Project   Project                   `toml:"project" json:"project"`
	Models    Models                    `toml:"models" json:"models"`
	Blender   Blender                   `toml:"blender" json:"blender"`
	Presets   map[string]PresetOverride `toml:"presets" json:"presets"`
	Bench     Bench                     `toml:"bench" json:"bench"`
	// Prices is the price table, by entry id: a provider's time, or a hosted model by its id.
	Prices map[string]Price `toml:"prices" json:"prices"`
}

type General struct {
	Language   string `toml:"language" json:"language"`
	DataDir    string `toml:"data_dir" json:"data_dir"`
	ModelsDir  string `toml:"models_dir" json:"models_dir"`
	CacheMaxGB int64  `toml:"cache_max_gb" json:"cache_max_gb"`
}

type Engine struct {
	// Command starts a local engine; empty means "forja-engine" on PATH.
	Command           []string `toml:"command" json:"command"`
	IdleUnloadMinutes int64    `toml:"idle_unload_minutes" json:"idle_unload_minutes"`
	VRAMReserveGB     float64  `toml:"vram_reserve_gb" json:"vram_reserve_gb"`
	FitMode           string   `toml:"fit_mode" json:"fit_mode"`
	FairnessCap       int64    `toml:"fairness_cap" json:"fairness_cap"`
	StartTimeout      string   `toml:"start_timeout" json:"start_timeout"`
	ManifestDirs      []string `toml:"manifest_dirs" json:"manifest_dirs"`
	// Share serves this machine's engine to other computers on the network, over TLS.
	Share     bool  `toml:"share" json:"share"`
	SharePort int64 `toml:"share_port" json:"share_port"`
}

type Routing struct {
	Policy            string            `toml:"policy" json:"policy"`
	AllowPaidFallback bool              `toml:"allow_paid_fallback" json:"allow_paid_fallback"`
	Priority          []string          `toml:"priority" json:"priority"`
	Stages            map[string]string `toml:"stages" json:"stages"`
	Budget            Budget            `toml:"budget_usd" json:"budget_usd"`
}

type Budget struct {
	PerJob   float64 `toml:"per_job" json:"per_job"`
	PerDay   float64 `toml:"per_day" json:"per_day"`
	PerMonth float64 `toml:"per_month" json:"per_month"`
}

type Provider struct {
	ID     string `toml:"id" json:"id"`
	Kind   string `toml:"kind" json:"kind"` // engine | serverless | api | comfyui
	URL    string `toml:"url" json:"url"`
	Secret string `toml:"secret" json:"secret"` // keychain entry name, never the value
	Label  string `toml:"label" json:"label,omitempty"`
	// Fingerprint pins the TLS certificate (SHA-256 of its DER) of an engine that signs its own.
	Fingerprint       string  `toml:"fingerprint" json:"fingerprint,omitempty"`
	Vendor            string  `toml:"vendor" json:"vendor,omitempty"`
	EndpointID        string  `toml:"endpoint_id" json:"endpoint_id,omitempty"`
	PriceUSDPerSecond float64 `toml:"price_usd_per_second" json:"price_usd_per_second,omitempty"`
	Paid              bool    `toml:"paid" json:"paid"`
	// Models are the hub models the user added (endpoint ids), for kind = "api" providers.
	Models []string `toml:"models" json:"models,omitempty"`
	// Price names the price table entry for the provider's time, when price_usd_per_second is unset.
	Price string `toml:"price" json:"price,omitempty"`
	// Volume and Region name a RunPod endpoint's network volume and its datacenter (EU-RO-1),
	// whose S3 API moves artifacts by hash; the volume's S3 key is in the keychain.
	Volume string `toml:"volume" json:"volume,omitempty"`
	Region string `toml:"region" json:"region,omitempty"`
	// Workflows maps stage types to ComfyUI workflow files (API format), relative to the config
	// folder, for kind = "comfyui" providers.
	Workflows map[string]string `toml:"workflows" json:"workflows,omitempty"`
}

type Project struct {
	Name               string `toml:"name" json:"name"`
	ExportDir          string `toml:"export_dir" json:"export_dir"`
	DefaultPreset      string `toml:"default_preset" json:"default_preset"`
	CommercialSafeOnly bool   `toml:"commercial_safe_only" json:"commercial_safe_only"`
	// Root is the folder holding forja.toml; set by the loader, never by a file.
	Root string `toml:"-" json:"root"`
}

// Models picks the model for preset stages that leave it open (docs/decisions.md, decision 12).
type Models struct {
	// Stages maps a stage type to a model id, e.g. shape = "triposg".
	Stages map[string]string `toml:"stages" json:"stages"`
	// Stubs lets the deterministic stub models be picked: tests, CI, UI work without a GPU.
	Stubs bool `toml:"stubs" json:"stubs"`
}

// Blender converts exported assets to formats other than GLB, and opens them for editing.
type Blender struct {
	// Path is Blender's executable; empty means look for it where installers put it.
	Path string `toml:"path" json:"path"`
}

// Bench sets what `forja bench compare` counts as a regression.
type Bench struct {
	// ThresholdPct is the growth in a stage's time or peak VRAM, in percent, that fails.
	ThresholdPct float64 `toml:"threshold_pct" json:"threshold_pct"`
	// Smaller changes are noise whatever their percent: a 20 ms stage that takes 30 ms once.
	MinSeconds float64 `toml:"min_seconds" json:"min_seconds"`
	MinVRAMMB  float64 `toml:"min_vram_mb" json:"min_vram_mb"`
}

// PresetOverride maps "stage.param" to a value, e.g. "lowpoly.target_tris" = 3000.
type PresetOverride struct {
	Overrides map[string]any `toml:"overrides" json:"overrides"`
}

// Loaded is a merged config plus where each value came from.
type Loaded struct {
	Config
	Sources map[string]string `json:"sources"`
	Files   []string          `json:"files"`
}

func (l *Loaded) DataDir() string {
	if l.General.DataDir != "" {
		return l.General.DataDir
	}
	return paths.DataDir()
}

func UserFile() string { return filepath.Join(paths.ConfigDir(), "config.toml") }

// FindProject walks up from dir looking for forja.toml; "" when there is none.
func FindProject(dir string) string {
	dir, _ = filepath.Abs(dir)
	for {
		if _, err := os.Stat(filepath.Join(dir, "forja.toml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// Load merges every layer. projectRoot may be "" (no project); overrides is the top layer.
func Load(projectRoot string, overrides map[string]any) (*Loaded, error) {
	merged := map[string]any{}
	sources := map[string]string{}
	var files []string
	merge(merged, Defaults(), "default", "", sources)

	layers := []struct{ name, path string }{
		{"system", paths.SystemConfigFile()},
		{"user", UserFile()},
	}
	if projectRoot != "" {
		layers = append(layers, struct{ name, path string }{"project", filepath.Join(projectRoot, "forja.toml")})
	}
	for _, layer := range layers {
		m, err := readFile(layer.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files = append(files, layer.path)
		merge(merged, m, layer.name, "", sources)
	}
	merge(merged, envLayer(), "env", "", sources)
	if overrides != nil {
		merge(merged, overrides, "flag", "", sources)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(merged); err != nil {
		return nil, err
	}
	var cfg Config
	if _, err := toml.Decode(buf.String(), &cfg); err != nil {
		return nil, err
	}
	cfg.Project.Root = projectRoot
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Loaded{Config: cfg, Sources: sources, Files: files}, nil
}

func readFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m := map[string]any{}
	if _, err := toml.Decode(string(raw), &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var probe Config
	md, err := toml.Decode(string(raw), &probe)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		if !strings.HasPrefix(k.String(), "presets.") {
			fmt.Fprintf(os.Stderr, "warning: %s: unknown key %s\n", path, k)
		}
	}
	return m, nil
}

// envLayer maps FORJA_SECTION__KEY=value (double underscore between levels) into the tree.
func envLayer() map[string]any {
	out := map[string]any{}
	for _, kv := range os.Environ() {
		key, val, _ := strings.Cut(kv, "=")
		rest, ok := strings.CutPrefix(key, "FORJA_")
		if !ok || !strings.Contains(rest, "__") {
			continue
		}
		parts := strings.Split(strings.ToLower(rest), "__")
		node := out
		for _, p := range parts[:len(parts)-1] {
			child, ok := node[p].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[p] = child
			}
			node = child
		}
		node[parts[len(parts)-1]] = parseScalar(val)
	}
	return out
}

func parseScalar(v string) any {
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}

func merge(dst, src map[string]any, layer, prefix string, sources map[string]string) {
	for k, v := range src {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if sub, ok := v.(map[string]any); ok {
			existing, ok := dst[k].(map[string]any)
			if !ok {
				existing = map[string]any{}
				dst[k] = existing
			}
			merge(existing, sub, layer, key, sources)
			continue
		}
		dst[k] = v
		sources[key] = layer
	}
}

func (c *Config) validate() error {
	switch c.Engine.FitMode {
	case "fit-first", "quality-first":
	default:
		return fmt.Errorf("engine.fit_mode: %q is not fit-first or quality-first", c.Engine.FitMode)
	}
	switch c.Routing.Policy {
	case "prefer-local", "cheapest", "fastest", "offline-only":
	default:
		return fmt.Errorf("routing.policy: %q is not prefer-local, cheapest, fastest or offline-only", c.Routing.Policy)
	}
	seen := map[string]bool{"local": true}
	for _, p := range c.Providers {
		if p.ID == "" || seen[p.ID] {
			return fmt.Errorf("providers: id %q is empty or duplicated", p.ID)
		}
		seen[p.ID] = true
	}
	return c.validatePrices()
}

// SortedSources lists "key = layer" lines for display.
func (l *Loaded) SortedSources() []string {
	keys := make([]string, 0, len(l.Sources))
	for k := range l.Sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + " = " + l.Sources[k]
	}
	return out
}
