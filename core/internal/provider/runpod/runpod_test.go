package runpod

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/store"
)

// fakeRunPod answers RunPod's serverless API (/run, /stream, /status, /cancel, /health) the
// way its documentation shows, with a worker that runs one stage and outputs a fixed mesh.
type fakeRunPod struct {
	t        *testing.T
	mesh     []byte
	meshRef  string
	volume   map[string][]byte // the network volume, by S3 path, when the test has one
	mu       sync.Mutex
	jobs     map[string]map[string]any // job id -> input
	polls    map[string]int
	hold     bool // keep jobs running until cancelled
	fail     string
	cancels  []string
	lastBody map[string]any
}

const meshBytes = "glTF\x02\x00\x00\x00 a fake mesh"

func newFake(t *testing.T) (*fakeRunPod, *httptest.Server) {
	t.Helper()
	tmp, _ := store.Open(t.TempDir())
	ref, _ := tmp.PutBytes([]byte(meshBytes))
	f := &fakeRunPod{t: t, mesh: []byte(meshBytes), meshRef: ref, jobs: map[string]map[string]any{}, polls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := APIBase
	APIBase = srv.URL + "/v2"
	t.Cleanup(func() { APIBase = old })
	return f, srv
}

func (f *fakeRunPod) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer rp-fake-key" {
		w.WriteHeader(401)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/v2/ep-fake/")
	if !ok {
		w.WriteHeader(404)
		return
	}
	op, id, _ := strings.Cut(rest, "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch op {
	case "health":
		_, _ = io.WriteString(w, `{"jobs":{"completed":3,"failed":0,"inProgress":0,"inQueue":1,"retried":0},"workers":{"idle":1,"running":0}}`)
	case "run":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody = body
		jobID := "job-" + string(rune('a'+len(f.jobs)))
		f.jobs[jobID] = body["input"].(map[string]any)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": jobID, "status": "IN_QUEUE"})
	case "stream":
		f.polls[id]++
		if f.hold {
			_, _ = io.WriteString(w, `{"status":"IN_PROGRESS","stream":[]}`)
			return
		}
		if f.fail != "" {
			_, _ = io.WriteString(w, `{"status":"FAILED","stream":[]}`)
			return
		}
		items := f.items(f.jobs[id])
		if f.polls[id] == 1 { // events first, then the result
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "IN_PROGRESS", "stream": wrap(items[:len(items)-1])})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "COMPLETED", "stream": wrap(items[len(items)-1:])})
	case "status":
		if f.fail != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "FAILED", "error": f.fail})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "COMPLETED", "delayTime": 2000,
			"executionTime": 1500, "output": f.items(f.jobs[id])})
	case "cancel":
		f.cancels = append(f.cancels, id)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "CANCELLED"})
	default:
		w.WriteHeader(404)
	}
}

func wrap(items []map[string]any) []map[string]any {
	out := make([]map[string]any, len(items))
	for i, it := range items {
		out[i] = map[string]any{"output": it}
	}
	return out
}

var described = map[string]any{
	"info":   map[string]any{"version": "0.1.0", "api_version": engineapi.APIVersion, "stages": map[string]any{"bake": "2"}},
	"models": []any{map[string]any{"id": "trellis2", "name": "TRELLIS.2", "tasks": []any{"shape"}, "status": "ready"}},
}

// items is what the worker yields for a job: events, then the result.
func (f *fakeRunPod) items(input map[string]any) []map[string]any {
	if input["op"] == "describe" {
		return []map[string]any{{"result": described}}
	}
	result := map[string]any{"info": described["info"], "models": described["models"],
		"run": map[string]any{"id": "engine-run", "status": "succeeded", "stages": map[string]any{
			"shape": map[string]any{"status": "succeeded", "seconds": 12.5,
				"outputs": []any{map[string]any{"hash": f.meshRef, "kind": "mesh/glb", "preview": true}}}}}}
	if input["outputs"] == "inline" {
		result["artifacts"] = map[string]any{f.meshRef: base64.StdEncoding.EncodeToString(f.mesh)}
	} else if f.volume != nil {
		f.volume["/vol-fake/"+mustKey(f.meshRef)] = f.mesh
	}
	return []map[string]any{
		{"event": map[string]any{"id": 1, "type": "stage.started", "data": map[string]any{"stage": "shape"}}},
		{"event": map[string]any{"id": 2, "type": "stage.finished", "data": map[string]any{"stage": "shape", "seconds": 12.5}}},
		{"event": map[string]any{"id": 3, "type": "run.finished", "data": map[string]any{"status": "succeeded"}}},
		{"result": result},
	}
}

