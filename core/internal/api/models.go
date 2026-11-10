package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/installs"
	"forja/internal/jobs"
	"forja/internal/plain"
	"forja/internal/provider"
)

// sample is a flat drawing of a chest, the input of the test run that ends setup.
//
//go:embed sample.png
var sample []byte

// ModelView is a provider's model plus what only the core knows: license acceptance and
// which stages it does now.
type ModelView struct {
	engineapi.Model
	Provider        string     `json:"provider"`
	ProviderLabel   string     `json:"provider_label"`
	Paid            bool       `json:"paid"`
	NeedsAcceptance bool       `json:"needs_acceptance"`
	AcceptedAt      *time.Time `json:"accepted_at,omitempty"`
	Uses            []ModelUse `json:"uses"`
}

// ModelUse is a stage type the model does when a preset leaves it open; pinned in config or
// picked by rank.
type ModelUse struct {
	Use    string `json:"use"`
	Pinned bool   `json:"pinned"`
}

func providerParam(r *http.Request) string {
	if id := r.URL.Query().Get("provider"); id != "" {
		return id
	}
	return jobs.LocalProvider
}

// models lists one provider's models (?provider=, default local), or every provider's with
// ?all=1. A provider that cannot answer is left out of an all-providers list.
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	ids := []string{providerParam(r)}
	all := r.URL.Query().Get("all") == "1"
	if all {
		ids = ids[:0]
		for _, p := range s.Providers.All() {
			ids = append(ids, p.ID())
		}
	} else if _, ok := s.Providers.Get(ids[0]); !ok {
		problem(w, 404, "no provider "+ids[0])
		return
	}
	get := s.Catalog.Get
	if fresh(r) {
		get = s.Catalog.Ask
	}
	cfg, err := s.Runner.Config("")
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	labels := map[string]string{jobs.LocalProvider: "This computer"}
	for _, pc := range cfg.Providers {
		labels[pc.ID] = pc.Label
	}
	accepted, _ := s.DB.Acceptances(r.Context())
	auto := map[string]jobs.Pick{}
	out := []ModelView{}
	for _, id := range ids {
		e, err := get(r.Context(), id)
		if err != nil {
			if !all {
				problem(w, 502, plain.Err(err))
				return
			}
			continue
		}
		for _, m := range e.Models {
			v := ModelView{Model: m, Provider: id, ProviderLabel: orID(labels[id], id), Uses: []ModelUse{},
				NeedsAcceptance: m.License.NeedsAcceptance()}
			if p, ok := s.Providers.Get(id); ok {
				v.Paid = p.Paid()
			}
			if at, ok := accepted[m.ID+" "+m.License.SPDX]; ok {
				v.AcceptedAt = &at
			}
			for _, use := range m.Tasks {
				if pin := cfg.Models.Stages[use]; pin != "" {
					if pin == m.ID && (cfg.Routing.Stages[use] == "" || cfg.Routing.Stages[use] == id) {
						v.Uses = append(v.Uses, ModelUse{Use: use, Pinned: true})
					}
					continue
				}
				pick, ok := auto[use]
				if !ok {
					pick, _ = s.Runner.Pick(r.Context(), "", use)
					auto[use] = pick
				}
				if pick.Model == m.ID && pick.Provider == id {
					v.Uses = append(v.Uses, ModelUse{Use: use})
				}
			}
			out = append(out, v)
		}
	}
	writeJSON(w, 200, out)
}

func orID(label, id string) string {
	if label == "" {
		return id
	}
	return label
}

type installBody struct {
	Provider      string `json:"provider"`
	AcceptLicense bool   `json:"accept_license"`
	engineapi.InstallRequest
}

