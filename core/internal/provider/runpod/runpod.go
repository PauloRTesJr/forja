// Package runpod runs stages on a RunPod serverless endpoint: the engine image behind RunPod's
// job queue (docker/runpod), which scales to zero and bills each second of GPU time.
//
// A run is one queue job. The worker streams the engine's events back while it runs, and its
// result is the engine's run. Artifacts move one of two ways:
//
//   - Through the endpoint's network volume, when Forja has the volume's S3 key: the engine's
//     store lives on the volume, so the core checks a hash with a HEAD and sends or fetches
//     only what the other side lacks. Intermediates stay on the volume between runs.
//   - Inline, without that key: the run's inputs ride in the job as base64 and every output
//     comes back in the result, within RunPod's payload limit.
package runpod

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"forja/internal/engineapi"
	"forja/internal/provider"
	"forja/internal/s3"
	"forja/internal/store"
)

// Vendor selects this provider: kind = "serverless", vendor = "runpod".
const Vendor = "runpod"

// APIBase is RunPod's serverless API; tests point it at a fake.
var APIBase = "https://api.runpod.ai/v2"

// VolumeEndpoint is where RunPod serves the S3 API of volumes in a datacenter.
var VolumeEndpoint = func(datacenter string) string {
	return "https://s3api-" + strings.ToLower(datacenter) + ".runpod.io"
}

// InlineLimit keeps a job under RunPod's 10 MB payload cap once base64 has grown it by a third.
const InlineLimit = 7 << 20

// storePrefix is the engine's store on the volume, as the worker mounts it (/runpod-volume/forja).
const storePrefix = "forja/store/objects/"

// Volume is an endpoint's network volume, reached through RunPod's S3 API.
type Volume struct {
	ID, Datacenter, AccessKey, Secret string
}

type Provider struct {
	id, endpoint, key string
	price             float64
	st                *store.Store
	vol               *s3.Client
	// API is RunPod's base URL; tests point it at a fake.
	API  string
	http *http.Client
	// Poll is the longest wait between two looks at a job.
	Poll time.Duration

	mu        sync.Mutex
	described *description
}

type description struct {
	Info   engineapi.Info    `json:"info"`
	Models []engineapi.Model `json:"models"`
}

// New makes the provider for one endpoint. vol is nil when Forja has no S3 key for its volume.
func New(id, endpointID, key string, usdPerSecond float64, vol *Volume, st *store.Store) *Provider {
	p := &Provider{id: id, endpoint: endpointID, key: key, price: usdPerSecond, st: st, API: APIBase,
		http: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}},
		Poll: 2 * time.Second}
	if vol != nil && vol.ID != "" {
		p.vol = &s3.Client{Endpoint: VolumeEndpoint(vol.Datacenter), Region: strings.ToUpper(vol.Datacenter),
			Bucket: vol.ID, AccessKey: vol.AccessKey, Secret: vol.Secret, HTTP: p.http}
	}
	return p
}

// URL is the endpoint's address, for display.
func (p *Provider) URL() string { return p.API + "/" + url.PathEscape(p.endpoint) }

func (p *Provider) ID() string          { return p.id }
func (p *Provider) Kind() provider.Kind { return provider.KindServerless }
func (p *Provider) Paid() bool          { return true }
func (p *Provider) SharesStore() bool   { return false }

// Asleep is true until a worker described the endpoint in this session: asking would wake one.
func (p *Provider) Asleep() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.described == nil
}

// UsesVolume says whether artifacts move through the network volume rather than inline.
func (p *Provider) UsesVolume() bool { return p.vol != nil }

func (p *Provider) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.URL()+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach RunPod: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return &answered{resp.StatusCode, "RunPod refused the API key"}
	case resp.StatusCode == http.StatusNotFound:
		return &answered{resp.StatusCode, fmt.Sprintf("RunPod has no serverless endpoint %s", p.endpoint)}
	case resp.StatusCode >= 400:
		return &answered{resp.StatusCode, fmt.Sprintf("RunPod answered %s: %s", resp.Status, strings.TrimSpace(string(raw)))}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// answered is an error RunPod answered with: an error status, or a job that ran and failed
