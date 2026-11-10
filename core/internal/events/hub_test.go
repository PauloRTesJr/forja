package events

import "testing"

func TestClosingTheHubEndsEverySubscriptionForGood(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe()
	h.Publish("job.updated", 1)
	h.Close()
	if ev, ok := <-ch; !ok || ev.Type != "job.updated" || ev.Seq != 1 {
		t.Fatalf("the event published before closing was lost: %+v %v", ev, ok)
	}
	if _, ok := <-ch; ok {
		t.Fatal("the subscription is still open after Close")
	}
	unsubscribe() // must not close the channel a second time
	unsubscribe()
	h.Publish("job.updated", 2) // nobody is left to hear it
	late, stop := h.Subscribe()
	defer stop()
	if _, ok := <-late; ok {
		t.Fatal("a subscription made after Close is open")
	}
}

func TestASlowSubscriberLosesEventsWithoutHoldingThePublisher(t *testing.T) {
	h := NewHub()
	slow, stopSlow := h.Subscribe()
	defer stopSlow()
	for i := range 300 { // more than a subscriber's buffer
		h.Publish("stage.progress", i)
	}
	first := <-slow
	var last Event
	n := 1
	for len(slow) > 0 {
		last = <-slow
		n++
	}
	// The gap in seq is how a client can tell it missed events and must read the state again.
	if first.Seq != 1 || n >= 300 || last.Seq != uint64(n) {
		t.Fatalf("got %d events, first seq %d, last seq %d", n, first.Seq, last.Seq)
	}
	h.Publish("job.finished", nil)
	if ev := <-slow; ev.Seq != 301 {
		t.Fatalf("after the gap, seq %d, want 301", ev.Seq)
	}
}
