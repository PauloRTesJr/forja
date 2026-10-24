package api

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/jobs"
)

// estimate says how long a job would take and what it would cost before it is queued: the
// Generate screen shows it next to the drop zone, `forja generate --estimate` prints it.
func (s *Server) estimate(w http.ResponseWriter, r *http.Request) {
	var req jobs.EstimateRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	est, err := s.Runner.Estimate(r.Context(), req)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, est)
}

// PriceRow is one entry of the price table and what it prices, or a hosted model's own price
// that no entry replaces.
type PriceRow struct {
	ID string `json:"id"`
	config.Price
	// Source is the layer that set the entry (default, user, project...), or "vendor" for the
	// price a hosted model's provider reports.
	Source string `json:"source"`
	// Default is the built-in entry a user's entry replaces.
	Default *config.Price `json:"default,omitempty"`
	// Vendor is what the provider of a hosted model reports for it.
	Vendor *engineapi.Price `json:"vendor,omitempty"`
	// For names what the entry prices, for people: providers, or a hosted model.
	For []string `json:"for"`
	// Model and Provider name the hosted model a row prices.
	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type PricesView struct {
	Prices []PriceRow `json:"prices"`
	// Units are what a price may be per.
	Units []string `json:"units"`
	// File is the config.toml the app writes prices to.
	File string `json:"file"`
}

func (s *Server) pricesView() (*PricesView, error) {
	cfg, err := s.Runner.Config("")
	if err != nil {
		return nil, err
	}
	defaults, _ := config.Defaults()["prices"].(map[string]any)
	byID := map[string]*PriceRow{}
	for id, p := range cfg.Prices {
		row := &PriceRow{ID: id, Price: p, Source: cfg.Sources["prices."+id+".usd"], For: []string{}}
		if row.Source == "" {
			row.Source = "default"
		}
		if d, ok := defaults[id].(map[string]any); ok && row.Source != "default" {
			usd, _ := d["usd"].(float64)
			per, _ := d["per"].(string)
			label, _ := d["label"].(string)
			row.Default = &config.Price{USD: usd, Per: per, Label: label}
		}
		byID[id] = row
	}
	labels := map[string]string{jobs.LocalProvider: "This computer"}
	for _, pc := range cfg.Providers {
		labels[pc.ID] = orID(pc.Label, pc.ID)
		if _, entry := cfg.Rate(pc.ID); entry != "" && byID[entry] != nil {
			byID[entry].For = append(byID[entry].For, labels[pc.ID])
		}
	}
	// Hosted models price themselves; an entry named after one replaces its price.
	for _, p := range s.Providers.All() {
		if !p.Paid() {
			continue
		}
		e, ok := s.Catalog.Last(p.ID())
		if !ok {
			continue
		}
		for _, m := range e.Models {
			if m.Price == nil || m.Status == "unsupported" {
				continue
			}
			what := m.Name + " on " + orID(labels[p.ID()], p.ID())
			row := byID[m.ID]
			if row == nil {
				row = &PriceRow{ID: m.ID, Price: config.Price{Label: m.Name, USD: m.Price.Amount, Per: m.Price.Unit}, Source: "vendor"}
				byID[m.ID] = row
			}
			row.For = append(row.For, what)
			row.Vendor, row.Model, row.Provider = m.Price, m.ID, p.ID()
		}
	}
	view := &PricesView{Prices: []PriceRow{}, Units: config.Units, File: config.UserFile()}
	for _, row := range byID {
		view.Prices = append(view.Prices, *row)
	}
	// Rates for providers' time first, then hosted models, each by id.
	sort.Slice(view.Prices, func(i, j int) bool {
		a, b := view.Prices[i], view.Prices[j]
		if (a.Model == "") != (b.Model == "") {
			return a.Model == ""
		}
		return a.ID < b.ID
	})
	return view, nil
}

func (s *Server) prices(w http.ResponseWriter, r *http.Request) {
	view, err := s.pricesView()
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, view)
}

// priceID checks an entry's id: a provider's rate card or a hosted model's id.
func priceID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' })
}

// priceSet writes an entry of the price table to the user's config.toml, keeping its comments.
func (s *Server) priceSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !priceID(id) {
		problem(w, 400, "not a price id: "+id)
		return
	}
	var body config.Price
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if body.USD < 0 || math.IsNaN(body.USD) || math.IsInf(body.USD, 0) {
		problem(w, 422, "the price must be a number of 0 or more")
		return
	}
	if !slices.Contains(config.Units, body.Per) {
		problem(w, 422, "per: want "+strings.Join(config.Units, ", "))
		return
	}
	changes := map[string]any{config.Key("prices", id, "usd"): body.USD, config.Key("prices", id, "per"): body.Per}
	if label := strings.TrimSpace(body.Label); label != "" {
		changes[config.Key("prices", id, "label")] = label
	}
	if err := config.Edit(config.UserFile(), changes, validateUser); err != nil {
		problem(w, 422, err.Error())
		return
	}
	s.pricesChanged(w, r)
}

// priceRemove removes the user's entry: a built-in one goes back to its default, a hosted
// model back to the price its provider reports.
func (s *Server) priceRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg, err := s.Runner.Config("")
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	switch src := cfg.Sources["prices."+id+".usd"]; src {
	case "user":
	case "":
		problem(w, 404, "config.toml has no price "+id)
		return
	case "default":
		problem(w, 409, id+" is a built-in price: set it to change it")
		return
	default:
		problem(w, 409, "price "+id+" is set in the "+src+" configuration; change it there")
		return
	}
	changes := map[string]any{}
	for _, k := range []string{"usd", "per", "label"} {
		changes[config.Key("prices", id, k)] = nil
	}
	if err := config.Edit(config.UserFile(), changes, validateUser); err != nil {
		problem(w, 422, err.Error())
		return
	}
	s.pricesChanged(w, r)
}

// pricesChanged answers with the table as it is now and tells clients: estimates, plans and
// the Models screen's costs depend on it.
func (s *Server) pricesChanged(w http.ResponseWriter, r *http.Request) {
	s.Hub.Publish(EvSettingsChanged, map[string]string{"prices": r.PathValue("id")})
	view, err := s.pricesView()
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, view)
}
