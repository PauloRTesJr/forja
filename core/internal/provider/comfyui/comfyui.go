// Package comfyui runs stages as ComfyUI workflows, through ComfyUI's own HTTP API: an
// existing ComfyUI setup, with its custom nodes, becomes a provider.
//
// Each stage type the user maps gets one workflow in ComfyUI's API format ("Export (API)"),
// with placeholders where Forja's inputs and parameters go:
//
//	{{image}}             the front image, as a file name in ComfyUI's input folder
//	{{image:back}}        another view (left, back, right); the model then needs that view
//	{{mesh}}              the input mesh (texture stages), as a file name in the input folder
//	{{seed}}              the job's seed
//	{{param:resolution}}  a stage parameter from the preset
//
// A value that is only a placeholder takes the value's type (a number stays a number). Inputs
// upload to ComfyUI once per session by hash, and the file the workflow saves (a GLB for mesh
// stages, an image for background removal) is downloaded into the core's store. A workflow is
// keyed by its content, so editing it never reuses results cached from the old one.
package comfyui

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"forja/internal/engineapi"
	"forja/internal/provider"
	"forja/internal/store"
)

// Stages are the stage types a workflow can stand in for: the ones models do.
var Stages = []string{"background-removal", "shape", "texture"}

const maxOutput = 2 << 30

var placeholder = regexp.MustCompile(`\{\{\s*(image(?::(?:front|left|back|right))?|mesh|seed|param:[A-Za-z0-9_.-]+)\s*\}\}`)

// Workflow is one stage type's workflow, as read from its file.
type Workflow struct {
	Use   string
	Name  string
	Graph map[string]any
	// Hash keys cached results: a changed workflow is a new model revision.
	Hash  string
	Views []string // views besides the front the workflow reads
	Mesh  bool
}

// Load reads and checks an API-format workflow for a stage type.
func Load(use, path string) (*Workflow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(use, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), raw)
}

// Parse checks a workflow: API format, known placeholders, and the inputs its stage needs.
func Parse(use, name string, raw []byte) (*Workflow, error) {
	if !slices.Contains(Stages, use) {
		return nil, fmt.Errorf("a ComfyUI workflow can do %s, not %q", strings.Join(Stages, ", "), use)
	}
	var graph map[string]any
	if err := json.Unmarshal(raw, &graph); err != nil {
		return nil, fmt.Errorf("the %s workflow is not JSON: %w", use, err)
	}
	if _, ok := graph["nodes"]; ok {
		return nil, fmt.Errorf("the %s workflow is in ComfyUI's editor format; export it with Export (API)", use)
	}
	for id, n := range graph {
		node, ok := n.(map[string]any)
		if !ok || node["class_type"] == nil {
			return nil, fmt.Errorf("the %s workflow's node %s has no class_type; export it with Export (API)", use, id)
		}
	}
	sum := sha256.Sum256(raw)
	w := &Workflow{Use: use, Name: name, Graph: graph, Hash: hex.EncodeToString(sum[:])}
	image := false
	for _, m := range placeholder.FindAllStringSubmatch(string(raw), -1) {
		switch tag := m[1]; {
		case tag == "image" || tag == "image:front":
			image = true
		case strings.HasPrefix(tag, "image:"):
			if v := tag[6:]; !slices.Contains(w.Views, v) {
				w.Views = append(w.Views, v)
			}
		case tag == "mesh":
			w.Mesh = true
		}
	}
	if !image {
		return nil, fmt.Errorf("the %s workflow has no {{image}} placeholder for the reference image", use)
	}
	if use == "texture" && !w.Mesh {
		return nil, errors.New("the texture workflow has no {{mesh}} placeholder for the mesh to paint")
	}
	sort.Slice(w.Views, func(i, j int) bool {
		return slices.Index(engineapi.Views, w.Views[i]) < slices.Index(engineapi.Views, w.Views[j])
	})
	return w, nil
}

