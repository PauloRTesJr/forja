package api

import (
	"strings"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/jobs"
)

// A plan for a job with side views says which stages' models do not read them, and which model
// would: both the preset plans and the estimate carry it.
func TestAPlanSaysWhichViewsAModelDoesNotRead(t *testing.T) {
	ts, eng, _ := server(t)
	eng.installed["bg"], eng.installed["shape"], eng.installed["paint"] = true, true, true
	mv := engineapi.Model{ID: "shape-mv", Name: "Shape MV", Tasks: []string{"shape"}, Rank: 5, Fit: "fits", Status: "not-installed",
		Views: []string{"front", "left", "back", "right"}}
	eng.extra = []engineapi.Model{mv}

	var raw []map[string]any
	if code := call(t, ts, "GET", "/v1/presets?fresh=1&views=left,back", nil, &raw); code != 200 {
		t.Fatalf("presets %d", code)
	}
	var list []PresetView
	call(t, ts, "GET", "/v1/presets?views=left,back", nil, &list)
	var draft *PresetView
	for i := range list {
		if list[i].ID == "draft" {
			draft = &list[i]
		}
	}
	shape := pickOf(draft.Plan, "shape")
	if shape.Model != "shape" || strings.Join(shape.UnreadViews, ",") != "left,back" {
		t.Fatalf("shape: %+v", shape)
	}
	if rb := shape.ReadBy; rb == nil || rb.Model != "shape-mv" || rb.ModelName != "Shape MV" || rb.InstallOn != jobs.LocalProvider || rb.Installed {
		t.Fatalf("read by %+v", rb)
	}
	// The JSON the clients read.
	for _, p := range raw {
		if p["id"] != "draft" {
			continue
		}
		for _, pick := range p["plan"].([]any) {
			m := pick.(map[string]any)
			if m["stage"] != "shape" {
				continue
			}
			rb, _ := m["read_by"].(map[string]any)
			if m["unread_views"] == nil || rb == nil || rb["model"] != "shape-mv" || rb["install_on"] != "local" {
				t.Fatalf("shape on the wire: %v", m)
			}
		}
	}

	// Without views nothing is unread.
	var plain []PresetView
	call(t, ts, "GET", "/v1/presets", nil, &plain)
	for _, p := range plain {
		if shape := pickOf(p.Plan, "shape"); shape.UnreadViews != nil || shape.ReadBy != nil {
			t.Fatalf("%s without views: %+v", p.ID, shape)
		}
	}

	var est jobs.Estimate
	if code := call(t, ts, "POST", "/v1/estimate", map[string]any{"preset": "draft", "views": []string{"right"}}, &est); code != 200 {
		t.Fatalf("estimate %d", code)
	}
	for _, s := range est.Stages {
		if s.Use == "shape" && (strings.Join(s.UnreadViews, ",") != "right" || s.ReadBy == nil || s.ReadBy.Model != "shape-mv") {
			t.Fatalf("estimate: %+v", s)
		}
	}
}

func pickOf(plan []jobs.Pick, use string) jobs.Pick {
	for _, p := range plan {
		if p.Use == use {
			return p
		}
	}
	return jobs.Pick{}
}
