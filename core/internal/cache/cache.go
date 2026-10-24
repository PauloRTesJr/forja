// Package cache keeps the reusable part of the object store, the stage cache, under the size
// the user set in general.cache_max_gb (docs/decisions.md, decision 20). What the library
// shows or still needs is never evicted, and nothing is evicted while a job runs.
package cache

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"forja/internal/config"
	"forja/internal/events"
	"forja/internal/library"
	"forja/internal/store"
)

// EvChanged tells clients how the store's bytes divide after an eviction removed something.
const EvChanged = "cache.changed"

// Grace keeps an object safe for a while after it was last stored or used: images picked on
// Generate wait in the store before their job exists, and another computer may have just
// checked that a shared engine holds one.
const Grace = time.Hour

// ErrBusy says that jobs are running, and eviction waits for them.
var ErrBusy = errors.New("jobs are running: the cache can be cleared once they finish")

// Usage is how the object store's bytes divide.
type Usage struct {
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
	// LibraryBytes are what the library shows or needs: assets, job images, unfinished jobs.
	LibraryBytes int64 `json:"library_bytes"`
	// CacheBytes are stage outputs and leftovers eviction may remove; the limit counts these.
	CacheBytes int64 `json:"cache_bytes"`
	// LimitBytes is general.cache_max_gb in bytes; 0 means no limit.
	LimitBytes int64 `json:"limit_bytes"`
}

// Freed is what one eviction removed.
type Freed struct {
	Objects int   `json:"objects"`
	Bytes   int64 `json:"bytes"`
	// Entries are the stage cache entries forgotten with them.
	Entries int `json:"entries"`
}

// Gate runs fn only while no job runs, and says whether it did.
type Gate interface {
	WhenIdle(fn func()) bool
}

// scan reads the store, divides it into library and cache, and lists the cache's objects
// least recently used first.
func scan(ctx context.Context, db *library.DB, st *store.Store) (Usage, []store.Object, error) {
	var u Usage
	kept, err := db.Kept(ctx)
	if err != nil {
		return u, nil, err
	}
	var reusable []store.Object
	err = st.Walk(func(o store.Object) error {
		u.Objects++
		u.Bytes += o.Size
		if kept[o.Ref] {
			u.LibraryBytes += o.Size
		} else {
			u.CacheBytes += o.Size
			reusable = append(reusable, o)
		}
		return ctx.Err()
	})
	sort.Slice(reusable, func(i, j int) bool { return reusable[i].Used.Before(reusable[j].Used) })
	return u, reusable, err
}

// evict removes cache objects, least recently used first, until the cache holds at most
// target bytes; target 0 removes all it may. Objects used within Grace stay.
func evict(ctx context.Context, db *library.DB, st *store.Store, target int64, now time.Time) (Freed, Usage, error) {
	u, reusable, err := scan(ctx, db, st)
	if err != nil {
		return Freed{}, u, err
	}
	var victims []store.Object
	gone := map[string]bool{}
	left := u.CacheBytes
	for _, o := range reusable {
		if left <= target || now.Sub(o.Used) < Grace {
			break // in use order: the rest were used more recently
		}
		victims = append(victims, o)
		gone[o.Ref] = true
		left -= o.Size
	}
	if len(victims) == 0 {
		return Freed{}, u, nil
	}
	// The entries go first: an interruption then leaves only files nothing points at, which
	// the next eviction removes, never an entry that points at a missing file.
	var f Freed
	if f.Entries, err = db.ForgetOutputs(ctx, gone); err != nil {
		return f, u, err
	}
	for _, o := range victims {
		if ctx.Err() != nil {
			break
		}
		if err := st.Remove(o.Ref); err != nil {
			continue // held open elsewhere (Windows); it goes next time
		}
		f.Objects++
		f.Bytes += o.Size
	}
	u.Objects -= f.Objects
	u.Bytes -= f.Bytes
	u.CacheBytes -= f.Bytes
	return f, u, ctx.Err()
}

// Keeper trims the cache when it grows past its limit: after the last running job ends, when
// the core starts, and when the limit or the library changes.
type Keeper struct {
	db    *library.DB
	st    *store.Store
	gate  Gate
	limit func() int64
	hub   *events.Hub
	log   *slog.Logger

	mu     sync.Mutex // one eviction at a time
	nudge  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	now    func() time.Time
}

// Limit reads general.cache_max_gb from the configuration, in bytes, each time it is asked,
// so a new limit applies without a restart.
func Limit(load func(project string) (*config.Loaded, error)) func() int64 {
	return func() int64 {
		cfg, err := load("")
		if err != nil {
			return 0
		}
		return cfg.General.CacheMaxGB << 30
	}
}

// New makes a keeper; limit returns the cache's limit in bytes, 0 for none.
func New(db *library.DB, st *store.Store, gate Gate, limit func() int64, hub *events.Hub, log *slog.Logger) *Keeper {
	ctx, cancel := context.WithCancel(context.Background())
	return &Keeper{db: db, st: st, gate: gate, limit: limit, hub: hub, log: log,
		nudge: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}), now: time.Now}
}

// Start trims whenever nudged, until Close.
func (k *Keeper) Start() {
	go func() {
		defer close(k.done)
		for {
			select {
			case <-k.ctx.Done():
				return
			case <-k.nudge:
				k.trim()
			}
		}
	}()
}

// Close stops trimming and waits for an eviction under way to stop.
func (k *Keeper) Close() {
	k.cancel()
	<-k.done
}

// Nudge asks for a trim soon; nudges while one is pending merge.
func (k *Keeper) Nudge() {
	select {
	case k.nudge <- struct{}{}:
	default:
	}
}

// Usage reads how the store's bytes divide now.
func (k *Keeper) Usage(ctx context.Context) (Usage, error) {
	u, _, err := scan(ctx, k.db, k.st)
	u.LimitBytes = k.limit()
	return u, err
}

// Clear removes every cache object it may, at once. It refuses while jobs run.
func (k *Keeper) Clear(ctx context.Context) (Freed, Usage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var f Freed
	var u Usage
	var err error
	if !k.gate.WhenIdle(func() { f, u, err = evict(ctx, k.db, k.st, 0, k.now()) }) {
		return f, u, ErrBusy
	}
	u.LimitBytes = k.limit()
	if err == nil {
		k.hub.Publish(EvChanged, u)
	}
	return f, u, err
}

// trim brings the cache to nine tenths of its limit once it is past it, so it does not run
// again after every job. While jobs run it does nothing: the last one to end nudges again.
func (k *Keeper) trim() {
	limit := k.limit()
	if limit <= 0 {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	var f Freed
	var u Usage
	var err error
	k.gate.WhenIdle(func() {
		u, _, err = scan(k.ctx, k.db, k.st)
		if err == nil && u.CacheBytes > limit {
			f, u, err = evict(k.ctx, k.db, k.st, limit/10*9, k.now())
		}
	})
	if err != nil && k.ctx.Err() == nil {
		k.log.Warn("trimming the cache", "err", err)
	}
	if f.Objects > 0 {
		u.LimitBytes = limit
		k.log.Info("trimmed the cache", "objects", f.Objects, "bytes", f.Bytes, "entries", f.Entries)
		k.hub.Publish(EvChanged, u)
	}
}
