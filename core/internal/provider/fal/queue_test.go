package fal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// queueFake is fal's queue for one endpoint, scripted per test: how the status stream behaves,
// the statuses polls see in turn (the last repeats), and server errors a path answers before it
// works.
type queueFake struct {
	*httptest.Server
	mu sync.Mutex
	// stream is "complete" (the default), "drop" (a 500), "silent" (open, never a line) or
	// "running" (the model takes the request, then the stream ends).
	stream string
	polls  []string
	fail   map[string]int
	code   map[string]int
	// result replaces the default answer, a GLB in model_glb.
	result map[string]any
	// elsewhere, when set, is the host the submission's addresses point at.
	elsewhere string

	submits, cancels, polled int
}

func newQueue(t *testing.T) *queueFake {
	t.Helper()
	q := &queueFake{fail: map[string]int{}, code: map[string]int{}}
	q.Server = httptest.NewServer(http.HandlerFunc(q.serve))
	t.Cleanup(q.Close)
	return q
}

const reqBase = "/fal-ai/fake/requests/r1"

func (q *queueFake) serve(w http.ResponseWriter, r *http.Request) {
	q.mu.Lock()
	key := r.Method + " " + r.URL.Path
	if q.fail[key] > 0 {
		q.fail[key]--
		code := q.code[key]
		if code == 0 {
			code = 500
		}
		if r.Method == http.MethodPost {
			q.submits++
		}
		q.mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"detail":"try again"}`))
		return
	}
	q.mu.Unlock()
	switch {
	case r.URL.Path == "/v1/models":
		in := map[string]any{"type": "object", "required": []any{"image_url"}, "properties": props("image_url", str, "seed", seed)}
		out := map[string]any{"type": "object", "properties": props("model_glb", fileRef)}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"endpoint_id": "fal-ai/fake-3d",
			"metadata": map[string]any{"display_name": "Fake 3D"}, "openapi": schema("fal-ai/fake-3d", in, out)}}})
	case r.URL.Path == "/v1/models/pricing":
		_ = json.NewEncoder(w).Encode(map[string]any{"prices": []any{map[string]any{"endpoint_id": "fal-ai/fake-3d",
			"unit_price": 0.3, "unit": "generation", "currency": "USD"}}})
	case r.Method == http.MethodPost && r.URL.Path == "/fal-ai/fake-3d":
		q.mu.Lock()
		q.submits++
		host := q.URL
		if q.elsewhere != "" {
			host = q.elsewhere
		}
		q.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"request_id": "r1", "status_url": host + reqBase + "/status",
			"response_url": host + reqBase, "cancel_url": host + reqBase + "/cancel"})
	case r.URL.Path == reqBase+"/status/stream":
		q.mu.Lock()
		mode := q.stream
		q.mu.Unlock()
		switch mode {
		case "drop":
			w.WriteHeader(500)
		case "silent":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "running":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\n", `{"status":"IN_PROGRESS"}`)
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			for _, s := range []string{`{"status":"IN_QUEUE","queue_position":2}`, `{"status":"IN_PROGRESS"}`, `{"status":"COMPLETED"}`} {
				fmt.Fprintf(w, "data: %s\n\n", s)
			}
		}
	case r.URL.Path == reqBase+"/status":
		q.mu.Lock()
		st := "COMPLETED"
		if len(q.polls) > 0 {
			st = q.polls[min(q.polled, len(q.polls)-1)]
		}
		q.polled++
		q.mu.Unlock()
		if st != "COMPLETED" {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": st, "queue_position": 0})
	case r.Method == http.MethodPut && r.URL.Path == reqBase+"/cancel":
		q.mu.Lock()
		q.cancels++
		q.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	case r.URL.Path == reqBase:
		q.mu.Lock()
		result := q.result
		q.mu.Unlock()
		if result == nil {
			result = map[string]any{"model_glb": map[string]any{"url": q.URL + "/files/out.glb"}}
		}
		_ = json.NewEncoder(w).Encode(result)
	case r.URL.Path == "/files/out.glb":
		_, _ = w.Write([]byte("glTF\x02\x00\x00\x00 fake mesh"))
	default:
		w.WriteHeader(404)
	}
}

func (q *queueFake) counts() (submits, cancels int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.submits, q.cancels
}

// quick shortens every wait for one test.
func quick(t *testing.T) {
	t.Helper()
	r, p, s, w := retryBase, pollMax, streamIdle, maxWait
	retryBase, pollMax, streamIdle, maxWait = time.Millisecond, 5*time.Millisecond, time.Second, 5*time.Second
	t.Cleanup(func() { retryBase, pollMax, streamIdle, maxWait = r, p, s, w })
}

// runOnce runs one shape stage against q and returns the run, its error and the events it sent.
func runOnce(t *testing.T, ctx context.Context, q *queueFake) (*engineapi.Run, error, []engineapi.Event) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("fal", key, []string{"fal-ai/fake-3d"}, st)
	p.API, p.Queue = q.URL, q.URL
	img, _ := st.PutBytes([]byte("\x89PNG fake image"))
	events := make(chan engineapi.Event, 64)
	run, err := p.Run(ctx, engineapi.RunRequest{Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape", Model: "fal-ai/fake-3d",
		Inputs: []string{img}}}}, events)
	close(events)
	var evs []engineapi.Event
	for ev := range events {
		evs = append(evs, ev)
	}
	return run, err, evs
}

func types(evs []engineapi.Event) string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.Type)
	}
	return strings.Join(out, ",")
}

func messages(evs []engineapi.Event) string {
	var out []string
	for _, ev := range evs {
		if ev.Type == "stage.log" {
			out = append(out, fmt.Sprint(ev.Data["message"]))
		}
	}
	return strings.Join(out, " | ")
}

func TestFailedPollsResultsAndDownloadsAreTriedAgain(t *testing.T) {
	quick(t)
	q := newQueue(t)
	q.stream, q.polls = "drop", []string{"IN_QUEUE", "IN_PROGRESS", "COMPLETED"}
	q.fail["GET "+reqBase+"/status"], q.code["GET "+reqBase+"/status"] = 2, 503
	q.fail["GET "+reqBase] = 1
	q.fail["GET /files/out.glb"], q.code["GET /files/out.glb"] = 1, 502
	run, err, evs := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("run %+v %v", run, err)
	}
	if submits, _ := q.counts(); submits != 1 {
		t.Fatalf("%d submissions for one generation", submits)
	}
	if got := strings.Count(types(evs), "stage.billed"); got != 1 {
		t.Fatalf("billed %d times: %s", got, types(evs))
	}
	if got := messages(evs); got != "Waiting in fal.ai's queue, position 1 | Running on fal.ai" {
		t.Fatalf("progress %q", got)
	}
}

func TestASilentStatusStreamGivesWayToPolling(t *testing.T) {
	quick(t)
	streamIdle = 50 * time.Millisecond
	q := newQueue(t)
	q.stream = "silent"
	start := time.Now()
	run, err, _ := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "succeeded" {
		t.Fatalf("run %+v %v", run, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("waited %s on a silent stream", time.Since(start))
	}
}

func TestAGenerationThatNeverFinishesIsCancelled(t *testing.T) {
	quick(t)
	maxWait = 200 * time.Millisecond
	q := newQueue(t)
	q.stream, q.polls = "drop", []string{"IN_QUEUE"}
	run, err, evs := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "did not finish") {
		t.Fatalf("run %+v %v", run, err)
	}
	if _, cancels := q.counts(); cancels != 1 {
		t.Fatalf("cancelled %d times", cancels)
	}
	// It never left the queue, so fal bills nothing.
	if strings.Contains(types(evs), "stage.billed") {
		t.Fatalf("billed a request that never ran: %s", types(evs))
	}
}

func TestCancellingARunCancelsItsRequest(t *testing.T) {
	quick(t)
	for _, tc := range []struct {
		status string
		billed bool
	}{{"IN_QUEUE", false}, {"IN_PROGRESS", true}} {
		q := newQueue(t)
		q.stream, q.polls = "drop", []string{tc.status}
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
				q.mu.Lock()
				polled := q.polled
				q.mu.Unlock()
				if polled > 0 {
					break
				}
			}
			cancel()
		}()
		run, err, evs := runOnce(t, ctx, q)
		if err != nil || run.Status != "cancelled" {
			t.Fatalf("%s: run %+v %v", tc.status, run, err)
		}
		if _, cancels := q.counts(); cancels != 1 {
			t.Fatalf("%s: cancelled %d times", tc.status, cancels)
		}
		// fal cannot stop a request its model already runs: that one is billed.
		if got := strings.Contains(types(evs), "stage.billed"); got != tc.billed {
			t.Fatalf("%s: billed %v, want %v", tc.status, got, tc.billed)
		}
	}
}

func TestASubmissionIsSentAgainOnlyWhenFalDidNotTakeIt(t *testing.T) {
	quick(t)
	q := newQueue(t)
	q.fail["POST /fal-ai/fake-3d"], q.code["POST /fal-ai/fake-3d"] = 1, 429
	if run, err, _ := runOnce(t, context.Background(), q); err != nil || run.Status != "succeeded" {
		t.Fatalf("after a rate limit: %+v %v", run, err)
	}
	if submits, _ := q.counts(); submits != 2 {
		t.Fatalf("%d submissions after one rate limit", submits)
	}
	// A server error may come after fal queued the generation: sending it again could pay twice.
	q = newQueue(t)
	q.fail["POST /fal-ai/fake-3d"] = 1
	if run, err, _ := runOnce(t, context.Background(), q); err != nil || run.Status != "failed" {
		t.Fatalf("after a server error: %+v %v", run, err)
	}
	if submits, _ := q.counts(); submits != 1 {
		t.Fatalf("%d submissions after a server error", submits)
	}
}

func TestTheKeyGoesOnlyToFalsQueue(t *testing.T) {
	quick(t)
	var mu sync.Mutex
	var leaked []string
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		mu.Lock()
		leaked = append(leaked, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
	}))
	t.Cleanup(other.Close)
	q := newQueue(t)
	q.elsewhere = other.URL
	run, err, _ := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "failed" || !strings.Contains(*run.Error, "unexpected address") {
		t.Fatalf("run %+v %v", run, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) > 0 {
		t.Fatalf("requests went to another host: %v", leaked)
	}
}

// A request Forja loses track of while the model runs it is cancelled and billed: fal goes on
// with it, and charges for it, either way.
func TestARunningRequestWhoseStatusIsLostIsCancelledAndBilled(t *testing.T) {
	quick(t)
	q := newQueue(t)
	q.stream = "running"
	q.fail["GET "+reqBase+"/status"] = 100
	run, err, evs := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "failed" {
		t.Fatalf("run %+v %v", run, err)
	}
	if _, cancels := q.counts(); cancels != 1 {
		t.Fatalf("cancelled %d times", cancels)
	}
	if got := strings.Count(types(evs), "stage.billed"); got != 1 {
		t.Fatalf("billed %d times: %s", got, types(evs))
	}
}

func TestAFinishedGenerationWhoseResultCannotBeReadIsBilled(t *testing.T) {
	quick(t)
	q := newQueue(t)
	q.fail["GET "+reqBase] = 100
	run, err, evs := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "failed" {
		t.Fatalf("run %+v %v", run, err)
	}
	if got := strings.Count(types(evs), "stage.billed"); got != 1 {
		t.Fatalf("billed %d times: %s", got, types(evs))
	}
	// fal finished it: there is nothing to cancel.
	if _, cancels := q.counts(); cancels != 0 {
		t.Fatalf("cancelled %d times", cancels)
	}
}

// The job runner moves a stage elsewhere only when its provider could not be reached: fal.ai
// refusing a call says it answered.
func TestFalsRefusalsCarryTheirStatus(t *testing.T) {
	q := newQueue(t)
	q.fail["GET /v1/models/pricing"], q.code["GET /v1/models/pricing"] = 1, 401
	p := New("fal", key, []string{"fal-ai/fake-3d"}, nil)
	p.API, p.Queue = q.URL, q.URL
	var answered interface{ StatusCode() int }
	if err := p.Health(context.Background()); !errors.As(err, &answered) || answered.StatusCode() != 401 {
		t.Fatalf("health %v", err)
	}
}

func TestAPaidResultThatCannotBeImportedIsStillBilled(t *testing.T) {
	quick(t)
	q := newQueue(t)
	q.result = map[string]any{"video": map[string]any{"url": "https://example.invalid/x.mp4"}}
	run, err, evs := runOnce(t, context.Background(), q)
	if err != nil || run.Status != "failed" || !strings.Contains(*run.Error, "no mesh") {
		t.Fatalf("run %+v %v", run, err)
	}
	if !strings.Contains(types(evs), "stage.billed") {
		t.Fatalf("a generation fal finished was not billed: %s", types(evs))
	}
}
