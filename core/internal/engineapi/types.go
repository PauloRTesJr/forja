package engineapi

import (
	"fmt"
	"slices"
	"strings"
)

// Views are what a reference image can show of its subject. "left" shows the subject's left
// side, so it faces the image's left edge; "right" is the mirror of that.
var Views = []string{"front", "left", "back", "right"}

// StageSpec is one stage of a run request. Inputs are "stage:<id>" or "blake3:<hex>".
type StageSpec struct {
	ID     string         `json:"id"`
	Use    string         `json:"use"`
	Model  string         `json:"model,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Inputs []string       `json:"inputs"`
	// Views says which view each input shows ("" for none) when the stage reads several (API 1.6).
	Views []string `json:"views,omitempty"`
}

type RunRequest struct {
	ClientRunID string      `json:"client_run_id,omitempty"`
	Priority    string      `json:"priority"`
	Seed        int         `json:"seed"`
	Stages      []StageSpec `json:"stages"`
}

type Output struct {
	Hash    string `json:"hash"`
	Kind    string `json:"kind"`
	Preview bool   `json:"preview"`
}

type StageState struct {
	Status  string   `json:"status"`
	Outputs []Output `json:"outputs"`
	Seconds *float64 `json:"seconds"`
	Error   *string  `json:"error"`
	Step    int      `json:"step"`
	Total   int      `json:"total"`
	// PeakVRAMBytes is PyTorch's reserved peak during the stage; nil when not measurable.
	PeakVRAMBytes *int64 `json:"peak_vram_bytes"`
}

type Run struct {
	ID     string                `json:"id"`
	Status string                `json:"status"`
	Error  *string               `json:"error"`
	Stages map[string]StageState `json:"stages"`
}

// Event is one Server-Sent Event from /v1/runs/{id}/events.
type Event struct {
	ID   int            `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type Info struct {
	Version    string         `json:"version"`
	APIVersion string         `json:"api_version"`
	System     map[string]any `json:"system"`
	FitMode    string         `json:"fit_mode"`
	Lanes      map[string]any `json:"lanes"`
	// Stages maps each built-in stage type to its adapter version (API 1.2+).
	Stages map[string]string `json:"stages"`
	// Unavailable says why the engine runs some built-in stage types nowhere here: those backed
	// by Blender where none is found (API 1.7). Routing passes such an engine by.
	Unavailable map[string]string `json:"unavailable,omitempty"`
}

type Model struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Family   string         `json:"family"`
	Tasks    []string       `json:"tasks"`
	License  License        `json:"license"`
	Params   map[string]any `json:"params"`
	Status   string         `json:"status"`
	Device   *string        `json:"device"`
	Requires map[string]any `json:"requirements"`
	Weights  Weights        `json:"weights"`
	// AdapterVersion and Upstream (the family's pinned source sha256) arrive with API 1.2.
	AdapterVersion string `json:"adapter_version"`
	Upstream       string `json:"upstream"`
	// Rank orders models for the same stage when a preset leaves the model open; Stub marks
	// the deterministic stand-ins (API 1.3).
	Rank int  `json:"rank"`
	Stub bool `json:"stub"`
	// What the Models screen shows (API 1.4): where the model would run here and how well
	// ("fits", "offload", "too-big", "unsupported"), its size, and a running install or removal.
	Description string  `json:"description"`
	DiskGB      float64 `json:"disk_gb"`
	VRAMGB      float64 `json:"vram_gb"`
	RunsOn      *string `json:"runs_on"`
	Fit         string  `json:"fit"`
	Task        *string `json:"task"`
	// Hosted models (a model hub such as fal.ai): what one generation costs, whether Forja
	// recommends it, and a picture for the catalog.
	Price       *Price `json:"price,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
	Thumbnail   string `json:"thumbnail,omitempty"`
	// Views the model reads besides the front (API 1.6); empty when it takes one image.
	// ViewsRequired are the ones it cannot run without: a hosted model may need a back view.
	Views         []string `json:"views,omitempty"`
	ViewsRequired []string `json:"views_required,omitempty"`
}

// Price is what a hosted model charges per Unit. PerCall is the unit for one generation, the
// only price a job's cost can be added up from before it runs; any other unit (a second, a
// vendor's compute unit) is shown as is.
type Price struct {
	Amount   float64 `json:"amount"`
	Unit     string  `json:"unit"`
	Currency string  `json:"currency"`
}

const PerCall = "generation"

// CatalogPage is one page of a model hub's catalog, as Forja can use it.
type CatalogPage struct {
	Models     []CatalogModel `json:"models"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type CatalogModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Thumbnail   string   `json:"thumbnail,omitempty"`
	License     License  `json:"license"`
	Price       *Price   `json:"price,omitempty"`
	Tasks       []string `json:"tasks"`
	// Problem says why Forja cannot call the model (no single image input, no mesh output).
	Problem     string `json:"problem,omitempty"`
	Added       bool   `json:"added"`
	Recommended bool   `json:"recommended"`
}