func mustKey(ref string) string {
	k, _ := objectKey(ref)
	return k
}

func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := st.PutBytes([]byte("\x89PNG a reference image"))
	return st, ref
}

func shapeRun(ref string) engineapi.RunRequest {
	return engineapi.RunRequest{ClientRunID: "job-1-shape", Priority: "interactive", Seed: 7,
		Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape", Model: "trellis2", Inputs: []string{ref}}}}
}

func collect(t *testing.T, p *Provider, req engineapi.RunRequest) (*engineapi.Run, []string, error) {
	t.Helper()
	evs := make(chan engineapi.Event, 16)
	var types []string
	done := make(chan struct{})
	go func() {
		for ev := range evs {
			types = append(types, ev.Type)
		}
		close(done)
	}()
	run, err := p.Run(context.Background(), req, evs)
	close(evs)
	<-done
	return run, types, err
}

func TestARunWithoutAVolumeCarriesItsFilesInline(t *testing.T) {
	f, _ := newFake(t)
	st, img := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0.00031, nil, st)
	p.Poll = time.Millisecond
	if !p.Paid() || p.SharesStore() || !p.Asleep() {
		t.Fatal("a serverless endpoint is paid, remote, and asleep until asked")
	}
	if ok, _ := p.HasArtifact(context.Background(), img); ok {
		t.Fatal("nothing stays on an endpoint without a volume")
	}
	if err := p.Upload(context.Background(), img); err != nil {
		t.Fatal(err)
	}
	run, types, err := collect(t, p, shapeRun(img))
	if err != nil {
		t.Fatal(err)
	}
	input := f.jobs["job-a"]
	inline := input["inline"].(map[string]any)
	if raw, _ := base64.StdEncoding.DecodeString(inline[img].(string)); string(raw) != "\x89PNG a reference image" {
		t.Fatalf("inline input %q", raw)
	}
	if input["outputs"] != "inline" || input["limit"].(float64) != InlineLimit {
		t.Fatalf("job input %v", input)
	}
	if strings.Join(types, ",") != "stage.started,stage.finished,run.finished" {
		t.Fatalf("events %v", types)
	}
	out := run.Stages["shape"].Outputs[0].Hash
	if !st.Has(out) || p.Download(context.Background(), out) != nil {
		t.Fatal("the inline output is not in the store")
	}
	if *run.Stages["shape"].Seconds != 12.5 {
		t.Fatalf("stage seconds %v", run.Stages["shape"].Seconds)
	}
	// The run's result described the worker, so asking again costs no job.
	if p.Asleep() {
		t.Fatal("still asleep after a run")
	}
	models, err := p.Models(context.Background())
	if err != nil || len(f.jobs) != 1 {
		t.Fatalf("models %v %v, jobs %d", models, err, len(f.jobs))
	}
	if pr := models[0].Price; pr == nil || pr.Amount != 0.00031 || pr.Unit != "second" {
		t.Fatalf("price %+v", models[0].Price)
	}
}

func TestDescribingWakesOneWorkerOnce(t *testing.T) {
	f, _ := newFake(t)
	st, _ := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0, nil, st)
	p.Poll = time.Millisecond
	info, err := p.Info(context.Background())
	if err != nil || info.Stages["bake"] != "2" {
		t.Fatalf("info %+v %v", info, err)
	}
	if _, err := p.Models(context.Background()); err != nil || len(f.jobs) != 1 {
		t.Fatalf("models: %v, %d jobs", err, len(f.jobs))
	}
	if m, _ := p.Models(context.Background()); m[0].Price != nil {
		t.Fatal("no price set, none shown")
	}
}

func fakeVolume(t *testing.T, f *fakeRunPod) {
	t.Helper()
	f.volume = map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=user_fake/") {
			w.WriteHeader(403)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			f.volume[r.URL.Path], _ = io.ReadAll(r.Body)
		default:
			body, ok := f.volume[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(body)
		}
	}))
	t.Cleanup(srv.Close)
	old := VolumeEndpoint
	VolumeEndpoint = func(dc string) string {
		if dc != "eu-ro-1" && dc != "EU-RO-1" {
			t.Errorf("datacenter %q", dc)
		}
		return srv.URL
	}
	t.Cleanup(func() { VolumeEndpoint = old })
}

