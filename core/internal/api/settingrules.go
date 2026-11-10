package api

import (
	"fmt"
	"math"
	"strconv"

	"forja/internal/jobs"
)

// numberRule is what a number setting accepts and how its refusal reads: the app shows min and
// max next to the field, and the refusal names the setting in words, not by key.
type numberRule struct {
	label string // starts the sentence: "VRAM reserve"
	unit  string // " GB", " minutes", "" (a sum is written with its currency)
	money bool
	min   float64
	max   float64 // a ceiling no sane value reaches, so a typo such as 1e20 never reaches config.toml
	note  string  // what 0 means, when it is special
	// card says the ceiling is the graphics card's memory when one is detected.
	card bool
}

var numberRules = map[string]numberRule{
	"engine.idle_unload_minutes":   {label: "Unload idle models after", unit: " minutes", max: 10080, note: "0 means never"},
	"engine.vram_reserve_gb":       {label: "VRAM reserve", unit: " GB", max: 1024, card: true},
	"routing.budget_usd.per_job":   {label: "The spending limit per generation", money: true, max: 1e6, note: "0 means no limit"},
	"routing.budget_usd.per_day":   {label: "The daily spending limit", money: true, max: 1e6, note: "0 means no limit"},
	"routing.budget_usd.per_month": {label: "The monthly spending limit", money: true, max: 1e6, note: "0 means no limit"},
}

// cardGB is the memory of the largest graphics card the local engine reported; 0 when unknown.
func (s *Server) cardGB() float64 {
	if s.Catalog == nil {
		return 0
	}
	e, ok := s.Catalog.Last(jobs.LocalProvider)
	if !ok || e == nil {
		return 0
	}
	devices, _ := e.System["devices"].([]any)
	var best float64
	for _, d := range devices {
		m, _ := d.(map[string]any)
		if b, ok := m["vram_bytes"].(float64); ok {
			best = math.Max(best, b/(1<<30))
		}
	}
	return math.Floor(best*10) / 10
}

// limits returns a rule's bounds for this computer.
func (s *Server) limits(key string) (rule numberRule, lo, hi float64, ok bool) {
	rule, ok = numberRules[key]
	if !ok {
		return rule, 0, 0, false
	}
	hi = rule.max
	if rule.card {
		if c := s.cardGB(); c > 0 {
			hi = c
		}
	}
	return rule, rule.min, hi, true
}

func (r numberRule) show(v float64) string {
	if r.money {
		return "$" + strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + r.unit
}

// number checks v against the rule of key; the error is a sentence for a person.
func (s *Server) number(key string, v any, whole bool) (float64, error) {
	rule, lo, hi, _ := s.limits(key)
	f, isNum := v.(float64)
	kind := "a number"
	if whole {
		kind = "a whole number"
	}
	bound := fmt.Sprintf("from %s to %s", rule.show(lo), rule.show(hi))
	if rule.card && hi < rule.max {
		bound += " (the size of your graphics card)"
	}
	if rule.note != "" {
		bound += "; " + rule.note
	}
	switch {
	case !isNum || math.IsNaN(f) || math.IsInf(f, 0):
		return 0, fmt.Errorf("%s needs %s %s.", rule.label, kind, bound)
	case whole && f != math.Trunc(f):
		return 0, fmt.Errorf("%s needs %s %s.", rule.label, kind, bound)
	case f < lo || f > hi:
		return 0, fmt.Errorf("%s must be %s.", rule.label, bound)
	}
	return f, nil
}