// (status 200). The job runner moves a stage to another provider only for a 5xx or no answer,
// since a job RunPod took has been billed and would only be billed again elsewhere.
type answered struct {
	Status int
	Msg    string
}

func (e *answered) Error() string   { return e.Msg }
func (e *answered) StatusCode() int { return e.Status }

func failedJob(format string, args ...any) error {
	return &answered{http.StatusOK, fmt.Sprintf(format, args...)}
}

// Waits that tests shorten: the first pause before a failed look at a job is tried again (it
// doubles), and the longest.
var (
	retryBase = time.Second
	retryMax  = 8 * time.Second
)

// failures is how many failed looks in a row a job survives.
const failures = 5

// transient reports a failed look at a job worth trying again: no answer, a rate limit, a
// server error. The end of ctx is not.
func transient(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var a *answered
	if errors.As(err, &a) {
		return a.Status == http.StatusTooManyRequests || a.Status >= 500
	}
	return true
}

// again runs fn until it succeeds, fails for good, or fails `failures` times in a row.
func again(ctx context.Context, fn func() error) error {
	delay := retryBase
	for n := 1; ; n++ {
		err := fn()
		if err == nil || n >= failures || !transient(ctx, err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, retryMax)
	}
}

// Workers is what RunPod's health check says of an endpoint's workers and queue.
type Workers struct {
	Idle    int `json:"idle"`
	Running int `json:"running"`
	Queued  int `json:"queued"`
}

// Check asks RunPod about the endpoint, which costs nothing and wakes no worker, and tries the
// volume's S3 key when there is one.
func (p *Provider) Check(ctx context.Context) (*Workers, error) {
	var h struct {
		Jobs struct {
			InQueue int `json:"inQueue"`
		} `json:"jobs"`
		Workers struct {
			Idle    int `json:"idle"`
			Running int `json:"running"`
		} `json:"workers"`
	}
	if err := p.call(ctx, http.MethodGet, "/health", nil, &h); err != nil {
		return nil, err
	}
	if p.vol != nil {
		if _, err := p.vol.Has(ctx, storePrefix+"00/check"); err != nil {
			return nil, fmt.Errorf("network volume %s: %w", p.vol.Bucket, err)
		}
	}
	return &Workers{Idle: h.Workers.Idle, Running: h.Workers.Running, Queued: h.Jobs.InQueue}, nil
}

func (p *Provider) Health(ctx context.Context) error {
	_, err := p.Check(ctx)
	return err
}

// describe asks a worker what it runs, once a session; every run's result refreshes it.
func (p *Provider) describe(ctx context.Context) (*description, error) {
	p.mu.Lock()
	d := p.described
	p.mu.Unlock()
	if d != nil {
		return d, nil
	}
	var got description
	if err := p.job(ctx, map[string]any{"op": "describe"}, nil, &got); err != nil {
		return nil, err
	}
	p.remember(&got)
	return &got, nil
}

func (p *Provider) remember(d *description) {
	if d == nil || d.Info.APIVersion == "" {
		return
	}
	p.mu.Lock()
	p.described = d
	p.mu.Unlock()
}

func (p *Provider) Info(ctx context.Context) (*engineapi.Info, error) {
	d, err := p.describe(ctx)
	if err != nil {
		return nil, err
	}
	info := d.Info
	return &info, engineapi.CheckCompatible(info.APIVersion)
}

// Models are the worker's, priced per second of GPU time when a price is set.
func (p *Provider) Models(ctx context.Context) ([]engineapi.Model, error) {
	d, err := p.describe(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engineapi.Model, len(d.Models))
	for i, m := range d.Models {
		if p.price > 0 {
			m.Price = &engineapi.Price{Amount: p.price, Unit: "second", Currency: "USD"}
		}
		out[i] = m
	}
	return out, nil
}

