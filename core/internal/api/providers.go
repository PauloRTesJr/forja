package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"forja/internal/config"
	"forja/internal/jobs"
	"forja/internal/provider"
	"forja/internal/provider/engine"
	"forja/internal/providers"
	"forja/internal/secrets"
	"forja/internal/tlscert"
)

// EvProvidersChanged tells clients that providers or sharing changed.
const EvProvidersChanged = "providers.changed"

type keyView struct {
	Stored bool   `json:"stored"`
	Hint   string `json:"hint,omitempty"`
	// Store names where the key lives, such as "Windows Credential Manager".
	Store string `json:"store"`
}

type providerRow struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Kind        string   `json:"kind"`
	URL         string   `json:"url,omitempty"`
	Paid        bool     `json:"paid"`
	Healthy     bool     `json:"healthy"`
	Latency     int64    `json:"latency_ms"`
	Error       string   `json:"error,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Key         *keyView `json:"key,omitempty"`
	// Editable is false for providers set in a system or project file, which the app leaves alone.
	Editable bool `json:"editable"`
}

func (s *Server) providers(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Runner.Config("")
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	byID := map[string]config.Provider{}
	for _, pc := range cfg.Providers {
		byID[pc.ID] = pc
	}
	src := cfg.Sources["providers"]
	all := s.Providers.All()
	rows := make([]providerRow, len(all))
	var wg sync.WaitGroup
	for i, p := range all {
		pc := byID[p.ID()]
		row := providerRow{ID: p.ID(), Label: pc.Label, Kind: string(p.Kind()), URL: pc.URL, Paid: p.Paid(),
			Fingerprint: tlscert.Short(pc.Fingerprint), Editable: p.ID() != jobs.LocalProvider && (src == "" || src == "user")}
		if row.Label == "" {
			row.Label = p.ID()
		}
		if pc.Secret != "" {
			hint, ok := secrets.Hint(pc.Secret)
			row.Key = &keyView{Stored: ok, Hint: hint, Store: secrets.Store()}
		}
		rows[i] = row
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Asking about the local engine must not start it.
			if p.Kind() == provider.KindLocal && !s.Engine.Running() {
				rows[i].Error = "idle"
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			err := p.Health(ctx)
			rows[i].Latency = time.Since(start).Milliseconds()
			rows[i].Healthy = err == nil
			if err != nil {
				rows[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	writeJSON(w, 200, rows)
}

func (s *Server) providerKinds(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, providers.Kinds())
}

type probeView struct {
	URL         string `json:"url"`
	Version     string `json:"version"`
	GPU         string `json:"gpu,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Latency     int64  `json:"latency_ms"`
}

func gpuName(system map[string]any) string {
	devices, _ := system["devices"].([]any)
	for _, d := range devices {
		dev, _ := d.(map[string]any)
		if dev["backend"] != "cpu" {
			name, _ := dev["name"].(string)
			return name
		}
	}
	return ""
}

func probeOf(u string, p *engine.Probe) probeView {
	return probeView{URL: u, Version: p.Info.Version, GPU: gpuName(p.Info.System), Fingerprint: tlscert.Short(p.Fingerprint),
		Latency: p.Latency.Milliseconds()}
}

// providerTest tries an address and token before anything is saved.
func (s *Server) providerTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	u, err := engine.NormalizeURL(body.URL)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	probe, err := engine.Test(r.Context(), u, body.Token, "")
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, probeOf(u, probe))
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// newProviderID makes an id from a label that no provider has yet.
func (s *Server) newProviderID(label string) string {
	base := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	if len(base) > 32 {
		base = strings.Trim(base[:32], "-")
	}
	if base == "" || base == jobs.LocalProvider {
		base = "engine"
	}
	id := base
	for n := 2; ; n++ {
		if _, taken := s.Providers.Get(id); !taken {
			return id
		}
		id = base + "-" + strconv.Itoa(n)
	}
}

