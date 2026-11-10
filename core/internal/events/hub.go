// Package events fans core events out to every subscriber (UI WebSocket, CLI watchers).
package events

import (
	"sync"
	"sync/atomic"
)

type Event struct {
	Seq  uint64 `json:"seq"`
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Hub struct {
	mu     sync.RWMutex
	subs   map[chan Event]struct{}
	closed bool
	seq    atomic.Uint64
}

func NewHub() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

// Publish never blocks: a subscriber too slow to keep up loses progress events, never the
// publisher's time. Progress is superseded by the next update anyway; terminal state is
// re-fetchable over the API.
func (h *Hub) Publish(typ string, data any) {
	ev := Event{Seq: h.seq.Add(1), Type: typ, Data: data}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	h.mu.Lock()
	if h.closed {
		close(ch)
	} else {
		h.subs[ch] = struct{}{}
	}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		// Close took the channel already if it is no longer among the subscribers.
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Close ends every subscription, now and later: the streams that carry events to clients
// end with it, so a core that stops does not wait for them.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