func objectKey(ref string) (string, error) {
	hex, ok := strings.CutPrefix(ref, "blake3:")
	if !ok || len(hex) != 64 {
		return "", fmt.Errorf("not an artifact ref: %q", ref)
	}
	return storePrefix + hex[:2] + "/" + hex[2:], nil
}

// HasArtifact checks the volume; without one, nothing stays on the endpoint between runs.
func (p *Provider) HasArtifact(ctx context.Context, ref string) (bool, error) {
	if p.vol == nil {
		return false, nil
	}
	key, err := objectKey(ref)
	if err != nil {
		return false, err
	}
	return p.vol.Has(ctx, key)
}

// Upload writes an input into the store on the volume unless it is there; without a volume,
// inputs go with the run instead.
func (p *Provider) Upload(ctx context.Context, ref string) error {
	if p.vol == nil {
		return nil
	}
	if ok, err := p.HasArtifact(ctx, ref); err != nil || ok {
		return err
	}
	key, _ := objectKey(ref)
	path, err := p.st.Path(ref)
	if err != nil {
		return err
	}
	return p.vol.PutFile(ctx, key, path)
}

// Download fetches an output from the volume; inline outputs are already in the store.
func (p *Provider) Download(ctx context.Context, ref string) error {
	if p.st.Has(ref) {
		return nil
	}
	if p.vol == nil {
		return fmt.Errorf("%s is not in the store yet", ref)
	}
	key, err := objectKey(ref)
	if err != nil {
		return err
	}
	body, err := p.vol.Get(ctx, key)
	if err != nil {
		return err
	}
	defer body.Close()
	got, err := p.st.Put(body)
	if err != nil {
		return err
	}
	if got != ref {
		return fmt.Errorf("download of %s produced %s", ref, got)
	}
	return nil
}

// result is the worker's answer to a run job.
type result struct {
	description
	Run       *engineapi.Run    `json:"run"`
	Artifacts map[string]string `json:"artifacts"`
	Missing   []string          `json:"missing"`
}

func (p *Provider) Run(ctx context.Context, req engineapi.RunRequest, events chan<- engineapi.Event) (*engineapi.Run, error) {
	in := map[string]any{"op": "run", "run": req, "outputs": "store"}
	if p.vol == nil {
		inline, err := p.inline(req)
		if err != nil {
			return nil, err
		}
		in["outputs"], in["inline"], in["limit"] = "inline", inline, InlineLimit
	}
	var res result
	if err := p.job(ctx, in, events, &res); err != nil {
		return nil, err
	}
	if len(res.Missing) > 0 {
		return nil, failedJob("the worker cannot find %s on the network volume", strings.Join(res.Missing, ", "))
	}
	if res.Run == nil {
		return nil, failedJob("the worker answered without a run")
	}
	for ref, b64 := range res.Artifacts {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, failedJob("output %s: %v", ref, err)
		}
		got, err := p.st.Put(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		if got != ref {
			return nil, fmt.Errorf("output %s arrived as %s", ref, got)
		}
	}
	p.remember(&res.description)
	return res.Run, nil
}

// inline reads the run's inputs from the store for a job that carries them.
func (p *Provider) inline(req engineapi.RunRequest) (map[string]string, error) {
	out := map[string]string{}
	total := 0
	for _, s := range req.Stages {
		for _, ref := range s.Inputs {
			if !strings.HasPrefix(ref, "blake3:") || out[ref] != "" {
				continue
			}
			f, err := p.st.Open(ref)
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(io.LimitReader(f, InlineLimit+1))
			f.Close()
			if err != nil {
				return nil, err
			}
			if total += len(raw); total > InlineLimit {
				return nil, fmt.Errorf("the inputs of stage %s pass %d MB, more than a RunPod job carries; "+
					"give Forja the S3 key of the endpoint's network volume", s.ID, InlineLimit>>20)
			}
			out[ref] = base64.StdEncoding.EncodeToString(raw)
		}
	}
	return out, nil
}

