package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"forja/internal/config"
	"forja/internal/provider"
	"forja/internal/provider/fal"
	"forja/internal/providers"
	"forja/internal/secrets"
)

// hubAdd connects a model hub with its API key: the key is tested, stored in the keychain, and
// the hub joins config.toml with the recommended models (docs/decisions.md, decision 16).
func (s *Server) hubAdd(w http.ResponseWriter, r *http.Request, vendor, key string) {
	if vendor != fal.Vendor {
		problem(w, 422, "model hub "+vendor+" is not supported")
		return
	}
	if key == "" {
		problem(w, 422, "paste the API key from your fal.ai dashboard")
		return
	}
	id := s.newProviderID("fal")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := fal.New(id, key, fal.RecommendedIDs(), s.Store).Health(ctx); err != nil {
		problem(w, 422, err.Error())
		return
	}
	pc := config.Provider{ID: id, Label: "fal.ai", Kind: string(provider.KindAPI), Vendor: fal.Vendor,
		Secret: providers.SecretName(id), Paid: true, Models: fal.RecommendedIDs()}
	if err := secrets.Set(pc.Secret, key); err != nil {
		problem(w, 500, "could not store the key in "+secrets.Store()+": "+err.Error())
		return
	}
	if err := config.AddProvider(config.UserFile(), pc, validateUser); err != nil {
		_ = secrets.Delete(pc.Secret)
		problem(w, 422, err.Error())
		return
	}
	// In routing order like any provider; paid ones are still never picked automatically
	// unless routing.allow_paid_fallback says so.
	if cfg, err := config.Load("", nil); err == nil && !slices.Contains(cfg.Routing.Priority, id) {
		priority := append(slices.Clone(cfg.Routing.Priority), id)
		_ = config.Edit(config.UserFile(), map[string]any{"routing.priority": anySlice(priority)}, validateUser)
	}
	s.rebuild(pc)
	writeJSON(w, 201, map[string]any{"id": id, "label": pc.Label})
}

func validateUser() error {
	_, err := config.Load("", nil)
	return err
}

// rebuild replaces a provider in the registry from its config and asks what it runs now.
func (s *Server) rebuild(pc config.Provider) {
	p, err := providers.Build(pc, s.Store)
	if err != nil {
		s.Log.Warn("rebuilding provider", "provider", pc.ID, "err", err)
		return
	}
	s.Providers.Add(p)
	go func() { _, _ = s.Catalog.Ask(context.Background(), pc.ID) }()
	s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": pc.ID})
}

func (s *Server) hub(w http.ResponseWriter, id string) (provider.Hub, bool) {
	p, ok := s.Providers.Get(id)
	if !ok {
		problem(w, 404, "no provider "+id)
		return nil, false
	}
	h, ok := p.(provider.Hub)
	if !ok {
		problem(w, 409, id+" has no catalog to browse")
		return nil, false
	}
	return h, true
}

// hubCatalog searches a hub's catalog, a page at a time.
func (s *Server) hubCatalog(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hub(w, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	page, err := h.Search(ctx, r.URL.Query().Get("q"), r.URL.Query().Get("cursor"))
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, page)
}

// maxThumbnail caps a relayed catalog picture.
const maxThumbnail = 4 << 20

// hubThumbnail relays a catalog picture: the desktop page loads images only from the core.
func (s *Server) hubThumbnail(w http.ResponseWriter, r *http.Request) {
	h, ok := s.hub(w, r.PathValue("id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	body, kind, err := h.Thumbnail(ctx, r.URL.Query().Get("url"))
	if err != nil {
		problem(w, 502, err.Error())
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "max-age=86400")
	_, _ = io.Copy(w, io.LimitReader(body, maxThumbnail))
}

// hubModels adds a model to a hub's list (POST) or removes one (DELETE ?model=).
func (s *Server) hubModels(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pc, ok := s.userProvider(w, id)
	if !ok {
		return
	}
	h, ok := s.hub(w, id)
	if !ok {
		return
	}
	var model string
	if r.Method == http.MethodPost {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Model == "" {
			problem(w, 400, "send the model to add")
			return
		}
		model = body.Model
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		checked, err := h.Check(ctx, model)
		if err != nil {
			problem(w, 502, err.Error())
			return
		}
		if checked.Problem != "" {
			problem(w, 422, "Forja cannot use "+checked.Name+": "+checked.Problem)
			return
		}
		if !slices.Contains(pc.Models, model) {
			pc.Models = append(pc.Models, model)
		}
	} else {
		model = r.URL.Query().Get("model")
		pc.Models = slices.DeleteFunc(slices.Clone(pc.Models), func(m string) bool { return m == model })
		if pc.Models == nil {
			pc.Models = []string{}
		}
		// A stage pinned to the model goes back to picking automatically.
		if cfg, err := config.Load("", nil); err == nil {
			unpin := map[string]any{}
			for use, m := range cfg.Models.Stages {
				if m == model && cfg.Routing.Stages[use] == id {
					unpin["models.stages."+use], unpin["routing.stages."+use] = nil, nil
				}
			}
			if len(unpin) > 0 {
				_ = config.Edit(config.UserFile(), unpin, validateUser)
			}
		}
	}
	if err := config.UpdateProvider(config.UserFile(), pc, validateUser); err != nil {
		problem(w, 422, err.Error())
		return
	}
	s.rebuild(pc)
	w.WriteHeader(204)
}

// spend is what paid providers cost today and this month, next to the limits.
func (s *Server) spend(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Runner.Config("")
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	now := time.Now()
	today, err := s.DB.Spent(r.Context(), time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	month, _ := s.DB.Spent(r.Context(), time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()))
	writeJSON(w, 200, map[string]any{"currency": "USD", "today": today, "month": month, "limits": cfg.Routing.Budget})
}