func (s *Server) modelInstall(w http.ResponseWriter, r *http.Request) {
	var body installBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if body.Provider == "" {
		body.Provider = jobs.LocalProvider
	}
	id := r.PathValue("id")
	e, err := s.Catalog.Get(r.Context(), body.Provider)
	if err != nil {
		problem(w, 502, plain.Err(err))
		return
	}
	m, ok := e.Model(id)
	if !ok {
		problem(w, 404, "provider "+body.Provider+" has no model "+id)
		return
	}
	if lic := m.License; lic.NeedsAcceptance() {
		accepted, err := s.DB.Acceptances(r.Context())
		if err != nil {
			problem(w, 500, plain.Err(err))
			return
		}
		if _, ok := accepted[id+" "+lic.SPDX]; !ok {
			if !body.AcceptLicense {
				problemWith(w, 409, m.Name+" is under "+lic.SPDX+"; accept its license to install it",
					map[string]any{"license": lic, "model": id})
				return
			}
			if err := s.DB.AcceptLicense(r.Context(), id, lic.SPDX); err != nil {
				problem(w, 500, plain.Err(err))
				return
			}
		}
	}
	s.startTask(w, r, body.Provider, func(i provider.Installer) (*engineapi.Task, error) {
		return i.Install(r.Context(), id, body.InstallRequest)
	})
}

func (s *Server) modelRemove(w http.ResponseWriter, r *http.Request) {
	id, from := r.PathValue("id"), providerParam(r)
	if e, err := s.Catalog.Get(r.Context(), from); err == nil {
		if _, ok := e.Model(id); !ok {
			problem(w, 404, "provider "+from+" has no model "+id+": say which computer it is on with ?provider=")
			return
		}
	}
	entry, err := s.Installs.Start(from, func(i provider.Installer) (*engineapi.Task, error) {
		return i.Remove(r.Context(), id)
	})
	if err != nil {
		writeProviderError(w, err)
		return
	}
	s.unpinRemoved(id, from)
	writeJSON(w, 202, entry)
}

// unpinRemoved drops the pins to a model that is being removed from a provider, unless
// another provider still has it ready: a stage type pinned to a model nobody has refuses every
// job, and the pin is not something the user would think to look for.
func (s *Server) unpinRemoved(model, from string) {
	cfg, err := config.Load("", nil)
	if err != nil {
		return
	}
	for _, p := range s.Providers.All() {
		if e, ok := s.Catalog.Last(p.ID()); ok && p.ID() != from {
			if m, ok := e.Model(model); ok && m.Ready() {
				return
			}
		}
	}
	changes := map[string]any{}
	for use, pin := range cfg.Models.Stages {
		if pin != model || strings.Contains(use, ".") {
			continue
		}
		if to := cfg.Routing.Stages[use]; to == "" || to == from {
			changes["models.stages."+use] = nil
			if to == from {
				changes["routing.stages."+use] = nil
			}
		}
	}
	if len(changes) == 0 {
		return
	}
	if err := config.Edit(config.UserFile(), changes, func() error { _, err := config.Load("", nil); return err }); err != nil {
		s.Log.Warn("could not unpin a removed model", "model", model, "err", err)
		return
	}
	s.Hub.Publish(EvSettingsChanged, map[string]string{"project": ""})
	s.Hub.Publish(EvModelsChanged, map[string]string{"provider": from})
}

// pinnable says why a model cannot be pinned to a stage type, in the terms of a 422; "" when
// it can. on is the provider the pin names, or "" for any.
func (s *Server) pinnable(ctx context.Context, use, model, on string) string {
	ids := []string{on}
	if on == "" {
		ids = ids[:0]
		for _, p := range s.Providers.All() {
			ids = append(ids, p.ID())
		}
	}
	var silent []string
	var found *engineapi.Model
	for _, id := range ids {
		e, err := s.Catalog.Get(ctx, id)
		if err != nil {
			silent = append(silent, id)
			continue
		}
		if m, ok := e.Model(model); ok {
			if m.Does(use) {
				return ""
			}
			found = &m
		}
	}
	switch {
	case found != nil:
		return fmt.Sprintf("%s is not a %s model (it does %s)", orID(found.Name, found.ID), use, strings.Join(found.Tasks, ", "))
	case len(silent) > 0:
		return fmt.Sprintf("no provider that answers has a model %s (%s could not be asked): `forja models ls` lists the models",
			model, strings.Join(silent, ", "))
	case on != "":
		return fmt.Sprintf("provider %s has no model %s: `forja models ls --provider %s` lists its models", on, model, on)
	}
	return fmt.Sprintf("no provider has a model %s: `forja models ls` lists the models", model)
}