// item is one thing a worker yields: an engine event, the result, or an error.
type item struct {
	Event  *engineapi.Event `json:"event"`
	Result json.RawMessage  `json:"result"`
	Error  string           `json:"error"`
}

type jobStatus struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Error  any             `json:"error"`
	Output json.RawMessage `json:"output"`
	Stream []struct {
		Output item `json:"output"`
	} `json:"stream"`
}

func finished(status string) bool {
	switch status {
	case "COMPLETED", "FAILED", "CANCELLED", "TIMED_OUT":
		return true
	}
	return false
}

// job submits one queue job and follows it to the end: events go to events, the result into
// out. A job that does not end here is cancelled on RunPod, whether ctx ended or following it
// failed: left alone, it would run on, and bill, until RunPod's own timeout.
func (p *Provider) job(ctx context.Context, input any, events chan<- engineapi.Event, out any) error {
	var sub jobStatus
	if err := p.call(ctx, http.MethodPost, "/run", map[string]any{"input": input}, &sub); err != nil {
		return err
	}
	if sub.ID == "" {
		return failedJob("RunPod accepted the job without an id")
	}
	ended := false
	defer func() {
		if !ended {
			p.cancel(sub.ID)
		}
	}()
	look := func(path string, st *jobStatus) error {
		err := again(ctx, func() error {
			*st = jobStatus{}
			return p.call(ctx, http.MethodGet, path+url.PathEscape(sub.ID), nil, st)
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	f := &follower{ctx: ctx, events: events}
	delay := 250 * time.Millisecond
	for {
		var st jobStatus
		if err := look("/stream/", &st); err != nil {
			return err
		}
		for _, s := range st.Stream {
			if err := f.take(s.Output); err != nil {
				return err
			}
		}
		if finished(st.Status) {
			ended = true
			if f.result == nil {
				// The aggregated output has every item, for what the stream did not show.
				var whole jobStatus
				if err := look("/status/", &whole); err != nil {
					return err
				}
				if err := f.replay(whole.Output); err != nil {
					return err
				}
				if f.result == nil {
					why := whole.Error
					if why == nil || why == "" {
						why = strings.ToLower(st.Status)
					}
					return failedJob("the RunPod job ended without a result: %v", why)
				}
			}
			if err := json.Unmarshal(f.result, out); err != nil {
				return failedJob("the RunPod worker's result: %v", err)
			}
			return nil
		}
		if len(st.Stream) > 0 {
			delay = 250 * time.Millisecond
		} else {
			delay = min(delay*2, p.Poll)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// follower takes a job's items in order, from its stream and then from its whole output.
type follower struct {
	ctx    context.Context
	events chan<- engineapi.Event
	seen   int
	result json.RawMessage
}

func (f *follower) take(it item) error {
	switch {
	case it.Error != "":
		return failedJob("the RunPod worker failed: %s", it.Error)
	case it.Event != nil:
		f.seen++
		if f.events != nil {
			select {
			case f.events <- *it.Event:
			case <-f.ctx.Done():
			}
		}
	case it.Result != nil:
		f.result = it.Result
	}
	return nil
}

// replay takes the items of a whole output that the stream did not show.
func (f *follower) replay(output json.RawMessage) error {
	var items []item
	if err := json.Unmarshal(output, &items); err != nil {
		var one item
		if json.Unmarshal(output, &one) != nil {
			return nil
		}
		items = []item{one}
	}
	shown, skipped := f.seen, 0
	for _, it := range items {
		if it.Event != nil && skipped < shown {
			skipped++
			continue
		}
		if err := f.take(it); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) cancel(jobID string) {
	ctx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = p.call(ctx, http.MethodPost, "/cancel/"+url.PathEscape(jobID), nil, nil)
}

// Cancel is a no-op: a run's job is cancelled when its context is.
func (p *Provider) Cancel(context.Context, string) error { return nil }

var (
	_ provider.Provider = (*Provider)(nil)
	_ provider.Sleeper  = (*Provider)(nil)
)
