package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/installs"
	"forja/internal/jobs"
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
		problem(w, 422, err.Error())
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
				problem(w, 502, err.Error())
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
		problem(w, 502, err.Error())
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
			problem(w, 500, err.Error())
			return
		}
		if _, ok := accepted[id+" "+lic.SPDX]; !ok {
			if !body.AcceptLicense {
				problemWith(w, 409, m.Name+" is under "+lic.SPDX+"; accept its license to install it",
					map[string]any{"license": lic, "model": id})
				return
			}
			if err := s.DB.AcceptLicense(r.Context(), id, lic.SPDX); err != nil {
				problem(w, 500, err.Error())
				return
			}
		}
	}
	s.startTask(w, r, body.Provider, func(i provider.Installer) (*engineapi.Task, error) {
		return i.Install(r.Context(), id, body.InstallRequest)
	})
}

func (s *Server) modelRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.startTask(w, r, providerParam(r), func(i provider.Installer) (*engineapi.Task, error) {
		return i.Remove(r.Context(), id)
	})
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
	case errors.Is(err, installs.ErrNoInstaller):
		problem(w, 409, err.Error())
	case errors.As(err, &prob):
		problem(w, prob.StatusCode(), detailOf(err))
	default:
		problem(w, 502, err.Error())
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
		problem(w, 422, err.Error())
		return
	}
	s.Hub.Publish(EvModelsChanged, map[string]string{"provider": ""})
	w.WriteHeader(204)
}

func (s *Server) checks(w http.ResponseWriter, r *http.Request) {
	inst, err := s.Installs.Installer(providerParam(r))
	if err != nil {
		writeProviderError(w, err)
		return
	}
	var models []string
	if q := r.URL.Query().Get("models"); q != "" {
		models = strings.Split(q, ",")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	out, err := inst.Checks(ctx, models)
	if err != nil {
		writeProviderError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) checkFix(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
		problem(w, 502, err.Error())
		return
	}
	all, err := s.Runner.Presets("")
	if err != nil {
		problem(w, 422, err.Error())
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
			problem(w, 502, err.Error())
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
		var best *engineapi.Model
		for i := range local.Models {
			m := &local.Models[i]
			if m.Stub || !m.Does(use) || (m.Fit != "fits" && m.Fit != "offload") {
				continue
			}
			if best == nil || m.Rank > best.Rank {
				best = m
			}
		}
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

// setupTest generates the bundled sample with the default preset, to prove the models work
// together end to end.
func (s *Server) setupTest(w http.ResponseWriter, r *http.Request) {
	ref, err := s.Store.PutBytes(sample)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	job, err := s.Runner.Submit(r.Context(), jobs.SubmitRequest{Input: ref, Name: "sample-chest", Priority: "interactive"})
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 202, job)
}
