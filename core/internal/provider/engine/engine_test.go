package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
