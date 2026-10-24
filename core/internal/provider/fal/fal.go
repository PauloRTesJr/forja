// Package fal is a model hub provider: models hosted on fal.ai, called through its queue API
// with one API key and billed per generation (docs/decisions.md, decision 16).
//
// Models are endpoints the user added (the recommended ones come first). Each is described by
// its OpenAPI schema (see schema.go), so a new endpoint needs no code. Inputs travel inline as
// data URIs and the result is downloaded into the core's store, so runs share the store.
package fal

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"forja/internal/engineapi"
	"forja/internal/provider"
	"forja/internal/store"
)

// Vendor is the config value that selects this provider: kind = "api", vendor = "fal".
const Vendor = "fal"

// Recommended endpoints are added when fal.ai is connected and listed first; each was checked
// to map onto Forja's stages. Higher rank first.
var Recommended = []struct {
	ID   string
	Rank int
}{
	{"fal-ai/hunyuan-3d/v3.1/pro/image-to-3d", 30},
	{"fal-ai/trellis-2", 28},
	{"tripo3d/h3.1/image-to-3d", 26},
	{"meshy/v7.1/image-to-3d", 24},
	{"fal-ai/hyper3d/rodin/v2.5", 22},
	{"fal-ai/trellis-2/retexture", 20},
}

// APIBase and QueueBase are fal's endpoints; tests point them at a fake server.
var (
	APIBase   = "https://api.fal.ai"
	QueueBase = "https://queue.fal.run"
)

// RecommendedIDs lists the recommended endpoints, for a newly connected hub.
func RecommendedIDs() []string {
	out := make([]string, len(Recommended))
	for i, r := range Recommended {
		out[i] = r.ID
	}
	return out
}

func rankOf(id string) (int, bool) {
	for _, r := range Recommended {
		if r.ID == id {
			return r.Rank, true
		}
	}
	return 0, false
}

const (
	// fal returns at most this many models per page when schemas are included.
	pageSize = 10
	stale    = 10 * time.Minute
	maxMesh  = 2 << 30
)

type Provider struct {
	id      string
	key     string
	enabled []string
	st      *store.Store
	// API and Queue are fal's base URLs; tests point them at a fake.
	API, Queue string
	http       *http.Client

	mu      sync.Mutex
	entries map[string]*entry
	loaded  time.Time
	cancels map[string]string // run id -> the cancel URL of its current request
}

// entry is what Forja knows about one endpoint.
type entry struct {
	Spec
	Name, Description, Thumbnail, License string
	Price                                 *engineapi.Price
}

func New(id, key string, models []string, st *store.Store) *Provider {
	return &Provider{
		id: id, key: key, enabled: models, st: st, API: APIBase, Queue: QueueBase,
		http:    &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}},
		entries: map[string]*entry{}, cancels: map[string]string{},
	}
}

func (p *Provider) ID() string          { return p.id }
func (p *Provider) Kind() provider.Kind { return provider.KindAPI }
func (p *Provider) Paid() bool          { return true }

// SharesStore is true: runs read inputs from and write results to the core's store directly.
func (p *Provider) SharesStore() bool { return true }
func (p *Provider) HasArtifact(_ context.Context, ref string) (bool, error) {
	return p.st.Has(ref), nil
}
func (p *Provider) Upload(context.Context, string) error   { return nil }
func (p *Provider) Download(context.Context, string) error { return nil }

// call sends one request to fal's API or queue with the key. The key never goes anywhere else.
func (p *Provider) call(ctx context.Context, method, rawURL string, body any, out any) error {
	return p.request(ctx, method, rawURL, body, out, true)
}

// public reads fal's catalog, which needs no key. Sent with one, fal answered without the
// API descriptions Forja maps models from.
func (p *Provider) public(ctx context.Context, rawURL string, out any) error {
	return p.request(ctx, http.MethodGet, rawURL, nil, out, false)
}

