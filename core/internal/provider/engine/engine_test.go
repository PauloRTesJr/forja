package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"forja/internal/engineapi"
	"forja/internal/provider"
)

// A gateway that ends every event stream after one event (as one that closes long requests
// does) still delivers a run of many events: each reconnect resumes after the last one seen.
func TestStreamsThatEndEarlyResumeWithoutGivingUp(t *testing.T) {
	const events = 12
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			_, _ = w.Write([]byte(`{"id":"run-1","status":"queued","stages":{}}`))
		case r.URL.Path == "/v1/runs/run-1/events":
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			typ := "stage.progress"
			if after+1 == events {
				typ = "run.finished"
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: {\"stage\":\"shape\"}\n\n", after+1, typ)
		case r.URL.Path == "/v1/runs/run-1":
			_, _ = w.Write([]byte(`{"id":"run-1","status":"succeeded","stages":{}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p := New("modal", provider.KindServerless, true, false, Static(srv.URL, "tok"), nil)
	evs := make(chan engineapi.Event, events)
	run, err := p.Run(context.Background(), engineapi.RunRequest{Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape"}}}, evs)
	if err != nil || run.Status != "succeeded" || len(evs) != events {
		t.Fatalf("run %+v, %v, %d events", run, err, len(evs))
	}
}

// A run whose events stop coming is cancelled before Run gives up on it: a Modal gateway or a
// rented pod would otherwise go on billing for it.
func TestARunWhoseStreamKeepsFailingIsCancelled(t *testing.T) {
	old := reconnect
	reconnect = time.Millisecond
	t.Cleanup(func() { reconnect = old })
	var mu sync.Mutex
	var deleted []string
	streams := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			_, _ = w.Write([]byte(`{"id":"run-1","status":"queued","stages":{}}`))
		case r.URL.Path == "/v1/runs/run-1/events":
			streams++
			w.WriteHeader(http.StatusBadGateway)
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			_, _ = w.Write([]byte(`{"id":"run-1","status":"cancelled","stages":{}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p := New("modal", provider.KindServerless, true, false, Static(srv.URL, "tok"), nil)
	_, err := p.Run(context.Background(), engineapi.RunRequest{Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape"}}},
		make(chan engineapi.Event, 8))
	mu.Lock()
	defer mu.Unlock()
	if err == nil || streams != 6 {
		t.Fatalf("err %v after %d streams", err, streams)
	}
	if len(deleted) != 1 || deleted[0] != "/v1/runs/run-1" {
		t.Fatalf("cancelled %v", deleted)
	}
}

func TestAPricedEngineGivesItsModelsAPricePerSecond(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"triposg","tasks":["shape"]}]`))
	}))
	defer srv.Close()
	p := New("pod", provider.KindEngine, true, false, Static(srv.URL, "tok"), nil)
	p.Price(0.0002)
	models, err := p.Models(context.Background())
	if err != nil || models[0].Price == nil || models[0].Price.Amount != 0.0002 || models[0].Price.Unit != "second" {
		t.Fatalf("models %+v %v", models, err)
	}
}

// A LAN computer that is switched off does not refuse the connection, it never answers. The
// operating system gives up after 20 s or more; nothing in the app should wait that long.
func TestConnectingToAnEngineThatNeverAnswersGivesUpSoon(t *testing.T) {
	old := dialTimeout
	dialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dialTimeout = old })
	// 192.0.2.0/24 is reserved for documentation (RFC 5737): nothing answers there.
	p := New("lan", provider.KindEngine, false, false, Static("http://192.0.2.1:7861", "tok"), nil)
	began := time.Now()
	if err := p.Health(context.Background()); err == nil {
		t.Skip("something answers at 192.0.2.1 on this network")
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("gave up after %s", took.Round(time.Millisecond))
	}
}

// crashing is an engine that dies in the middle of its first `lose` runs: the run's events
// stop, and the engine that comes back does not know the run.
func crashing(t *testing.T, lose int) (srv *httptest.Server, posts, asked *int) {
	t.Helper()
	old := reconnect
	reconnect = time.Millisecond
	t.Cleanup(func() { reconnect = old })
	var mu sync.Mutex
	posts, asked = new(int), new(int)
	streams := map[string]int{}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := fmt.Sprintf("run-%d", *posts)
		lost := *posts <= lose
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			*posts++
			fmt.Fprintf(w, `{"id":"run-%d","status":"queued","stages":{}}`, *posts)
		case r.URL.Path == "/v1/runs/"+id+"/events" && lost:
			streams[id]++
			if streams[id] == 1 {
				// One event, then the connection drops, as when the process dies.
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "id: 1\nevent: stage.started\ndata: {\"stage\":\"shape\"}\n\n")
				return
			}
			*asked++
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, `{"title":"Not Found","status":404,"detail":"no run %s"}`, id)
		case r.URL.Path == "/v1/runs/"+id+"/events":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "id: 1\nevent: stage.started\ndata: {\"stage\":\"shape\"}\n\nid: 2\nevent: run.finished\ndata: {}\n\n")
		case r.URL.Path == "/v1/runs/"+id:
			fmt.Fprintf(w, `{"id":"%s","status":"succeeded","stages":{}}`, id)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, posts, asked
}

// The engine crashed in the middle of a run and was started again. The core asked the new
// engine for the old run six times, then failed the job with "event stream for run ... kept
// failing: engine: Not Found (404)", though the engine was back and could have run it.
func TestARunLostWhenTheEngineDiedIsSentOnceMore(t *testing.T) {
	srv, posts, asked := crashing(t, 1)
	p := New("local", provider.KindLocal, false, true, Static(srv.URL, "tok"), nil)
	run, err := p.Run(context.Background(), engineapi.RunRequest{Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape"}}},
		make(chan engineapi.Event, 8))
	if err != nil || run.Status != "succeeded" || run.ID != "run-2" {
		t.Fatalf("run %+v, %v", run, err)
	}
	if *posts != 2 || *asked != 1 {
		t.Fatalf("posted %d times, asked the restarted engine for the lost run %d times; want 2 and 1", *posts, *asked)
	}
}

func TestARunLostTwiceIsReportedAsLost(t *testing.T) {
	srv, posts, _ := crashing(t, 2)
	p := New("local", provider.KindLocal, false, true, Static(srv.URL, "tok"), nil)
	_, err := p.Run(context.Background(), engineapi.RunRequest{Stages: []engineapi.StageSpec{{ID: "shape", Use: "shape"}}},
		make(chan engineapi.Event, 8))
	if !errors.Is(err, provider.ErrRunLost) || *posts != 2 {
		t.Fatalf("got %v after %d posts, want ErrRunLost after 2", err, *posts)
	}
	// The runner moves a stage to another provider only when this one did not answer for it.
	var answered interface{ StatusCode() int }
	if errors.As(err, &answered) {
		t.Fatalf("a lost run reads as the engine's refusal (status %d), so its stages would not fall back", answered.StatusCode())
	}
}