// ModelID names the model a workflow stands for.
func ModelID(use string) string { return "comfyui-" + use }

type Provider struct {
	id, base, token string
	paid            bool
	price           float64
	st              *store.Store
	workflows       map[string]*Workflow
	problems        map[string]string // stage type -> why its workflow cannot load
	http            *http.Client
	// Poll is how often a run's history is read when ComfyUI's event socket is unreachable.
	Poll time.Duration

	mu       sync.Mutex
	uploaded map[string]string // artifact ref -> file name in ComfyUI's input folder
	prompts  map[string]string // run id -> prompt id, for cancelling
}

// New makes a provider for one ComfyUI. workflows maps stage types to their files.
func New(id, baseURL, token string, workflows map[string]string, paid bool, usdPerSecond float64, st *store.Store) *Provider {
	p := &Provider{id: id, base: strings.TrimRight(baseURL, "/"), token: token, paid: paid, price: usdPerSecond, st: st,
		workflows: map[string]*Workflow{}, problems: map[string]string{}, uploaded: map[string]string{},
		prompts: map[string]string{}, Poll: time.Second,
		http: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}}}
	for use, path := range workflows {
		w, err := Load(use, path)
		if err != nil {
			p.problems[use] = err.Error()
			continue
		}
		p.workflows[use] = w
	}
	return p
}

func (p *Provider) ID() string          { return p.id }
func (p *Provider) Kind() provider.Kind { return provider.KindComfyUI }
func (p *Provider) Paid() bool          { return p.paid }

// SharesStore is true: runs read inputs from and write outputs to the core's store, and
// ComfyUI keeps its own copies in its input and output folders.
func (p *Provider) SharesStore() bool { return true }
func (p *Provider) HasArtifact(_ context.Context, ref string) (bool, error) {
	return p.st.Has(ref), nil
}
func (p *Provider) Upload(context.Context, string) error   { return nil }
func (p *Provider) Download(context.Context, string) error { return nil }

// cleartext is true when the token would cross the network unencrypted: plain http to another
// machine. Such a ComfyUI gets no request at all rather than the token in the clear.
func (p *Provider) cleartext() bool {
	if p.token == "" {
		return false
	}
	u, err := url.Parse(p.base)
	if err != nil {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme != "https" && u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())
}

var errCleartext = errors.New("Forja sends ComfyUI's access token only over https, or to a ComfyUI on this computer; " +
	"give it an https address, or remove the token")

func (p *Provider) request(ctx context.Context, method, path string, body io.Reader, kind string) (*http.Response, error) {
	if p.cleartext() {
		return nil, errCleartext
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return nil, err
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}
	if kind != "" {
		req.Header.Set("Content-Type", kind)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach ComfyUI at %s: %w", p.base, err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, errors.New("ComfyUI refused the token")
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("ComfyUI answered %s: %s", resp.Status, comfyError(raw))
	}
	return resp, nil
}

