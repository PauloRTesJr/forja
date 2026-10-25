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
	"net"
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
	// callTimeout bounds one request to fal's API or queue; a submission carries its images inline.
	callTimeout = 2 * time.Minute
	// downloadTimeout bounds one download of a result file.
	downloadTimeout = 10 * time.Minute
	// failures is how many transient errors in a row one request survives.
	failures = 5
)

// Waits that tests shorten: the first pause before a retry (it doubles), the longest pause
// between polls, how long the status stream may stay silent before polling takes over, and
// how long one generation may take, queue included, before it is cancelled.
var (
	retryBase  = time.Second
	pollMax    = 8 * time.Second
	streamIdle = 45 * time.Second
	maxWait    = 30 * time.Minute
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
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &httpError{resp.StatusCode, "fal.ai refused the API key"}
	}
	if resp.StatusCode >= 400 {
		return &httpError{resp.StatusCode, fmt.Sprintf("fal.ai answered %s: %s", resp.Status, problemOf(raw))}
	}
	if err != nil {
		return fmt.Errorf("reading fal.ai's answer: %w", err)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// httpError is fal.ai answering with an error status.
type httpError struct {
	Status int
	Msg    string
}

func (e *httpError) Error() string { return e.Msg }

// StatusCode tells the job runner fal.ai answered: only a 5xx or no answer at all reads as
// fal.ai down, which may move a stage to another provider.
func (e *httpError) StatusCode() int { return e.Status }

// permanent marks an error that trying again cannot fix, such as a file that is not a GLB.
type permanent struct{ error }

func (e permanent) Unwrap() error { return e.error }

// transient reports an error worth trying again: no answer, a rate limit or a server error.
// A timeout of one request is transient; the end of ctx is not.
func transient(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var pe permanent
	if errors.As(err, &pe) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status >= 500
	}
	return !errors.Is(err, context.Canceled)
}

// unsent reports a submission fal.ai certainly did not take: refused for load, or never sent.
// Any other failure may have queued a generation that is billed, so it is not tried again.
func unsent(err error) bool {
	var he *httpError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status == http.StatusServiceUnavailable
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// again runs fn until it succeeds, fails for good, or fails `failures` times in a row, waiting
// longer after each failure.
func again(ctx context.Context, retryable func(error) bool, fn func() error) error {
	delay := retryBase
	for n := 1; ; n++ {
		err := fn()
		if err == nil || n >= failures || !retryable(err) || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, pollMax)
	}
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
		// The endpoint id names the model version, as a weight revision does.
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
	submit := func() error { return p.call(ctx, http.MethodPost, p.Queue+"/"+e.Endpoint, body, &sub) }
	if err := again(ctx, unsent, submit); err != nil {
		return "", err
	}
	if err := p.ours(sub.StatusURL, sub.ResponseURL, sub.CancelURL); err != nil {
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
	g := &progress{events: events, stage: s.ID}
	if err := p.wait(ctx, sub.StatusURL, g); err != nil {
		if !g.done {
			// It may still be queued or running, whatever stopped Forja following it.
			p.cancel(sub.CancelURL)
		}
		// fal bills a request once its model takes it, and cancels only one still in its queue.
		if g.started {
			billed(events, s.ID)
		}
		return "", err
	}
	// From here the generation is paid for, whatever becomes of its result.
	billed(events, s.ID)
	var result map[string]any
	fetch := func() error { return p.call(ctx, http.MethodGet, sub.ResponseURL, nil, &result) }
	if err := again(ctx, func(err error) bool { return transient(ctx, err) }, fetch); err != nil {
		return "", err
	}
	fileURL, _ := dig(result, append(strings.Split(e.Output, "."), "url")...).(string)
	if fileURL == "" {
		return "", fmt.Errorf("%s returned no mesh in %s", e.Name, e.Output)
	}
	return p.fetchMesh(ctx, fileURL, e.Name)
}

// billed tells the job runner that fal.ai charged for a stage, even one that then failed or was
// cancelled. It is sent whatever the context: the runner reads every event until Run returns.
func billed(events chan<- engineapi.Event, stage string) {
	select {
	case events <- engineapi.Event{Type: "stage.billed", Data: map[string]any{"stage": stage}}:
	case <-time.After(10 * time.Second):
	}
}

// ours checks that the addresses fal.ai answered a submission with are on its queue's host:
// they get the API key.
func (p *Provider) ours(status, response, cancel string) error {
	base, err := url.Parse(p.Queue)
	if err != nil {
		return err
	}
	for i, raw := range []string{status, response, cancel} {
		if raw == "" && i == 2 {
			continue // without one the request cannot be cancelled, which only costs money
		}
		u, err := url.Parse(raw)
		if err != nil || raw == "" || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
			return fmt.Errorf("fal.ai answered the submission with an unexpected address %q", raw)
		}
	}
	return nil
}

