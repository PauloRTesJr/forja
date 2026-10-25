// Package engine is the provider for anything that speaks the engine's /v1 API: the local
// engine, a LAN box, a rented pod.
package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"forja/internal/engineapi"
	"forja/internal/provider"
	"forja/internal/store"
)

// Endpoint resolves where the engine is right now; the local engine starts lazily.
type Endpoint func(ctx context.Context) (baseURL, token string, err error)

type Provider struct {
	id       string
	kind     provider.Kind
	paid     bool
	price    float64
	shared   bool
	endpoint Endpoint
	store    *store.Store
	http     *http.Client
}

func New(id string, kind provider.Kind, paid, sharesStore bool, ep Endpoint, st *store.Store) *Provider {
	return &Provider{
		id: id, kind: kind, paid: paid, shared: sharesStore, endpoint: ep, store: st,
		// No overall timeout: event streams live as long as the run.
		http: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second}},
	}
}

// Static returns an Endpoint for a fixed URL.
func Static(url, token string) Endpoint {
	return func(context.Context) (string, string, error) { return strings.TrimRight(url, "/"), token, nil }
}

func (p *Provider) ID() string          { return p.id }
func (p *Provider) Kind() provider.Kind { return p.kind }
func (p *Provider) Paid() bool          { return p.paid }
func (p *Provider) SharesStore() bool   { return p.shared }

type ProblemError struct {
	Status int
	Title  string
	Detail string
}

func (e *ProblemError) Error() string {
	return fmt.Sprintf("engine: %s (%d): %s", e.Title, e.Status, e.Detail)
}

// StatusCode and ProblemDetail let the core pass an engine's refusal on to its own clients.
func (e *ProblemError) StatusCode() int       { return e.Status }
func (e *ProblemError) ProblemDetail() string { return e.Detail }

