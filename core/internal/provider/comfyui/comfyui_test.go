package comfyui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"forja/internal/engineapi"
	"forja/internal/store"
)

// A texture workflow as ComfyUI's Export (API) writes it, with Forja's placeholders.
const paintWorkflow = `{
  "1": {"class_type": "LoadImage", "inputs": {"image": "{{image}}"}},
  "2": {"class_type": "Hy3DLoadMesh", "inputs": {"glb": "{{mesh}}"}},
  "3": {"class_type": "Hy3DPaint", "inputs": {"image": ["1", 0], "mesh": ["2", 0], "seed": "{{seed}}",
        "resolution": "{{param:resolution}}", "note": "seed {{seed}}"}},
  "4": {"class_type": "SaveGLB", "inputs": {"mesh": ["3", 0], "filename_prefix": "forja"}}
}`

type fakeComfy struct {
	t        *testing.T
	mu       sync.Mutex
	uploads  []string
	files    map[string][]byte // input folder
	prompts  []map[string]any
	noSocket bool
	fail     string // a node error the run reports
	refuse   bool   // validation fails at /prompt
	sockets  map[string]*websocket.Conn
	done     map[string]bool
	stopped  []string
}

const outputGLB = "glTF\x02\x00\x00\x00 painted"

func newComfy(t *testing.T) (*fakeComfy, *httptest.Server) {
	f := &fakeComfy{t: t, files: map[string][]byte{}, sockets: map[string]*websocket.Conn{}, done: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeComfy) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/system_stats":
		_, _ = io.WriteString(w, `{"system":{"os":"posix","comfyui_version":"0.3.60","python_version":"3.12"},
			"devices":[{"name":"cuda:0 NVIDIA RTX 4090 : cudaMallocAsync","type":"cuda","index":0,"vram_total":25757220864}]}`)
	case r.URL.Path == "/upload/image":
		file, header, err := r.FormFile("image")
		if err != nil || r.FormValue("type") != "input" {
			w.WriteHeader(400)
			return
		}
		raw, _ := io.ReadAll(file)
		f.mu.Lock()
		f.uploads = append(f.uploads, header.Filename)
		f.files[header.Filename] = raw
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"name": header.Filename, "subfolder": "", "type": "input"})
	case r.URL.Path == "/ws":
		if f.noSocket {
			w.WriteHeader(404)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.sockets[r.URL.Query().Get("clientId")] = conn
		f.mu.Unlock()
		<-r.Context().Done()
	case r.URL.Path == "/prompt":
		var body struct {
			Prompt   map[string]any `json:"prompt"`
			ClientID string         `json:"client_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if f.refuse {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"type":"prompt_outputs_failed_validation","message":"Prompt outputs failed validation","details":""},
				"node_errors":{"2":{"errors":[{"type":"value_not_in_list","message":"Value not in list","details":"glb: 'x.glb' not in []"}],"class_type":"Hy3DLoadMesh"}}}`)
			return
		}
		f.mu.Lock()
		f.prompts = append(f.prompts, body.Prompt)
		id := "prompt-" + string(rune('a'+len(f.prompts)-1))
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"prompt_id": id, "number": 1, "node_errors": map[string]any{}})
		go f.execute(id, f.socket(body.ClientID))
	case strings.HasPrefix(r.URL.Path, "/history/"):
		id := strings.TrimPrefix(r.URL.Path, "/history/")
		f.mu.Lock()
		done := f.done[id]
		f.mu.Unlock()
		if !done {
			_, _ = io.WriteString(w, `{}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{id: map[string]any{
			"outputs": map[string]any{
				"1": map[string]any{"images": []any{map[string]any{"filename": "preview.png", "subfolder": "", "type": "temp"}}},
				"4": map[string]any{"3d": []any{map[string]any{"filename": "forja_00001_.glb", "subfolder": "3d", "type": "output"}}},
			},
			"status": map[string]any{"status_str": "success", "completed": true, "messages": []any{}},
		}})
	case r.URL.Path == "/view":
		q := r.URL.Query()
		if q.Get("filename") != "forja_00001_.glb" || q.Get("subfolder") != "3d" || q.Get("type") != "output" {
			w.WriteHeader(404)
			return
		}
		_, _ = io.WriteString(w, outputGLB)
	case r.URL.Path == "/interrupt", r.URL.Path == "/queue":
		f.mu.Lock()
		f.stopped = append(f.stopped, r.URL.Path)
		f.mu.Unlock()
	default:
		w.WriteHeader(404)
	}
}

// socket is the client's event socket, which its handler may still be registering.
func (f *fakeComfy) socket(client string) *websocket.Conn {
	for range 200 {
		f.mu.Lock()
		conn, ok := f.sockets[client]
		f.mu.Unlock()
		if ok || f.noSocket {
			return conn
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

// execute plays a run's socket messages the way ComfyUI sends them.
func (f *fakeComfy) execute(id string, conn *websocket.Conn) {
	ctx := context.Background()
	send := func(typ string, data map[string]any) {
		if conn == nil {
			return
		}
		data["prompt_id"] = id
		raw, _ := json.Marshal(map[string]any{"type": typ, "data": data})
		_ = conn.Write(ctx, websocket.MessageText, raw)
	}
	send("execution_start", map[string]any{})
	_ = func() error {
		if conn != nil {
			return conn.Write(ctx, websocket.MessageBinary, []byte{0, 0, 0, 1, 'p', 'r', 'e', 'v'})
		}
		return nil
	}()
	send("progress", map[string]any{"value": 1, "max": 2, "node": "3"})
	send("progress", map[string]any{"value": 2, "max": 2, "node": "3"})
	if f.fail != "" {
		send("execution_error", map[string]any{"node_id": "3", "node_type": "Hy3DPaint", "exception_message": f.fail})
		return
	}
	f.mu.Lock()
	f.done[id] = true
	f.mu.Unlock()
	send("executing", map[string]any{"node": nil})
}

func setup(t *testing.T, srv *httptest.Server) (*Provider, *store.Store, engineapi.RunRequest) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hy3d-paint.json")
	if err := os.WriteFile(path, []byte(paintWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(filepath.Join(dir, "store"))
	mesh, _ := st.PutBytes([]byte("glTF\x02\x00\x00\x00 lowpoly"))
	img, _ := st.PutBytes([]byte("\x89PNG front"))
	p := New("comfy", srv.URL, "", map[string]string{"texture": path}, false, 0, st)
	p.Poll = 5 * time.Millisecond
	req := engineapi.RunRequest{Seed: 42, Stages: []engineapi.StageSpec{{ID: "paint", Use: "texture", Model: ModelID("texture"),
		Params: map[string]any{"resolution": 1024}, Inputs: []string{mesh, img}}}}
	return p, st, req
}

func run(t *testing.T, p *Provider, req engineapi.RunRequest) (*engineapi.Run, []engineapi.Event) {
	t.Helper()
	evs := make(chan engineapi.Event, 64)
	r, err := p.Run(context.Background(), req, evs)
	close(evs)
	if err != nil {
		t.Fatal(err)
	}
	var got []engineapi.Event
	for ev := range evs {
		got = append(got, ev)
	}
	return r, got
}

func TestAStageRunsAsAWorkflowWithItsInputsAndParams(t *testing.T) {
	f, srv := newComfy(t)
	p, st, req := setup(t, srv)
	if p.Paid() || p.Kind() != "comfyui" {
		t.Fatal("a ComfyUI is free unless the user says it costs")
	}
	r, events := run(t, p, req)
	if r.Status != "succeeded" {
		t.Fatalf("run %+v", r)
	}
	out := r.Stages["paint"].Outputs[0]
	if out.Kind != "mesh/glb" || !st.Has(out.Hash) {
		t.Fatalf("output %+v", out)
	}
	if fh, _ := st.Open(out.Hash); fh != nil {
		raw, _ := io.ReadAll(fh)
		fh.Close()
		if string(raw) != outputGLB {
			t.Fatalf("stored %q", raw)
		}
	}
	node := f.prompts[0]["3"].(map[string]any)["inputs"].(map[string]any)
	if node["seed"] != float64(42) || node["resolution"] != float64(1024) || node["note"] != "seed 42" {
		t.Fatalf("filled inputs %v", node)
	}
	image := f.prompts[0]["1"].(map[string]any)["inputs"].(map[string]any)["image"].(string)
	glb := f.prompts[0]["2"].(map[string]any)["inputs"].(map[string]any)["glb"].(string)
	if !strings.HasPrefix(image, "forja-") || !strings.HasSuffix(image, ".png") || !strings.HasSuffix(glb, ".glb") {
		t.Fatalf("uploaded names %q %q", image, glb)
	}
	var progress int
	for _, ev := range events {
		if ev.Type == "stage.progress" {
			progress++
		}
	}
	if progress != 2 {
		t.Fatalf("events %+v", events)
	}
	// Inputs go up once a session.
	run(t, p, req)
	if len(f.uploads) != 2 {
		t.Fatalf("uploads %v", f.uploads)
	}
}

func TestWithoutItsSocketARunReadsTheHistory(t *testing.T) {
	f, srv := newComfy(t)
	f.noSocket = true
	p, _, req := setup(t, srv)
	if r, _ := run(t, p, req); r.Status != "succeeded" {
		t.Fatalf("run %+v", r)
	}
}

func TestComfyUIsErrorsReachTheStage(t *testing.T) {
	f, srv := newComfy(t)
	p, _, req := setup(t, srv)
	f.refuse = true
	r, _ := run(t, p, req)
	if r.Status != "failed" || !strings.Contains(*r.Error, "node 2 (Hy3DLoadMesh): Value not in list") {
		t.Fatalf("validation: %+v", *r.Error)
	}
	f.refuse, f.fail = false, "CUDA out of memory"
	r, _ = run(t, p, req)
	if r.Status != "failed" || !strings.Contains(*r.Error, "Hy3DPaint failed: CUDA out of memory") {
		t.Fatalf("execution: %+v", *r.Error)
	}
}

func TestModelsAreTheMappedWorkflows(t *testing.T) {
	_, srv := newComfy(t)
	p, _, _ := setup(t, srv)
	models, _ := p.Models(context.Background())
	if len(models) != 1 || models[0].ID != "comfyui-texture" || !models[0].Does("texture") || !models[0].Ready() {
		t.Fatalf("models %+v", models)
	}
	if len(models[0].Weights.Revision) != 64 || models[0].Name != "hy3d-paint (ComfyUI)" {
		t.Fatalf("the workflow's content keys its results: %+v", models[0].Weights)
	}
	info, err := p.Info(context.Background())
	if err != nil || info.Version != "ComfyUI 0.3.60" || len(info.Stages) != 0 {
		t.Fatalf("info %+v %v", info, err)
	}

	broken := New("c", srv.URL, "", map[string]string{"shape": filepath.Join(t.TempDir(), "missing.json")}, false, 0, nil)
	models, _ = broken.Models(context.Background())
	if len(models) != 1 || models[0].Status != "unsupported" || models[0].Ready() {
		t.Fatalf("a workflow that cannot load: %+v", models)
	}
}

func TestATokenNeverCrossesTheNetworkInTheClear(t *testing.T) {
	_, srv := newComfy(t)
	st, _ := store.Open(filepath.Join(t.TempDir(), "store"))
	ctx := context.Background()
	if _, err := New("c", "http://192.0.2.10:8188", "proxy-fake-token", nil, false, 0, st).Stats(ctx); !errors.Is(err, errCleartext) {
		t.Fatalf("a token over plain http to another machine: %v", err)
	}
	if New("c", "http://192.0.2.10:8188", "", nil, false, 0, st).cleartext() ||
		New("c", "https://comfy.example.test", "proxy-fake-token", nil, false, 0, st).cleartext() {
		t.Fatal("refused a ComfyUI that takes no token, or one behind https")
	}
	// On this computer the token never leaves it.
	if _, err := New("c", srv.URL, "proxy-fake-token", nil, false, 0, st).Stats(ctx); err != nil {
		t.Fatalf("a token to this computer: %v", err)
	}
}

func TestParseRefusesWorkflowsForjaCannotFill(t *testing.T) {
	for name, tc := range map[string]struct{ use, json, want string }{
		"editor format": {"shape", `{"nodes": [], "links": []}`, "Export (API)"},
		"no image":      {"shape", `{"1": {"class_type": "X", "inputs": {}}}`, "{{image}}"},
		"no mesh":       {"texture", `{"1": {"class_type": "X", "inputs": {"i": "{{image}}"}}}`, "{{mesh}}"},
		"a built-in":    {"mesh.decimate", `{}`, "not \"mesh.decimate\""},
	} {
		if _, err := Parse(tc.use, "w", []byte(tc.json)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	w, err := Parse("shape", "mv", []byte(`{"1": {"class_type": "X", "inputs": {"a": "{{image}}", "b": "{{image:back}}", "c": "{{image:left}}"}}}`))
	if err != nil || strings.Join(w.Views, ",") != "left,back" {
		t.Fatalf("views %v %v", w, err)
	}
	if _, err := fill(w.Graph, map[string]any{"image": "a.png"}); err == nil || !strings.Contains(err.Error(), "{{image:back}}") {
		t.Fatalf("a missing view: %v", err)
	}
}