// providerAdd adds another computer: with the pairing code it shows, or with its token.
// The token goes to the keychain and the provider to the user's config.toml.
func (s *Server) providerAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string `json:"kind"`
		Vendor string `json:"vendor"`
		Label  string `json:"label"`
		URL    string `json:"url"`
		Token  string `json:"token"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if body.Kind == string(provider.KindAPI) {
		s.hubAdd(w, r, body.Vendor, strings.TrimSpace(body.Token))
		return
	}
	u, err := engine.NormalizeURL(body.URL)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token, fingerprint, name := strings.TrimSpace(body.Token), "", ""
	switch {
	case body.Code != "":
		paired, err := engine.Pair(ctx, u, body.Code)
		if err != nil {
			problem(w, 422, err.Error())
			return
		}
		token, fingerprint, name = paired.Token, paired.Fingerprint, paired.Name
	case token == "":
		problem(w, 422, "enter the pairing code the other computer shows, or its access token")
		return
	}
	probe, err := engine.Test(ctx, u, token, fingerprint)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	if fingerprint == "" {
		fingerprint = probe.Fingerprint
	}
	label := strings.TrimSpace(body.Label)
	if label == "" {
		label = name
	}
	if label == "" {
		if parsed, err := url.Parse(u); err == nil {
			label, _, _ = net.SplitHostPort(parsed.Host)
		}
	}
	id := s.newProviderID(label)
	pc := config.Provider{ID: id, Label: label, Kind: string(provider.KindEngine), URL: u,
		Secret: providers.SecretName(id), Fingerprint: fingerprint}
	if err := secrets.Set(pc.Secret, token); err != nil {
		problem(w, 500, "could not store the token in "+secrets.Store()+": "+err.Error())
		return
	}
	validate := func() error { _, err := config.Load("", nil); return err }
	if err := config.AddProvider(config.UserFile(), pc, validate); err != nil {
		_ = secrets.Delete(pc.Secret)
		problem(w, 422, err.Error())
		return
	}
	// After this computer: stages use it for models this computer lacks (decision 12).
	if cfg, err := config.Load("", nil); err == nil && !slices.Contains(cfg.Routing.Priority, id) {
		priority := append(slices.Clone(cfg.Routing.Priority), id)
		if err := config.Edit(config.UserFile(), map[string]any{"routing.priority": anySlice(priority)}, validate); err != nil {
			s.Log.Warn("could not add the provider to routing.priority", "provider", id, "err", err)
		}
	}
	p, err := providers.Build(pc, s.Store)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.Providers.Add(p)
	go func() { _, _ = s.Catalog.Ask(context.Background(), id) }()
	s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": id})
	writeJSON(w, 201, map[string]any{"id": id, "label": label, "probe": probeOf(u, probe)})
}

func anySlice(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func (s *Server) userProvider(w http.ResponseWriter, id string) (config.Provider, bool) {
	cfg, err := s.Runner.Config("")
	if err != nil {
		problem(w, 422, err.Error())
		return config.Provider{}, false
	}
	for _, pc := range cfg.Providers {
		if pc.ID == id {
			if src := cfg.Sources["providers"]; src != "" && src != "user" {
				problem(w, 409, fmt.Sprintf("provider %s is set in the %s configuration; change it there", id, src))
				return config.Provider{}, false
			}
			return pc, true
		}
	}
	problem(w, 404, "no provider "+id)
	return config.Provider{}, false
}

// providerRemove forgets a provider: its config, its key, and routes that named it.
func (s *Server) providerRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pc, ok := s.userProvider(w, id)
	if !ok {
		return
	}
	validate := func() error { _, err := config.Load("", nil); return err }
	cfg, _ := config.Load("", nil)
	changes := map[string]any{}
	if cfg != nil && slices.Contains(cfg.Routing.Priority, id) {
		changes["routing.priority"] = anySlice(slices.DeleteFunc(slices.Clone(cfg.Routing.Priority), func(x string) bool { return x == id }))
	}
	if cfg != nil {
		for use, target := range cfg.Routing.Stages {
			if target == id && !strings.Contains(use, ".") {
				changes["routing.stages."+use] = nil
			}
		}
	}
	if len(changes) > 0 {
		if err := config.Edit(config.UserFile(), changes, nil); err != nil {
			problem(w, 422, err.Error())
			return
		}
	}
	if err := config.RemoveProvider(config.UserFile(), id, validate); err != nil {
		problem(w, 422, err.Error())
		return
	}
	if pc.Secret != "" {
		_ = secrets.Delete(pc.Secret)
	}
	s.Providers.Remove(id)
	s.Catalog.Forget(id)
	s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": id})
	w.WriteHeader(204)
}

// providerToken replaces a provider's key, after checking that the new one works.
func (s *Server) providerToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	pc, ok := s.userProvider(w, id)
	if !ok {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		problem(w, 400, "send the new token")
		return
	}
	token := strings.TrimSpace(body.Token)
	if _, err := engine.Test(r.Context(), pc.URL, token, pc.Fingerprint); err != nil {
		problem(w, 422, err.Error())
		return
	}
	if pc.Secret == "" {
		pc.Secret = providers.SecretName(id)
	}
	if err := secrets.Set(pc.Secret, token); err != nil {
		problem(w, 500, err.Error())
		return
	}
	p, err := providers.Build(pc, s.Store)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.Providers.Add(p)
	s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": id})
	w.WriteHeader(204)
}

type shareView struct {
	// Enabled is the setting; Active says the running engine serves the network now.
	Enabled     bool     `json:"enabled"`
	Active      bool     `json:"active"`
	Port        int64    `json:"port"`
	Addresses   []string `json:"addresses"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	// Pending is "restart" when the change waits for running jobs to end.
	Pending string `json:"pending,omitempty"`
}