func (p *Provider) do(ctx context.Context, method, path string, body io.Reader, out any) (*http.Response, error) {
	base, token, err := p.endpoint(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil && method != http.MethodPut {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var prob struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(raw, &prob) != nil || prob.Detail == "" {
			prob.Detail = string(raw)
		}
		return resp, &ProblemError{resp.StatusCode, prob.Title, prob.Detail}
	}
	if out != nil {
		defer resp.Body.Close()
		return resp, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp, nil
}

func (p *Provider) Health(ctx context.Context) error {
	resp, err := p.do(ctx, http.MethodGet, "/v1/health", nil, nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (p *Provider) Info(ctx context.Context) (*engineapi.Info, error) {
	var info engineapi.Info
	if _, err := p.do(ctx, http.MethodGet, "/v1/info", nil, &info); err != nil {
		return nil, err
	}
	return &info, engineapi.CheckCompatible(info.APIVersion)
}

func (p *Provider) Models(ctx context.Context) ([]engineapi.Model, error) {
	var models []engineapi.Model
	_, err := p.do(ctx, http.MethodGet, "/v1/models", nil, &models)
	if p.price > 0 {
		for i := range models {
			models[i].Price = &engineapi.Price{Amount: p.price, Unit: "second", Currency: "USD"}
		}
	}
	return models, err
}

// Price sets what the engine's machine costs per second (a rented pod, a Modal GPU), which its
// models then carry.
func (p *Provider) Price(usdPerSecond float64) { p.price = usdPerSecond }

func (p *Provider) HasArtifact(ctx context.Context, ref string) (bool, error) {
	resp, err := p.do(ctx, http.MethodHead, "/v1/artifacts/"+ref, nil, nil)
	var prob *ProblemError
	if errors.As(err, &prob) && prob.Status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

func (p *Provider) Upload(ctx context.Context, ref string) error {
	if p.shared {
		return nil
	}
	if ok, err := p.HasArtifact(ctx, ref); err != nil || ok {
		return err
	}
	f, err := p.store.Open(ref)
	if err != nil {
		return err
	}
	defer f.Close()
	resp, err := p.do(ctx, http.MethodPut, "/v1/artifacts/"+ref, f, nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (p *Provider) Download(ctx context.Context, ref string) error {
	if p.shared || p.store.Has(ref) {
		return nil
	}
	resp, err := p.do(ctx, http.MethodGet, "/v1/artifacts/"+ref, nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	got, err := p.store.Put(resp.Body)
	if err != nil {
		return err
	}
	if got != ref {
		return fmt.Errorf("download of %s produced %s", ref, got)
	}
	return nil
}

func (p *Provider) Run(ctx context.Context, req engineapi.RunRequest, events chan<- engineapi.Event) (*engineapi.Run, error) {
	body, _ := json.Marshal(req)
	var run engineapi.Run
	if _, err := p.do(ctx, http.MethodPost, "/v1/runs", bytes.NewReader(body), &run); err != nil {
		return nil, err
	}
	cursor := 0
	for attempt := 0; ; attempt++ {
		before, opened := cursor, time.Now()
		done, err := p.stream(ctx, run.ID, &cursor, events)
		if done {
			break
		}
		if errors.Is(err, io.ErrUnexpectedEOF) && (cursor > before || time.Since(opened) > 5*time.Second) {
			// A gateway that closes long requests (Modal) ends streams that worked; resume.
			attempt = 0
		}
		if ctx.Err() != nil {
			p.abandon(run.ID)
			return nil, ctx.Err()
		}
		if attempt >= 5 {
			// Unwatched, the run would go on, and a paid gateway or pod bills it.
			p.abandon(run.ID)
			return nil, fmt.Errorf("event stream for run %s kept failing: %w", run.ID, err)
		}
		select { // reconnect, resuming at cursor
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt+1) * reconnect):
		}
	}
	var final engineapi.Run
	if _, err := p.do(ctx, http.MethodGet, "/v1/runs/"+run.ID, nil, &final); err != nil {
		return nil, err
	}
	return &final, nil
}

// reconnect is the pause before the first reconnect of a run's event stream; it grows by as
// much each time. Tests shorten it.
var reconnect = 200 * time.Millisecond

// abandon cancels a run the core stops following, whatever became of the caller's context.
func (p *Provider) abandon(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Cancel(ctx, runID)
}

// stream reads SSE until run.finished (done=true) or the connection drops.
func (p *Provider) stream(ctx context.Context, runID string, cursor *int, events chan<- engineapi.Event) (bool, error) {
	resp, err := p.do(ctx, http.MethodGet, "/v1/runs/"+runID+"/events?after="+strconv.Itoa(*cursor), nil, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var ev engineapi.Event
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			ev.ID, _ = strconv.Atoi(line[4:])
		case strings.HasPrefix(line, "event: "):
			ev.Type = line[7:]
		case strings.HasPrefix(line, "data: "):
			ev.Data = map[string]any{}
			_ = json.Unmarshal([]byte(line[6:]), &ev.Data)
		case line == "":
			if ev.Type == "" {
				continue
			}
			*cursor = ev.ID
			select {
			case events <- ev:
			case <-ctx.Done():
				return false, ctx.Err()
			}
			if ev.Type == "run.finished" {
				return true, nil
			}
			ev = engineapi.Event{}
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return false, io.ErrUnexpectedEOF
}

func (p *Provider) Cancel(ctx context.Context, runID string) error {
	resp, err := p.do(ctx, http.MethodDelete, "/v1/runs/"+runID, nil, nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

var _ provider.Provider = (*Provider)(nil)

func (p *Provider) Checks(ctx context.Context, models, stages []string) (*engineapi.Checks, error) {
	q := url.Values{"models": {strings.Join(models, ",")}}
	if len(stages) > 0 {
		q.Set("stages", strings.Join(stages, ","))
	}
	var out engineapi.Checks
	_, err := p.do(ctx, http.MethodGet, "/v1/checks?"+q.Encode(), nil, &out)
	return &out, err
}

func (p *Provider) Fix(ctx context.Context, check string) (*engineapi.Task, error) {
	return p.task(ctx, http.MethodPost, "/v1/checks/"+url.PathEscape(check)+"/fix", nil)
}

func (p *Provider) Install(ctx context.Context, model string, req engineapi.InstallRequest) (*engineapi.Task, error) {
	body, _ := json.Marshal(req)
	return p.task(ctx, http.MethodPost, "/v1/models/"+url.PathEscape(model)+"/install", bytes.NewReader(body))
}

func (p *Provider) Remove(ctx context.Context, model string) (*engineapi.Task, error) {
	return p.task(ctx, http.MethodDelete, "/v1/models/"+url.PathEscape(model), nil)
}

func (p *Provider) CancelTask(ctx context.Context, id string) error {
	resp, err := p.do(ctx, http.MethodDelete, "/v1/tasks/"+url.PathEscape(id), nil, nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func (p *Provider) task(ctx context.Context, method, path string, body io.Reader) (*engineapi.Task, error) {
	var t engineapi.Task
	if _, err := p.do(ctx, method, path, body, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// FollowTask reads the task's event stream, reconnecting from the last state it saw.
func (p *Provider) FollowTask(ctx context.Context, id string, states chan<- engineapi.Task) error {
	seen, failures := 0, 0
	for {
		done, err := p.followOnce(ctx, id, &seen, states)
		if done || ctx.Err() != nil {
			return ctx.Err()
		}
		if failures++; failures > 5 {
			return fmt.Errorf("lost the progress of task %s: %w", id, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(failures) * 300 * time.Millisecond):
		}
	}
}

func (p *Provider) followOnce(ctx context.Context, id string, seen *int, states chan<- engineapi.Task) (bool, error) {
	path := "/v1/tasks/" + url.PathEscape(id) + "/events?after=" + strconv.Itoa(*seen)
	resp, err := p.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var t engineapi.Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			continue
		}
		*seen = t.Version
		select {
		case states <- t:
		case <-ctx.Done():
			return false, ctx.Err()
		}
		if t.Done() {
			return true, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	return false, io.ErrUnexpectedEOF
}

var _ provider.Installer = (*Provider)(nil)