var errTooLong = errors.New("fal.ai did not finish the generation in time")

type queueStatus struct {
	Status        string `json:"status"`
	QueuePosition *int   `json:"queue_position"`
	Error         string `json:"error"`
	Logs          []struct {
		Message string `json:"message"`
	} `json:"logs"`
}

// progress turns a request's status into what the job shows, once per change.
type progress struct {
	events        chan<- engineapi.Event
	stage         string
	last          string
	started, done bool
}

func (g *progress) saw(ctx context.Context, st queueStatus) {
	msg := ""
	switch st.Status {
	case "IN_QUEUE":
		msg = "Waiting in fal.ai's queue"
		if st.QueuePosition != nil {
			msg = fmt.Sprintf("Waiting in fal.ai's queue, position %d", *st.QueuePosition+1)
		}
	case "IN_PROGRESS", "COMPLETED":
		g.started, g.done = true, st.Status == "COMPLETED"
		msg = "Running on fal.ai"
	}
	if msg != "" && msg != g.last {
		g.last = msg
		emit(ctx, g.events, "stage.log", map[string]any{"stage": g.stage, "message": msg})
	}
}

// wait follows a request until it completes. fal streams its status; polling takes over when
// the stream drops or goes quiet, and rides out a few failed polls in a row. g says what fal
// showed of it: whether the model began on it (fal bills it from then, even when it is
// cancelled) and whether fal finished it, well or not.
func (p *Provider) wait(ctx context.Context, statusURL string, g *progress) error {
	ctx, cancel := context.WithTimeoutCause(ctx, maxWait, errTooLong)
	defer cancel()
	if done, err := p.stream(ctx, statusURL, g); done {
		return err
	}
	misses := 0
	for delay := retryBase; ; delay = min(delay*2, pollMax) {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		var st queueStatus
		err := p.call(ctx, http.MethodGet, statusURL+"?logs=1", nil, &st)
		switch {
		case err == nil:
			misses = 0
			g.saw(ctx, st)
			if st.Status == "COMPLETED" {
				return completed(st)
			}
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case transient(ctx, err) && misses+1 < failures:
			misses++
		default:
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
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

// stream reads the status stream until the request completes (done), the stream ends, or it
// stays silent for streamIdle: a connection that hangs would otherwise hold the job forever.
func (p *Provider) stream(ctx context.Context, statusURL string, g *progress) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL+"/stream?logs=1", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Key "+p.key)
	idle := time.AfterFunc(streamIdle, cancel)
	defer idle.Stop()
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
		idle.Reset(streamIdle)
		line, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var st queueStatus
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &st) != nil {
			continue
		}
		g.saw(ctx, st)
		if st.Status == "COMPLETED" {
			return true, completed(st)
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

// fetchMesh downloads a result file into the store, trying again when the transfer fails: the
// generation is paid for by now. It is a public link, so the key stays home.
func (p *Provider) fetchMesh(ctx context.Context, fileURL, model string) (string, error) {
	var ref string
	err := again(ctx, func(err error) bool { return transient(ctx, err) }, func() error {
		var err error
		ref, err = p.download(ctx, fileURL, model)
		return err
	})
	return ref, err
}

func (p *Provider) download(ctx context.Context, fileURL, model string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", permanent{err}
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading the mesh from %s: %w", model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &httpError{resp.StatusCode, fmt.Sprintf("downloading the mesh from %s: %s", model, resp.Status)}
	}
	head := make([]byte, 4)
	n, err := io.ReadFull(resp.Body, head)
	if err != nil && n < len(head) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("downloading the mesh from %s: %w", model, err)
	}
	if string(head[:n]) != "glTF" {
		return "", permanent{fmt.Errorf("%s returned a file that is not a GLB", model)}
	}
	return p.st.Put(&capped{r: io.MultiReader(bytes.NewReader(head[:n]), resp.Body), left: maxMesh, model: model})
}

// capped fails a download that grows past maxMesh, rather than storing a cut-off mesh.
type capped struct {
	r     io.Reader
	left  int64
	model string
}

func (c *capped) Read(b []byte) (int, error) {
	if c.left <= 0 {
		if n, err := c.r.Read(make([]byte, 1)); n == 0 && err != nil {
			return 0, err
		}
		return 0, permanent{fmt.Errorf("%s returned a mesh over %d GB", c.model, maxMesh>>30)}
	}
	if int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.r.Read(b)
	c.left -= int64(n)
	return n, err
}

func randomID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var _ provider.Provider = (*Provider)(nil)