func (p *Provider) getJSON(ctx context.Context, path string, out any) error {
	resp, err := p.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Provider) postJSON(ctx context.Context, path string, body, out any) error {
	raw, _ := json.Marshal(body)
	resp, err := p.request(ctx, http.MethodPost, path, bytes.NewReader(raw), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// comfyError reads ComfyUI's validation errors: the prompt's, then each node's.
func comfyError(raw []byte) string {
	var doc struct {
		Error struct {
			Message string `json:"message"`
			Details string `json:"details"`
		} `json:"error"`
		NodeErrors map[string]struct {
			ClassType string `json:"class_type"`
			Errors    []struct {
				Message string `json:"message"`
				Details string `json:"details"`
			} `json:"errors"`
		} `json:"node_errors"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Error.Message == "" {
		return strings.TrimSpace(string(raw))
	}
	msgs := []string{doc.Error.Message}
	ids := make([]string, 0, len(doc.NodeErrors))
	for id := range doc.NodeErrors {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		n := doc.NodeErrors[id]
		for _, e := range n.Errors {
			msgs = append(msgs, fmt.Sprintf("node %s (%s): %s %s", id, n.ClassType, e.Message, e.Details))
		}
	}
	return strings.Join(msgs, "; ")
}

// Stats is what ComfyUI's /system_stats says of itself.
type Stats struct {
	System struct {
		Version string `json:"comfyui_version"`
		OS      string `json:"os"`
	} `json:"system"`
	Devices []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		VRAMTotal int64  `json:"vram_total"`
	} `json:"devices"`
}

func (p *Provider) Stats(ctx context.Context) (*Stats, error) {
	var s Stats
	return &s, p.getJSON(ctx, "/system_stats", &s)
}

func (p *Provider) Health(ctx context.Context) error {
	_, err := p.Stats(ctx)
	return err
}

func (p *Provider) Info(ctx context.Context) (*engineapi.Info, error) {
	s, err := p.Stats(ctx)
	if err != nil {
		return nil, err
	}
	devices := []any{}
	for _, d := range s.Devices {
		devices = append(devices, map[string]any{"id": d.Type, "backend": d.Type, "name": d.Name, "vram_bytes": d.VRAMTotal})
	}
	return &engineapi.Info{Version: "ComfyUI " + s.System.Version, APIVersion: engineapi.APIVersion, Stages: map[string]string{},
		System: map[string]any{"vendor": "ComfyUI", "os": s.System.OS, "devices": devices}}, nil
}

var where = "comfyui"

// Models has one model per mapped stage type: its workflow.
func (p *Provider) Models(context.Context) ([]engineapi.Model, error) {
	uses := make([]string, 0, len(p.workflows)+len(p.problems))
	for use := range p.workflows {
		uses = append(uses, use)
	}
	for use := range p.problems {
		uses = append(uses, use)
	}
	sort.Strings(uses)
	out := []engineapi.Model{}
	for _, use := range uses {
		m := engineapi.Model{ID: ModelID(use), Family: "ComfyUI", Tasks: []string{use}, Params: map[string]any{},
			Requires: map[string]any{}, Status: "ready", Device: &where, RunsOn: &where, Fit: "hosted", AdapterVersion: "1",
			License: engineapi.License{SPDX: "NOASSERTION", Commercial: true, Restrictions: []string{}}}
		w := p.workflows[use]
		if w == nil {
			m.Name, m.Status, m.Device, m.Tasks = use+" workflow (ComfyUI)", "unsupported", nil, []string{}
			m.Description = "Forja cannot use it: " + p.problems[use]
			out = append(out, m)
			continue
		}
		m.Name = w.Name + " (ComfyUI)"
		m.Description = "A ComfyUI workflow; its nodes and models carry their own licenses."
		m.Weights = engineapi.Weights{Repo: "comfyui", Revision: w.Hash}
		if len(w.Views) > 0 {
			m.Views = append([]string{"front"}, w.Views...)
			m.ViewsRequired = w.Views
		}
		if p.price > 0 {
			m.Price = &engineapi.Price{Amount: p.price, Unit: "second", Currency: "USD"}
		}
		out = append(out, m)
	}
	return out, nil
}

// Run queues each stage's workflow in turn and waits for it, inputs uploaded, outputs stored.
func (p *Provider) Run(ctx context.Context, req engineapi.RunRequest, events chan<- engineapi.Event) (*engineapi.Run, error) {
	run := &engineapi.Run{ID: "comfyui-" + randomID(), Status: "succeeded", Stages: map[string]engineapi.StageState{}}
	produced := map[string]string{}
	for _, s := range req.Stages {
		start := time.Now()
		emit(ctx, events, "stage.started", map[string]any{"stage": s.ID, "model": s.Model, "device": where})
		out, err := p.runStage(ctx, run.ID, req.Seed, s, produced, events)
		secs := time.Since(start).Seconds()
		if err != nil {
			msg, status := err.Error(), "failed"
			if ctx.Err() != nil {
				msg, status = "cancelled", "cancelled"
			}
			run.Status, run.Error = status, &msg
			run.Stages[s.ID] = engineapi.StageState{Status: status, Error: &msg, Seconds: &secs}
			emit(ctx, events, "stage.failed", map[string]any{"stage": s.ID, "error": msg})
			break
		}
		produced[s.ID] = out.Hash
		run.Stages[s.ID] = engineapi.StageState{Status: "succeeded", Outputs: []engineapi.Output{out}, Seconds: &secs}
		emit(ctx, events, "artifact.ready", map[string]any{"stage": s.ID, "hash": out.Hash, "kind": out.Kind, "preview": out.Preview})
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

func (p *Provider) runStage(ctx context.Context, runID string, seed int, s engineapi.StageSpec, produced map[string]string,
	events chan<- engineapi.Event,
) (engineapi.Output, error) {
	w := p.workflows[s.Use]
	if w == nil || s.Model != ModelID(s.Use) {
		if why := p.problems[s.Use]; why != "" {
			return engineapi.Output{}, errors.New(why)
		}
		return engineapi.Output{}, fmt.Errorf("this ComfyUI has no workflow for %s", s.Use)
	}
	values := map[string]any{"seed": seed}
	if v, ok := s.Params["seed"]; ok {
		values["seed"] = v
	}
	for k, v := range s.Params {
		values["param:"+k] = v
	}
	for i, in := range s.Inputs {
		ref := in
		if id, ok := strings.CutPrefix(in, "stage:"); ok {
			ref = produced[id]
		}
		name, kind, err := p.upload(ctx, ref)
		if err != nil {
			return engineapi.Output{}, err
		}
		view := "front"
		if i < len(s.Views) && s.Views[i] != "" {
			view = s.Views[i]
		}
		switch {
		case kind == "mesh":
			values["mesh"] = name
		case view == "front":
			values["image"], values["image:front"] = name, name
		default:
			values["image:"+view] = name
		}
	}
	graph, err := fill(w.Graph, values)
	if err != nil {
		return engineapi.Output{}, fmt.Errorf("the %s workflow: %w", s.Use, err)
	}

	client := randomID()
	ws := p.listen(ctx, client)
	var queued struct {
		PromptID string `json:"prompt_id"`
	}
	if err := p.postJSON(ctx, "/prompt", map[string]any{"prompt": graph, "client_id": client}, &queued); err != nil {
		if ws != nil {
			ws.CloseNow()
		}
		// A ComfyUI that lost its input folder (moved, cleaned) gets the files again next time.
		p.mu.Lock()
		for _, in := range s.Inputs {
			delete(p.uploaded, in)
			if id, ok := strings.CutPrefix(in, "stage:"); ok {
				delete(p.uploaded, produced[id])
			}
		}
		p.mu.Unlock()
		return engineapi.Output{}, err
	}
	p.mu.Lock()
	p.prompts[runID] = queued.PromptID
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.prompts, runID)
		p.mu.Unlock()
	}()
	if err := p.wait(ctx, ws, queued.PromptID, s.ID, events); err != nil {
		if ctx.Err() != nil {
			p.interrupt(queued.PromptID)
		}
		return engineapi.Output{}, err
	}
	return p.fetch(ctx, queued.PromptID, s.Use)
}

// fill copies a workflow with its placeholders replaced.
func fill(graph map[string]any, values map[string]any) (map[string]any, error) {
	var missing []string
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, val := range x {
				out[k] = walk(val)
			}
			return out
		case []any:
			out := make([]any, len(x))
			for i, val := range x {
				out[i] = walk(val)
			}
			return out
		case string:
			if m := placeholder.FindStringSubmatch(x); m != nil && m[0] == strings.TrimSpace(x) {
				val, ok := values[m[1]]
				if !ok {
					missing = append(missing, m[1])
					return x
				}
				return val
			}
			return placeholder.ReplaceAllStringFunc(x, func(tag string) string {
				key := placeholder.FindStringSubmatch(tag)[1]
				val, ok := values[key]
				if !ok {
					missing = append(missing, key)
					return tag
				}
				return fmt.Sprint(val)
			})
		}
		return v
	}
	out := walk(graph).(map[string]any)
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("nothing to put in {{%s}}", strings.Join(slices.Compact(missing), "}}, {{"))
	}
	return out, nil
}

// upload sends an input to ComfyUI's input folder, once per session, named by its hash.
func (p *Provider) upload(ctx context.Context, ref string) (string, string, error) {
	f, err := p.st.Open(ref)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	kind, ext := sniff(head[:n])
	if kind == "" {
		return "", "", fmt.Errorf("input %s is neither an image nor a GLB", ref)
	}
	p.mu.Lock()
	name, ok := p.uploaded[ref]
	p.mu.Unlock()
	if ok {
		return name, kind, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	digest := strings.TrimPrefix(ref, "blake3:")
	name = "forja-" + digest[:min(16, len(digest))] + ext
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("image", name)
	if _, err := io.Copy(part, f); err != nil {
		return "", "", err
	}
	_ = mw.WriteField("type", "input")
	_ = mw.WriteField("overwrite", "true")
	_ = mw.Close()
	resp, err := p.request(ctx, http.MethodPost, "/upload/image", &body, mw.FormDataContentType())
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var up struct {
		Name      string `json:"name"`
		Subfolder string `json:"subfolder"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil || up.Name == "" {
		return "", "", errors.New("ComfyUI did not say where it saved the upload")
	}
	name = up.Name
	if up.Subfolder != "" {
		name = up.Subfolder + "/" + up.Name
	}
	p.mu.Lock()
	p.uploaded[ref] = name
	p.mu.Unlock()
	return name, kind, nil
}

func sniff(head []byte) (kind, ext string) {
	switch {
	case bytes.HasPrefix(head, []byte("\x89PNG")):
		return "image", ".png"
	case bytes.HasPrefix(head, []byte{0xFF, 0xD8, 0xFF}):
		return "image", ".jpg"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image", ".webp"
	case bytes.HasPrefix(head, []byte("glTF")):
		return "mesh", ".glb"
	}
	return "", ""
}

// listen opens ComfyUI's event socket for one client id; nil when it cannot (a proxy that
// passes no WebSockets), and the run then reads the history instead.
func (p *Provider) listen(ctx context.Context, client string) *websocket.Conn {
	u, err := url.Parse(p.base + "/ws?clientId=" + url.QueryEscape(client))
	if err != nil || p.cleartext() {
		return nil
	}
	u.Scheme = strings.Replace(u.Scheme, "http", "ws", 1)
	opts := &websocket.DialOptions{HTTPClient: p.http}
	if p.token != "" {
		opts.HTTPHeader = http.Header{"Authorization": {"Bearer " + p.token}}
	}
	dial, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dial, u.String(), opts)
	if err != nil {
		return nil
	}
	conn.SetReadLimit(64 << 20) // previews arrive as binary frames
	return conn
}

type message struct {
	Type string `json:"type"`
	Data struct {
		PromptID  string  `json:"prompt_id"`
		Node      *string `json:"node"`
		Value     int     `json:"value"`
		Max       int     `json:"max"`
		Exception string  `json:"exception_message"`
		NodeType  string  `json:"node_type"`
	} `json:"data"`
}

// wait follows a prompt to its end, forwarding progress.
func (p *Provider) wait(ctx context.Context, ws *websocket.Conn, prompt, stage string, events chan<- engineapi.Event) error {
	if ws != nil {
		defer ws.CloseNow()
		for {
			typ, raw, err := ws.Read(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				break // the socket dropped: the history says how it ended
			}
			if typ != websocket.MessageText {
				continue
			}
			var m message
			if json.Unmarshal(raw, &m) != nil || m.Data.PromptID != prompt {
				continue
			}
			switch m.Type {
			case "progress":
				emit(ctx, events, "stage.progress", map[string]any{"stage": stage, "step": m.Data.Value, "total": m.Data.Max})
			case "execution_error":
				return fmt.Errorf("ComfyUI node %s failed: %s", m.Data.NodeType, strings.TrimSpace(m.Data.Exception))
			case "execution_interrupted":
				return errors.New("the ComfyUI run was interrupted")
			case "execution_success":
				return nil
			case "executing":
				if m.Data.Node == nil {
					return nil
				}
			}
		}
	}
	for {
		h, err := p.history(ctx, prompt)
		if err != nil {
			return err
		}
		if h != nil && (h.Status.Completed || h.Status.StatusStr == "error") {
			if h.Status.StatusStr == "error" {
				return errors.New("the ComfyUI run failed; ComfyUI's console says why")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.Poll):
		}
	}
}

type history struct {
	Outputs map[string]map[string]any `json:"outputs"`
	Status  struct {
		StatusStr string `json:"status_str"`
		Completed bool   `json:"completed"`
	} `json:"status"`
}

func (p *Provider) history(ctx context.Context, prompt string) (*history, error) {
	var all map[string]history
	if err := p.getJSON(ctx, "/history/"+url.PathEscape(prompt), &all); err != nil {
		return nil, err
	}
	h, ok := all[prompt]
	if !ok {
		return nil, nil
	}
	return &h, nil
}

type file struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// fetch downloads the file a finished prompt saved for its stage into the store.
func (p *Provider) fetch(ctx context.Context, prompt, use string) (engineapi.Output, error) {
	h, err := p.history(ctx, prompt)
	if err != nil {
		return engineapi.Output{}, err
	}
	want, kind := []string{".glb"}, "mesh/glb"
	if use == "background-removal" {
		want, kind = []string{".png", ".webp"}, "image/png"
	}
	var found *file
	if h != nil {
		nodes := make([]string, 0, len(h.Outputs))
		for id := range h.Outputs {
			nodes = append(nodes, id)
		}
		sort.Strings(nodes)
	search:
		for _, id := range nodes {
			for _, files := range h.Outputs[id] {
				list, _ := files.([]any)
				for _, f := range list {
					raw, _ := json.Marshal(f)
					var cand file
					if json.Unmarshal(raw, &cand) == nil && cand.Filename != "" &&
						slices.Contains(want, strings.ToLower(filepath.Ext(cand.Filename))) {
						found = &cand
						break search
					}
				}
			}
		}
	}
	if found == nil {
		return engineapi.Output{}, fmt.Errorf("the %s workflow saved no %s file; end it with a node that saves one", use,
			strings.Join(want, " or "))
	}
	q := url.Values{"filename": {found.Filename}, "subfolder": {found.Subfolder}, "type": {orDefault(found.Type, "output")}}
	resp, err := p.request(ctx, http.MethodGet, "/view?"+q.Encode(), nil, "")
	if err != nil {
		return engineapi.Output{}, err
	}
	defer resp.Body.Close()
	ref, err := p.st.Put(io.LimitReader(resp.Body, maxOutput))
	if err != nil {
		return engineapi.Output{}, err
	}
	return engineapi.Output{Hash: ref, Kind: kind, Preview: kind == "mesh/glb"}, nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// interrupt stops a prompt: out of the queue if it waits, interrupted if it runs.
func (p *Provider) interrupt(prompt string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = p.postJSON(ctx, "/queue", map[string]any{"delete": []string{prompt}}, nil)
	_ = p.postJSON(ctx, "/interrupt", map[string]any{"prompt_id": prompt}, nil)
}

func (p *Provider) Cancel(_ context.Context, runID string) error {
	p.mu.Lock()
	prompt := p.prompts[runID]
	p.mu.Unlock()
	if prompt != "" {
		p.interrupt(prompt)
	}
	return nil
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var _ provider.Provider = (*Provider)(nil)