func TestAVolumeMovesEachFileOnceByHash(t *testing.T) {
	f, _ := newFake(t)
	fakeVolume(t, f)
	st, img := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0.00031, &Volume{ID: "vol-fake", Datacenter: "EU-RO-1",
		AccessKey: "user_fake", Secret: "rps_fake"}, st)
	p.Poll = time.Millisecond
	ctx := context.Background()
	if err := p.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Upload(ctx, img); err != nil {
		t.Fatal(err)
	}
	if string(f.volume["/vol-fake/"+mustKey(img)]) != "\x89PNG a reference image" {
		t.Fatalf("volume %v", f.volume)
	}
	if ok, err := p.HasArtifact(ctx, img); !ok || err != nil {
		t.Fatalf("has %v %v", ok, err)
	}
	f.volume["/vol-fake/"+mustKey(img)] = []byte("changed") // a second upload would overwrite it
	if err := p.Upload(ctx, img); err != nil || string(f.volume["/vol-fake/"+mustKey(img)]) != "changed" {
		t.Fatal("uploaded a file the volume already had")
	}

	run, _, err := collect(t, p, shapeRun(img))
	if err != nil {
		t.Fatal(err)
	}
	if input := f.jobs["job-a"]; input["inline"] != nil || input["outputs"] != "store" {
		t.Fatalf("a volume job carries no files: %v", input)
	}
	out := run.Stages["shape"].Outputs[0].Hash
	if st.Has(out) {
		t.Fatal("the output came without being asked for")
	}
	if err := p.Download(ctx, out); err != nil || !st.Has(out) {
		t.Fatalf("download: %v", err)
	}
}

func TestAWrongVolumeKeyFailsTheCheck(t *testing.T) {
	f, _ := newFake(t)
	fakeVolume(t, f)
	st, _ := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0, &Volume{ID: "vol-fake", Datacenter: "eu-ro-1", AccessKey: "user_other", Secret: "x"}, st)
	if err := p.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "refused the S3 key") {
		t.Fatalf("health %v", err)
	}
}

func TestCancellingARunCancelsItsJob(t *testing.T) {
	f, _ := newFake(t)
	f.hold = true
	st, img := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0, nil, st)
	p.Poll = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			f.mu.Lock()
			n := f.polls["job-a"]
			f.mu.Unlock()
			if n >= 2 {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	if _, err := p.Run(ctx, shapeRun(img), make(chan engineapi.Event, 16)); err == nil {
		t.Fatal("a cancelled run returned no error")
	}
	if len(f.cancels) != 1 || f.cancels[0] != "job-a" {
		t.Fatalf("cancels %v", f.cancels)
	}
}

func TestAFailedJobSaysWhy(t *testing.T) {
	f, _ := newFake(t)
	f.fail = "CUDA out of memory"
	st, img := newStore(t)
	p := New("rp", "ep-fake", "rp-fake-key", 0, nil, st)
	p.Poll = time.Millisecond
	if _, _, err := collect(t, p, shapeRun(img)); err == nil || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("err %v", err)
	}
}

func TestHealthNamesAWrongKeyOrEndpoint(t *testing.T) {
	newFake(t)
	st, _ := newStore(t)
	if err := New("rp", "ep-fake", "wrong", 0, nil, st).Health(context.Background()); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("wrong key: %v", err)
	}
	if err := New("rp", "ep-other", "rp-fake-key", 0, nil, st).Health(context.Background()); err == nil || !strings.Contains(err.Error(), "no serverless endpoint") {
		t.Fatalf("wrong endpoint: %v", err)
	}
	w, err := New("rp", "ep-fake", "rp-fake-key", 0, nil, st).Check(context.Background())
	if err != nil || w.Idle != 1 || w.Queued != 1 {
		t.Fatalf("workers %+v %v", w, err)
	}
}

func TestInputsTooLargeToCarryAskForTheVolume(t *testing.T) {
	newFake(t)
	st, _ := newStore(t)
	big, _ := st.PutBytes(make([]byte, InlineLimit+1))
	p := New("rp", "ep-fake", "rp-fake-key", 0, nil, st)
	if _, _, err := collect(t, p, shapeRun(big)); err == nil || !strings.Contains(err.Error(), "S3 key") {
		t.Fatalf("err %v", err)
	}
}