func (s *Server) startTask(w http.ResponseWriter, r *http.Request, providerID string,
	begin func(provider.Installer) (*engineapi.Task, error),
) {
	entry, err := s.Installs.Start(providerID, begin)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 202, entry)
}

// writeProviderError passes a provider's refusal (409, 403, 404) through with its status.
func writeProviderError(w http.ResponseWriter, err error) {
	var prob interface{ StatusCode() int }
	switch {
	case errors.Is(err, installs.ErrUnknownProvider):
		problem(w, 404, plain.Err(err))
	case errors.Is(err, installs.ErrNoInstaller):
		problem(w, 409, plain.Err(err))
	case errors.As(err, &prob):
		problem(w, prob.StatusCode(), detailOf(err))
	default:
		problem(w, 502, plain.Err(err))
	}
}

func detailOf(err error) string {
	var d interface{ ProblemDetail() string }
	if errors.As(err, &d) {
		return d.ProblemDetail()
	}
	return err.Error()
}

// modelStage pins the model a stage type uses when a preset leaves it open; an empty model
// goes back to picking by rank. It writes the user's config.toml.
func (s *Server) modelStage(w http.ResponseWriter, r *http.Request) {
	use := r.PathValue("use")
	var body struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if use == "" || strings.ContainsAny(use, ". ") {
		problem(w, 400, "not a stage type: "+use)
		return
	}
	if body.Provider != "" {
		if _, ok := s.Providers.Get(body.Provider); !ok {
			problem(w, 404, "no provider "+body.Provider)
			return
		}
	}
	if body.Model != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if why := s.pinnable(ctx, use, body.Model, body.Provider); why != "" {
			problem(w, 422, why)
			return
		}
	}
	// The model and, when given, the provider that runs it; nothing means automatic.
	var model, prov any
	if body.Model != "" {
		model = body.Model
		if body.Provider != "" {
			prov = body.Provider
		}
	}
	err := config.Edit(config.UserFile(), map[string]any{"models.stages." + use: model, "routing.stages." + use: prov}, func() error {
		_, err := config.Load("", nil)
		return err
	})
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	s.Hub.Publish(EvSettingsChanged, map[string]string{"project": ""})
	s.Hub.Publish(EvModelsChanged, map[string]string{"provider": ""})
	w.WriteHeader(204)
}

func (s *Server) checks(w http.ResponseWriter, r *http.Request) {
	inst, err := s.Installs.Installer(providerParam(r))
	if err != nil {
		writeProviderError(w, err)
		return
	}
	var models, stages []string
	if q := r.URL.Query().Get("models"); q != "" {
		models = strings.Split(q, ",")
	}
	// ?stages=mesh.remesh: what this machine needs to run those stage types itself (Blender).
	if q := r.URL.Query().Get("stages"); q != "" {
		stages = strings.Split(q, ",")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := inst.Checks(ctx, models, stages)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) checkFix(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if from := providerParam(r); from != jobs.LocalProvider {
		if _, ok := s.Providers.Get(from); !ok {
			problem(w, 404, "no provider "+from)
			return
		}
		problem(w, 409, "A fix installs software on the computer that runs the engine, so it can only run on that computer. Open Forja there to apply it.")
		return
	}
	s.startTask(w, r, providerParam(r), func(i provider.Installer) (*engineapi.Task, error) {
		return i.Fix(r.Context(), id)
	})
}

func (s *Server) installList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.Installs.List())
}

