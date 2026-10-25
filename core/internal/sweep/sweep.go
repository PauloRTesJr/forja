// Package sweep removes what the object store holds that the library no longer shows or needs:
// the stage outputs of finished jobs, and the files of deleted assets. It runs when the core
// starts, after the last running job ends and after an asset is deleted, never while a job runs.
package sweep

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"forja/internal/library"
	"forja/internal/store"
)

// Grace keeps an object for a while after it was last stored or used: images picked on
// Generate wait in the store before their job exists, and another computer may have just
// checked that a shared engine holds one.
const Grace = time.Hour

// Gate runs fn only while no job runs, and says whether it did.
type Gate interface {
	WhenIdle(fn func()) bool
}

// Swept is what one sweep removed.
type Swept struct {
	Objects int
	Bytes   int64
}

// leftovers removes every object the library does not keep and nothing used within Grace.
func leftovers(ctx context.Context, db *library.DB, st *store.Store, now time.Time) (Swept, error) {
	var s Swept
	kept, err := db.Kept(ctx)
	if err != nil {
		return s, err
	}
	var gone []store.Object
	err = st.Walk(func(o store.Object) error {
		if !kept[o.Ref] && now.Sub(o.Used) >= Grace {
			gone = append(gone, o)
		}
		return ctx.Err()
	})
	if err != nil {
		return s, err
	}
	for _, o := range gone {
		if ctx.Err() != nil {
			break
		}
		if err := st.Remove(o.Ref); err != nil {
			continue // held open elsewhere (Windows); it goes next time
		}
		s.Objects++
		s.Bytes += o.Size
	}
	return s, ctx.Err()
}

// Sweeper sweeps whenever nudged, one sweep at a time.
type Sweeper struct {
	db   *library.DB
	st   *store.Store
	gate Gate
	log  *slog.Logger

	nudge  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
	now    func() time.Time
}

func New(db *library.DB, st *store.Store, gate Gate, log *slog.Logger) *Sweeper {
	ctx, cancel := context.WithCancel(context.Background())
	return &Sweeper{db: db, st: st, gate: gate, log: log,
		nudge: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}), now: time.Now}
}

// Start sweeps once now, then whenever nudged, until Close.
func (s *Sweeper) Start() {
	s.Nudge()
	go func() {
		defer close(s.done)
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-s.nudge:
				s.sweep()
			}
		}
	}()
}

// Close stops sweeping and waits for a sweep under way to stop.
func (s *Sweeper) Close() {
	s.once.Do(s.cancel)
	<-s.done
}

// Nudge asks for a sweep soon; nudges while one is pending merge.
func (s *Sweeper) Nudge() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// sweep does nothing while jobs run: the last one to end nudges again.
func (s *Sweeper) sweep() {
	var swept Swept
	var err error
	s.gate.WhenIdle(func() { swept, err = leftovers(s.ctx, s.db, s.st, s.now()) })
	if err != nil && s.ctx.Err() == nil {
		s.log.Warn("removing leftover files", "err", err)
	}
	if swept.Objects > 0 {
		s.log.Info("removed leftover files", "objects", swept.Objects, "bytes", swept.Bytes)
	}
}