func (p *Provider) request(ctx context.Context, method, rawURL string, body any, out any, withKey bool) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, r)
	if err != nil {
		return err
	}
	if withKey {
		req.Header.Set("Authorization", "Key "+p.key)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach fal.ai: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errors.New("fal.ai refused the API key")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("fal.ai answered %s: %s", resp.Status, problemOf(raw))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// problemOf pulls a readable message out of fal's error bodies.
func problemOf(raw []byte) string {
	var doc struct {
		Detail any `json:"detail"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &doc) == nil {
		if doc.Error.Message != "" {
			return doc.Error.Message
		}
		switch d := doc.Detail.(type) {
		case string:
			return d
		case []any:
			var msgs []string
			for _, item := range d {
				if m, ok := item.(map[string]any); ok {
					msgs = append(msgs, fmt.Sprint(m["loc"], ": ", m["msg"]))
				}
			}
			return strings.Join(msgs, "; ")
		}
	}
	return strings.TrimSpace(string(raw))
}

// Health checks the key with a call that costs nothing: a price lookup.
func (p *Provider) Health(ctx context.Context) error {
	probe := "fal-ai/trellis-2"
	if len(p.enabled) > 0 {
		probe = p.enabled[0]
	}
	return p.call(ctx, http.MethodGet, p.API+"/v1/models/pricing?endpoint_id="+url.QueryEscape(probe), nil, nil)
}

func (p *Provider) Info(context.Context) (*engineapi.Info, error) {
	return &engineapi.Info{Version: "fal.ai", APIVersion: engineapi.APIVersion, Stages: map[string]string{},
		System: map[string]any{"vendor": "fal.ai"}}, nil
}

type catalogModel struct {
	EndpointID string         `json:"endpoint_id"`
	Metadata   map[string]any `json:"metadata"`
	OpenAPI    map[string]any `json:"openapi"`
}

type catalogPage struct {
	Models     []catalogModel `json:"models"`
	NextCursor string         `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

func (p *Provider) newEntry(m catalogModel) *entry {
	str := func(k string) string { s, _ := m.Metadata[k].(string); return s }
	e := &entry{Name: str("display_name"), Description: str("description"), Thumbnail: str("thumbnail_url"),
		License: str("license_type")}
	if e.Name == "" {
		e.Name = m.EndpointID
	}
	if m.OpenAPI == nil || m.OpenAPI["error"] != nil {
		e.Spec = Spec{Endpoint: m.EndpointID, Problem: "fal.ai has no API description for it"}
	} else {
		e.Spec = MapSchema(m.EndpointID, m.OpenAPI)
	}
	return e
}

// describe fetches schemas and prices for endpoints, in pages fal allows.
func (p *Provider) describe(ctx context.Context, ids []string) (map[string]*entry, error) {
	out := map[string]*entry{}
	for start := 0; start < len(ids); start += pageSize {
		q := url.Values{"expand": {"openapi-3.0"}}
		for _, id := range ids[start:min(start+pageSize, len(ids))] {
			q.Add("endpoint_id", id)
		}
		var page catalogPage
		if err := p.public(ctx, p.API+"/v1/models?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		for _, m := range page.Models {
			out[m.EndpointID] = p.newEntry(m)
		}
	}
	p.prices(ctx, out)
	return out, nil
}

// prices fills in what each endpoint charges; a failed lookup leaves the price unknown.
func (p *Provider) prices(ctx context.Context, entries map[string]*entry) {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for start := 0; start < len(ids); start += 50 {
		q := url.Values{}
		for _, id := range ids[start:min(start+50, len(ids))] {
			q.Add("endpoint_id", id)
		}
		var page struct {
			Prices []struct {
				EndpointID string  `json:"endpoint_id"`
				UnitPrice  float64 `json:"unit_price"`
				Unit       string  `json:"unit"`
				Currency   string  `json:"currency"`
			} `json:"prices"`
		}
		if err := p.call(ctx, http.MethodGet, p.API+"/v1/models/pricing?"+q.Encode(), nil, &page); err != nil {
			continue
		}
		for _, pr := range page.Prices {
			if e := entries[pr.EndpointID]; e != nil {
				unit := pr.Unit
				if slices.Contains(perCallUnits, strings.ToLower(unit)) {
					unit = engineapi.PerCall
				}
				e.Price = &engineapi.Price{Amount: pr.UnitPrice, Unit: unit, Currency: pr.Currency}
			}
		}
	}
	// Many 3D endpoints are priced per compute unit, which says nothing of what one generation
	// costs; fal's estimate from past calls does.
	var wg sync.WaitGroup
	for id, e := range entries {
		if e.Price == nil || e.Price.Unit == engineapi.PerCall {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if pr := p.perCall(ctx, id); pr != nil {
				e.Price = pr
			}
		}()
	}
	wg.Wait()
}

// perCallUnits are fal's billing units that mean one generation of an image-to-3D model.
var perCallUnits = []string{"image", "images", "generation", "generations", "request", "requests", "call", "calls"}

// perCall is fal's estimate of one call to an endpoint, or nil when it has none.
func (p *Provider) perCall(ctx context.Context, endpoint string) *engineapi.Price {
	body := map[string]any{"estimate_type": "historical_api_price",
		"endpoints": map[string]any{endpoint: map[string]int{"call_quantity": 1}}}
	var est struct {
		TotalCost float64 `json:"total_cost"`
		Currency  string  `json:"currency"`
	}
	if err := p.call(ctx, http.MethodPost, p.API+"/v1/models/pricing/estimate", body, &est); err != nil || est.TotalCost <= 0 {
		return nil
	}
	return &engineapi.Price{Amount: est.TotalCost, Unit: engineapi.PerCall, Currency: est.Currency}
}

// load describes the enabled endpoints, at most every few minutes.
func (p *Provider) load(ctx context.Context) error {
	p.mu.Lock()
	missing := false
	for _, id := range p.enabled {
		missing = missing || p.entries[id] == nil
	}
	fresh := !missing && time.Since(p.loaded) < stale
	p.mu.Unlock()
	if fresh {
		return nil
	}
	got, err := p.describe(ctx, p.enabled)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range got {
		p.entries[id] = e
	}
	p.loaded = time.Now()
	return nil
}

var hosted = "fal.ai"

func (p *Provider) Models(ctx context.Context) ([]engineapi.Model, error) {
	if err := p.load(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]engineapi.Model, 0, len(p.enabled))
	for _, id := range p.enabled {
		e := p.entries[id]
		if e == nil {
			continue // fal no longer lists it
		}
		out = append(out, e.model())
	}
	return out, nil
}

func (e *entry) model() engineapi.Model {
	rank, recommended := rankOf(e.Endpoint)
	m := engineapi.Model{
		ID: e.Endpoint, Name: e.Name, Family: "fal.ai", Tasks: e.Tasks, Description: e.Description,
		License: engineapi.License{SPDX: "fal.ai " + orDefault(e.License, "terms"), Commercial: e.License == "commercial",
			Restrictions: []string{}, URL: "https://fal.ai/models/" + e.Endpoint},
		Params: map[string]any{}, Requires: map[string]any{},
		Status: "ready", Device: &hosted, RunsOn: &hosted, Fit: "hosted", Rank: rank, Recommended: recommended,
		Price: e.Price, Thumbnail: e.Thumbnail, AdapterVersion: "1",
		// The endpoint id names the model version, so it keys cached results like a weight revision.
		Weights:       engineapi.Weights{Repo: "fal.ai", Revision: e.Endpoint},
		ViewsRequired: e.ViewsRequired,
	}
	if e.ImageList || len(e.Views) > 0 {
		m.Views = []string{"front"}
		for _, v := range engineapi.Views[1:] {
			if e.ImageList || e.Views[v] != "" {
				m.Views = append(m.Views, v)
			}
		}
	}
	if e.Problem != "" {
		m.Status, m.Device, m.Tasks = "unsupported", nil, []string{}
		m.Description = "Forja cannot use it: " + e.Problem
	}
	return m
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Search lists fal's image-to-3D catalog, as Forja could use each model.
func (p *Provider) Search(ctx context.Context, query, cursor string) (*engineapi.CatalogPage, error) {
	q := url.Values{"category": {"image-to-3d"}, "status": {"active"}, "limit": {strconv.Itoa(pageSize)},
		"expand": {"openapi-3.0"}}
	if query != "" {
		q.Set("q", query)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var page catalogPage
	if err := p.public(ctx, p.API+"/v1/models?"+q.Encode(), &page); err != nil {
		return nil, err
	}
	entries := map[string]*entry{}
	for _, m := range page.Models {
		entries[m.EndpointID] = p.newEntry(m)
	}
	p.prices(ctx, entries)
	out := &engineapi.CatalogPage{Models: []engineapi.CatalogModel{}}
	if page.HasMore {
		out.NextCursor = page.NextCursor
	}
	for _, m := range page.Models {
		e := entries[m.EndpointID]
		model := e.model()
		_, recommended := rankOf(m.EndpointID)
		out.Models = append(out.Models, engineapi.CatalogModel{
			ID: m.EndpointID, Name: e.Name, Description: e.Description, Thumbnail: e.Thumbnail, License: model.License,
			Price: e.Price, Tasks: model.Tasks, Problem: e.Problem, Added: slices.Contains(p.enabled, m.EndpointID),
			Recommended: recommended,
		})
	}
	return out, nil
}

// Check describes one endpoint before it is added, so a model Forja cannot call is refused.
func (p *Provider) Check(ctx context.Context, id string) (engineapi.CatalogModel, error) {
	got, err := p.describe(ctx, []string{id})
	if err != nil {
		return engineapi.CatalogModel{}, err
	}
	e := got[id]
	if e == nil {
		return engineapi.CatalogModel{}, fmt.Errorf("fal.ai has no model %s", id)
	}
	m := e.model()
	return engineapi.CatalogModel{ID: id, Name: e.Name, Description: e.Description, License: m.License, Price: e.Price,
		Tasks: m.Tasks, Problem: e.Problem}, nil
}

// Thumbnail fetches a catalog picture, without the key: they are public files on fal's CDN.
func (p *Provider) Thumbnail(ctx context.Context, raw string) (io.ReadCloser, string, error) {
	u, err := url.Parse(raw)
	if err != nil || !thumbnailHost(u) {
		return nil, "", fmt.Errorf("not a fal.ai picture: %q", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	kind := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 400 || !strings.HasPrefix(kind, "image/") {
		resp.Body.Close()
		return nil, "", fmt.Errorf("fal.ai picture: %s, %s", resp.Status, kind)
	}
	return resp.Body, kind, nil
}

// thumbnailHost is true for the hosts fal serves catalog pictures from. Cleaned paths only,
// so a "/falserverless/../" path cannot reach another bucket.
func thumbnailHost(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || path.Clean(u.Path) != u.Path {
		return false
	}
	h := u.Hostname()
	return h == "fal.media" || strings.HasSuffix(h, ".fal.media") ||
		(h == "storage.googleapis.com" && strings.HasPrefix(u.Path, "/falserverless/"))
}

// Run calls each stage's endpoint in turn: inputs go inline, the mesh comes back into the store.
func (p *Provider) Run(ctx context.Context, req engineapi.RunRequest, events chan<- engineapi.Event) (*engineapi.Run, error) {
	if err := p.load(ctx); err != nil {
		return nil, err
	}
	run := &engineapi.Run{ID: "fal-" + randomID(), Status: "succeeded", Stages: map[string]engineapi.StageState{}}
	produced := map[string]string{}
	for _, s := range req.Stages {
		start := time.Now()
		emit(ctx, events, "stage.started", map[string]any{"stage": s.ID})
		ref, err := p.runStage(ctx, run.ID, req.Seed, s, produced, events)
		secs := time.Since(start).Seconds()
		if err != nil {
			msg := err.Error()
			status := "failed"
			if ctx.Err() != nil {
				status, msg = "cancelled", "cancelled"
			}
			run.Status, run.Error = status, &msg
			run.Stages[s.ID] = engineapi.StageState{Status: status, Error: &msg, Seconds: &secs}
			emit(ctx, events, "stage.failed", map[string]any{"stage": s.ID, "error": msg})
			break
		}
		produced[s.ID] = ref
		out := engineapi.Output{Hash: ref, Kind: "mesh/glb", Preview: true}
		run.Stages[s.ID] = engineapi.StageState{Status: "succeeded", Outputs: []engineapi.Output{out}, Seconds: &secs}
		emit(ctx, events, "artifact.ready", map[string]any{"stage": s.ID, "hash": ref, "kind": out.Kind, "preview": true})
		emit(ctx, events, "stage.finished", map[string]any{"stage": s.ID, "seconds": secs})
	}
	return run, nil
}

func emit(ctx context.Context, events chan<- engineapi.Event, typ string, data map[string]any) {
	select {
	case events <- engineapi.Event{Type: typ, Data: data}:
	case <-ctx.Done():
	}
}

func (p *Provider) runStage(ctx context.Context, runID string, seed int, s engineapi.StageSpec,
	produced map[string]string, events chan<- engineapi.Event,
) (string, error) {
	p.mu.Lock()
	e := p.entries[s.Model]
	p.mu.Unlock()
	if e == nil {
		return "", fmt.Errorf("%s is not one of the fal.ai models added to Forja", s.Model)
	}
	if e.Problem != "" {
		return "", fmt.Errorf("Forja cannot call %s: %s", e.Name, e.Problem)
	}
	body := map[string]any{}
	for k, v := range e.Fixed {
		body[k] = v
	}
	if e.Seed != "" {
		body[e.Seed] = seed
	}
	var list []string // the front first, then the other views, for an endpoint taking a list
	for i, in := range s.Inputs {
		if id, ok := strings.CutPrefix(in, "stage:"); ok {
			in = produced[id]
		}
		uri, kind, err := p.dataURI(in)
		if err != nil {
			return "", err
		}
		view := "front"
		if i < len(s.Views) && s.Views[i] != "" {
			view = s.Views[i]
		}
		switch {
		case kind == "image" && e.ImageList && view == "front":
			list = append([]string{uri}, list...)
		case kind == "image" && e.ImageList:
			list = append(list, uri)
		case kind == "image" && view == "front":
			body[e.Image] = uri
		case kind == "image" && e.Views[view] != "":
			body[e.Views[view]] = uri
		case kind == "mesh" && e.Mesh != "":
			body[e.Mesh] = uri
		}
	}
	if e.ImageList && len(list) > 0 {
		body[e.Image] = list
	}
	if body[e.Image] == nil {
		return "", fmt.Errorf("stage %s has no image for %s", s.ID, e.Name)
	}
	for _, v := range e.ViewsRequired {
		if body[e.Views[v]] == nil {
			return "", fmt.Errorf("%s needs the %s view; add it to the job", e.Name, v)
		}
	}
	if e.Mesh != "" && body[e.Mesh] == nil {
		return "", fmt.Errorf("stage %s has no mesh for %s", s.ID, e.Name)
	}

	var sub struct {
		RequestID   string `json:"request_id"`
		StatusURL   string `json:"status_url"`
		ResponseURL string `json:"response_url"`
		CancelURL   string `json:"cancel_url"`
	}
	if err := p.call(ctx, http.MethodPost, p.Queue+"/"+e.Endpoint, body, &sub); err != nil {
		return "", err
	}
	p.mu.Lock()
	p.cancels[runID] = sub.CancelURL
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.cancels, runID)
		p.mu.Unlock()
	}()
	if err := p.wait(ctx, sub.StatusURL, s.ID, events); err != nil {
		if ctx.Err() != nil {
			p.cancel(sub.CancelURL)
		}
		return "", err
	}
	var result map[string]any
	if err := p.call(ctx, http.MethodGet, sub.ResponseURL, nil, &result); err != nil {
		return "", err
	}
	fileURL, _ := dig(result, append(strings.Split(e.Output, "."), "url")...).(string)
	if fileURL == "" {
		return "", fmt.Errorf("%s returned no mesh in %s", e.Name, e.Output)
	}
	return p.fetchMesh(ctx, fileURL, e.Name)
}

type queueStatus struct {
	Status        string `json:"status"`
	QueuePosition *int   `json:"queue_position"`
	Error         string `json:"error"`
	Logs          []struct {
		Message string `json:"message"`
	} `json:"logs"`
}

// wait follows a request until it completes: fal streams its status, and polling takes over
// if the stream drops.
func (p *Provider) wait(ctx context.Context, statusURL, stage string, events chan<- engineapi.Event) error {
	done, err := p.stream(ctx, statusURL, stage, events)
	if done || ctx.Err() != nil {
		return err
	}
	for delay := time.Second; ; delay = min(delay*2, 8*time.Second) {
		var st queueStatus
		if err := p.call(ctx, http.MethodGet, statusURL+"?logs=1", nil, &st); err != nil {
			return err
		}
		if st.Status == "COMPLETED" {
			return completed(st)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

func completed(st queueStatus) error {
	if st.Error != "" {
		return errors.New(st.Error)
	}
	return nil
}

func (p *Provider) stream(ctx context.Context, statusURL, stage string, events chan<- engineapi.Event) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL+"/stream?logs=1", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Key "+p.key)
	resp, err := p.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("status stream: %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var st queueStatus
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &st) != nil {
			continue
		}
		if st.Status == "COMPLETED" {
			return true, completed(st)
		}
		if st.Status == "IN_QUEUE" && st.QueuePosition != nil {
			emit(ctx, events, "stage.log", map[string]any{"stage": stage, "message": fmt.Sprintf("in fal.ai's queue, position %d", *st.QueuePosition)})
		}
	}
	return false, sc.Err()
}

func (p *Provider) cancel(cancelURL string) {
	if cancelURL == "" {
		return
	}
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = p.call(ctx, http.MethodPut, cancelURL, nil, nil)
}

func (p *Provider) Cancel(_ context.Context, runID string) error {
	p.mu.Lock()
	u := p.cancels[runID]
	p.mu.Unlock()
	p.cancel(u)
	return nil
}

// dataURI inlines an artifact for a request, telling an image from a mesh by its bytes.
func (p *Provider) dataURI(ref string) (string, string, error) {
	f, err := p.st.Open(ref)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", "", err
	}
	kind, mime := sniff(raw)
	if kind == "" {
		return "", "", fmt.Errorf("input %s is neither an image nor a GLB", ref)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), kind, nil
}

func sniff(raw []byte) (kind, mime string) {
	switch {
	case bytes.HasPrefix(raw, []byte("\x89PNG")):
		return "image", "image/png"
	case bytes.HasPrefix(raw, []byte{0xFF, 0xD8, 0xFF}):
		return "image", "image/jpeg"
	case len(raw) > 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "image", "image/webp"
	case bytes.HasPrefix(raw, []byte("glTF")):
		return "mesh", "model/gltf-binary"
	}
	return "", ""
}

// fetchMesh downloads a result file into the store. It is a public link: the key stays home.
func (p *Provider) fetchMesh(ctx context.Context, fileURL, model string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading the mesh from %s: %w", model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("downloading the mesh from %s: %s", model, resp.Status)
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(resp.Body, head)
	if string(head[:n]) != "glTF" {
		return "", fmt.Errorf("%s returned a file that is not a GLB", model)
	}
	return p.st.Put(io.LimitReader(io.MultiReader(bytes.NewReader(head[:n]), resp.Body), maxMesh))
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var _ provider.Provider = (*Provider)(nil)