func (s *Server) share() (*shareView, error) {
	cfg, err := config.Load("", nil)
	if err != nil {
		return nil, err
	}
	sh := s.Engine.Sharing()
	view := &shareView{Enabled: cfg.Engine.Share, Active: sh.Port != 0, Port: cfg.Engine.SharePort, Addresses: []string{},
		Fingerprint: tlscert.Short(sh.Fingerprint)}
	for _, ip := range tlscert.Addresses() {
		if ip.To4() != nil {
			view.Addresses = append(view.Addresses, net.JoinHostPort(ip.String(), strconv.FormatInt(view.Port, 10)))
		}
	}
	return view, nil
}

func (s *Server) shareGet(w http.ResponseWriter, _ *http.Request) {
	view, err := s.share()
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, view)
}

// sharePut turns sharing on or off: the setting, then the engine restarts to match.
func (s *Server) sharePut(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	err := config.Edit(config.UserFile(), map[string]any{"engine.share": body.Enabled}, func() error {
		_, err := config.Load("", nil)
		return err
	})
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	pending := ""
	if s.applyEngineSettings() == "next-start" && s.Engine.Running() {
		pending = "restart"
	} else if body.Enabled {
		// A shared GPU must answer even when nobody here is generating.
		go func() { _, _, _ = s.Engine.Endpoint(context.Background()) }()
	}
	view, err := s.share()
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	view.Pending = pending
	s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": jobs.LocalProvider})
	writeJSON(w, 200, view)
}

// shareCode shows a six-digit code another computer trades, once, for this engine's token.
func (s *Server) shareCode(w http.ResponseWriter, r *http.Request) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	code := fmt.Sprintf("%06d", n.Int64())
	const ttl = 10 * time.Minute
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.Engine.Pairing(ctx, code, ttl); err != nil {
		problem(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"code": code[:3] + " " + code[3:], "expires_at": time.Now().Add(ttl)})
}
