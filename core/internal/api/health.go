package api

import (
	"context"
	"sync"
	"time"

	"forja/internal/plain"
	"forja/internal/provider"
)

// healthFresh is how long a provider's last check stands before the list asks again.
const healthFresh = 30 * time.Second

// healthTimeout bounds one reachability check.
const healthTimeout = 3 * time.Second

// healthRecord is what the core last learned about whether a provider answers.
type healthRecord struct {
	p       provider.Provider // a provider that was replaced starts over
	healthy bool
	latency int64
	err     error
	known   bool // a check has finished
	at      time.Time
	// done is closed when the check running now ends; nil when none runs.
	done chan struct{}
}

// healthBook remembers provider health between requests, so the list answers at once and a
// computer that is switched off costs its timeout in the background, not on every load.
type healthBook struct {
	mu  sync.Mutex
	rec map[string]*healthRecord
}

// look returns a copy of what is known of p and whether a check must start.
func (b *healthBook) look(p provider.Provider) (healthRecord, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.rec == nil {
		b.rec = map[string]*healthRecord{}
	}
	r := b.rec[p.ID()]
	if r == nil || r.p != p {
		r = &healthRecord{p: p}
		b.rec[p.ID()] = r
	}
	return *r, r.done == nil && (!r.known || time.Since(r.at) > healthFresh)
}

// check starts a reachability check of p in the background, unless one runs. It tells the app
// when the answer is in, whatever it is: the list showed "checking" until then.
func (s *Server) check(p provider.Provider) {
	b := &s.reach
	b.mu.Lock()
	r := b.rec[p.ID()]
	if r == nil || r.p != p || r.done != nil {
		b.mu.Unlock()
		return
	}
	r.done = make(chan struct{})
	done := r.done
	b.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
		defer cancel()
		start := time.Now()
		err := p.Health(ctx)
		b.mu.Lock()
		if cur := b.rec[p.ID()]; cur == r {
			r.healthy, r.err, r.latency, r.known, r.at, r.done = err == nil, err, time.Since(start).Milliseconds(), true, time.Now(), nil
		}
		b.mu.Unlock()
		close(done)
		s.Hub.Publish(EvProvidersChanged, map[string]string{"provider": p.ID()})
	}()
}

// wait returns once the check running for p, if any, has ended or ctx is done.
func (b *healthBook) wait(ctx context.Context, p provider.Provider) {
	b.mu.Lock()
	var done chan struct{}
	if r := b.rec[p.ID()]; r != nil && r.p == p {
		done = r.done
	}
	b.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// sentence is the record's failure as one line for people.
func (r healthRecord) sentence(label string) string { return plain.Provider(label, r.err) }
