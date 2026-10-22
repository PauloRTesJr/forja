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
	mu   sync.RWMutex
	subs map[chan Event]struct{}
	seq  atomic.Uint64
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
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}
