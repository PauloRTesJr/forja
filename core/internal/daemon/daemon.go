// Package daemon wires the core together and serves it on the local socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"forja/internal/api"
	"forja/internal/catalog"
	"forja/internal/config"
	"forja/internal/events"
	"forja/internal/ipc"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/provider"
	"forja/internal/provider/engine"
	"forja/internal/secrets"
	"forja/internal/store"
)

type Options struct {
	Version string
	Address string // socket path or pipe name; "" for the default
	Log     *slog.Logger
}

type Core struct {
	Address string
	server  *http.Server
	engine  *localengine.Manager
	db      *library.DB
	lock    *flock.Flock
	done    chan struct{}
	once    sync.Once
}

// ErrAlreadyRunning means another core owns this data directory.
var ErrAlreadyRunning = errors.New("another forja core is running for this user")

func Start(opts Options) (*Core, error) {
	base, err := config.Load("", nil)
	if err != nil {
		return nil, err
	}
	dataDir := base.DataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	// One core per user: two would each start an engine and fight over the GPU.
	lock := flock.New(filepath.Join(dataDir, "core.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAlreadyRunning
	}
	c, err := start(opts, base, dataDir)
	if err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	c.lock = lock
	return c, nil
}

func start(opts Options, base *config.Loaded, dataDir string) (*Core, error) {
	log := opts.Log
	st, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		return nil, err
	}
	db, err := library.Open(dataDir)
	if err != nil {
		return nil, err
	}
	_ = db.FailUnfinished(context.Background())

	cfgFn := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	eng := localengine.New(base, st.Root, filepath.Join(dataDir, "logs"), log)
	providers := provider.NewRegistry()
	providers.Add(engine.New(jobs.LocalProvider, provider.KindLocal, false, true, eng.Endpoint, st))
	for _, pc := range base.Providers {
		switch provider.Kind(pc.Kind) {
		case provider.KindEngine:
			token := ""
			if pc.Secret != "" {
				if token, err = secrets.Get(pc.Secret); err != nil {
					log.Warn("provider secret unavailable", "provider", pc.ID, "secret", pc.Secret, "err", err)
				}
			}
			providers.Add(engine.New(pc.ID, provider.KindEngine, pc.Paid, false, engine.Static(pc.URL, token), st))
		default:
			log.Warn("provider kind not implemented yet; skipping", "provider", pc.ID, "kind", pc.Kind)
		}
	}

	hub := events.NewHub()
	cat := catalog.New(providers, db,
		func(id string) bool { return id == jobs.LocalProvider && !eng.Running() },
		func(id string) { hub.Publish(api.EvModelsChanged, map[string]string{"provider": id}) })
	// Models installed while the engine was stopped show up as soon as it runs again.
	eng.Started = func() { _, _ = cat.Ask(context.Background(), jobs.LocalProvider) }
	runner := jobs.NewRunner(db, st, hub, providers, cat, cfgFn, log, opts.Version, 8)
	addr := opts.Address
	if addr == "" {
		addr = ipc.DefaultAddress(dataDir)
	}
	c := &Core{Address: addr, engine: eng, db: db, done: make(chan struct{})}
	srv := &api.Server{
		Version: opts.Version, DataDir: dataDir, DB: db, Store: st, Hub: hub, Runner: runner,
		Providers: providers, Catalog: cat, Engine: eng, Config: cfgFn, Log: log, Shutdown: c.Stop,
	}
	c.server = &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := ipc.Listen(addr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	go func() {
		if err := c.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("core server", "err", err)
		}
	}()
	log.Info("core listening", "address", addr, "data", dataDir)
	return c, nil
}

// Listener-free check used by clients deciding whether to start a core in-process.
func Reachable(addr string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	conn, err := ipc.Dial(ctx, addr)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (c *Core) Stop() {
	c.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.server.Shutdown(ctx)
		c.engine.Stop()
		_ = c.db.Close()
		if c.lock != nil {
			_ = c.lock.Unlock()
		}
		close(c.done)
	})
}

func (c *Core) Done() <-chan struct{} { return c.done }