// Ready reports whether the model can run on the provider now.
func (m *Model) Ready() bool {
	return (m.Status == "ready" || m.Status == "loaded") && m.Device != nil
}

// Reads reports which of views the model can use, in the order of Views.
func (m *Model) Reads(views []string) []string {
	var out []string
	for _, v := range Views {
		if slices.Contains(views, v) && slices.Contains(m.Views, v) {
			out = append(out, v)
		}
	}
	return out
}

// Lacks lists the views the model needs that are not in have (the front is always had).
func (m *Model) Lacks(have []string) []string {
	var out []string
	for _, v := range m.ViewsRequired {
		if v != "front" && !slices.Contains(have, v) {
			out = append(out, v)
		}
	}
	return out
}

// Does reports whether the model provides a stage type.
func (m *Model) Does(task string) bool {
	for _, t := range m.Tasks {
		if t == task {
			return true
		}
	}
	return false
}

type Weights struct {
	Repo     string `json:"repo"`
	Revision string `json:"revision"`
}

type License struct {
	SPDX         string   `json:"spdx"`
	Commercial   bool     `json:"commercial"`
	Restrictions []string `json:"restrictions"`
	URL          string   `json:"url"`
}

// NeedsAcceptance is true for licenses the user must accept before installing: custom ones,
// non-commercial ones and ones with restrictions (spec section 7).
func (l License) NeedsAcceptance() bool {
	return len(l.Restrictions) > 0 || !l.Commercial || strings.HasPrefix(l.SPDX, "LicenseRef-")
}

// CheckCompatible refuses engines with a different major API version.
func CheckCompatible(engineVersion string) error {
	major := func(v string) string { return strings.SplitN(v, ".", 2)[0] }
	if major(engineVersion) != major(APIVersion) {
		return fmt.Errorf("engine speaks API %s but this core needs %s.x; install a matching forja-engine",
			engineVersion, major(APIVersion))
	}
	return nil
}

// Check is one thing a machine needs before models install (API 1.4).
type Check struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	OK     bool     `json:"ok"`
	Found  string   `json:"found"`
	Need   string   `json:"need"`
	Why    string   `json:"why"`
	Fix    *Fix     `json:"fix"`
	Models []string `json:"models"`
}

type Fix struct {
	Label string `json:"label"`
	// Runnable fixes run on the provider's machine on request; the rest are steps or a link.
	Runnable bool     `json:"runnable"`
	Command  string   `json:"command"`
	URL      string   `json:"url"`
	Steps    []string `json:"steps"`
	Admin    bool     `json:"admin"`
}

type Checks struct {
	Lane   string  `json:"lane"`
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Task is an install, removal or fix running on a provider, as its latest state.
type Task struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Target     string         `json:"target"`
	Status     string         `json:"status"`
	Steps      []TaskStep     `json:"steps"`
	Progress   *TaskProgress  `json:"progress"`
	Log        []string       `json:"log"`
	Error      *string        `json:"error"`
	Result     map[string]any `json:"result"`
	CreatedAt  float64        `json:"created_at"`
	FinishedAt *float64       `json:"finished_at"`
	Version    int            `json:"version"`
}

func (t *Task) Done() bool {
	return t.Status == "succeeded" || t.Status == "failed" || t.Status == "cancelled"
}

type TaskStep struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type TaskProgress struct {
	Step  string `json:"step"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// InstallRequest picks the torch lane (default: detected) and whether to fetch weights.
type InstallRequest struct {
	Lane    string `json:"lane,omitempty"`
	Weights *bool  `json:"weights,omitempty"`
}
