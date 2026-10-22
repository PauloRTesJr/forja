package engineapi

import (
	"fmt"
	"strings"
)

// StageSpec is one stage of a run request. Inputs are "stage:<id>" or "blake3:<hex>".
type StageSpec struct {
	ID     string         `json:"id"`
	Use    string         `json:"use"`
	Model  string         `json:"model,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	Inputs []string       `json:"inputs"`
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
}

type License struct {
	SPDX         string   `json:"spdx"`
	Commercial   bool     `json:"commercial"`
	Restrictions []string `json:"restrictions"`
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