func (s *Server) installCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Installs.Cancel(r.Context(), r.PathValue("provider"), r.PathValue("id")); err != nil {
		writeProviderError(w, err)
		return
	}
	w.WriteHeader(202)
}

// SetupStage is how one stage type that presets need is covered.
type SetupStage struct {
	Use string `json:"use"`
	// ready: a provider has a model for it; install: this machine can run the model below;
	// remote: no model runs here, so it needs another computer or a cloud provider.
	Status    string  `json:"status"`
	Model     string  `json:"model,omitempty"`
	ModelName string  `json:"model_name,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	DiskGB    float64 `json:"disk_gb,omitempty"`
	Fit       string  `json:"fit,omitempty"`
}

type SetupView struct {
	System  map[string]any `json:"system"`
	Stages  []SetupStage   `json:"stages"`
	Install []string       `json:"install"`
	DiskGB  float64        `json:"disk_gb"`
	Ready   bool           `json:"ready"`
}

// setup recommends what to install so every built-in preset runs: per stage type, the
// best-ranked model this machine can run, unless some provider already has one ready.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	s.askAll(ctx)
	local, err := s.Catalog.Get(ctx, jobs.LocalProvider)
	if err != nil {
		problem(w, 502, plain.Err(err))
		return
	}
	all, err := s.Runner.Presets("")
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	var uses []string
	covered := map[string]jobs.Pick{}
	for _, id := range []string{"draft", "mobile-prop", "environment-piece", "hero-character"} {
		p, ok := all[id]
		if !ok {
			continue
		}
		plan, err := s.Runner.Resolve(ctx, "", p, nil)
		if err != nil {
			problem(w, 502, plain.Err(err))
			return
		}
		for _, pick := range plan {
			if pick.Model == "" && !pick.Missing {
				continue
			}
			if !slices.Contains(uses, pick.Use) {
				uses = append(uses, pick.Use)
			}
			if !pick.Missing {
				covered[pick.Use] = pick
			}
		}
	}
	view := SetupView{System: local.System, Stages: []SetupStage{}, Install: []string{}, Ready: true}
	for _, use := range uses {
		st := SetupStage{Use: use}
		if pick, ok := covered[use]; ok {
			st.Status, st.Model, st.ModelName, st.Provider = "ready", pick.Model, pick.ModelName, pick.Provider
			view.Stages = append(view.Stages, st)
			continue
		}
		view.Ready = false
		best := bestHere(local.Models, use)
		if best == nil {
			st.Status = "remote"
		} else {
			st.Status, st.Model, st.ModelName, st.Provider = "install", best.ID, best.Name, jobs.LocalProvider
			st.DiskGB, st.Fit = best.DiskGB, best.Fit
			if !slices.Contains(view.Install, best.ID) {
				view.Install = append(view.Install, best.ID)
				view.DiskGB += best.DiskGB
			}
		}
		view.Stages = append(view.Stages, st)
	}
	writeJSON(w, 200, view)
}

// bestHere is the best-ranked real model for a stage type that this computer can run, with
// offload if need be: what setup and a blocked optional stage offer to install. Nil when
// every model for it needs another computer.
func bestHere(models []engineapi.Model, use string) *engineapi.Model {
	var best *engineapi.Model
	for i := range models {
		m := &models[i]
		if m.Stub || !m.Does(use) || (m.Fit != "fits" && m.Fit != "offload") {
			continue
		}
		if best == nil || m.Rank > best.Rank {
			best = m
		}
	}
	return best
}

// setupTest generates the bundled sample with the default preset, to prove the models work
// together end to end.
func (s *Server) setupTest(w http.ResponseWriter, r *http.Request) {
	ref, err := s.Store.PutBytes(sample)
	if err != nil {
		problem(w, 500, plain.Err(err))
		return
	}
	job, err := s.Runner.Submit(r.Context(), jobs.SubmitRequest{Input: ref, Name: "sample-chest", Priority: "interactive"})
	if err != nil {
		problem(w, 422, plain.Err(err))
		return
	}
	writeJSON(w, 202, job)
}
