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
	"forja/internal/installs"
	"forja/internal/ipc"
	"forja/internal/jobs"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/provider"
	"forja/internal/provider/engine"
	configured "forja/internal/providers"
	"forja/internal/secrets"
	"forja/internal/store"
	"forja/internal/sweep"
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
	runner  *jobs.Runner
	hub     *events.Hub
	sweeper *sweep.Sweeper
	db      *library.DB
	lock    *flock.Flock
	logFile *rotatingFile
	done    chan struct{}
	once    sync.Once
}

// ErrAlreadyRunning means another core owns this data directory.
var ErrAlreadyRunning = errors.New("another Forja core already runs on this data folder: close the app that started it, or run `forja daemon stop`")

// lockWait is how long a starting core waits for one that is stopping to let go of the data
// directory. Tests shorten it.
var lockWait = 5 * time.Second

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
	// A core on its way out (the app was closed and opened again at once) frees the lock within
	// moments; one that stays is running.
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	cancel()
	if !ok {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
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
	handler := opts.Log.Handler()
	logFile, err := openLog(filepath.Join(dataDir, "logs", "core.log"))
	if err != nil {
		opts.Log.Warn("not writing core.log", "err", err)
	} else {
		handler = tee{handler, slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug})}
	}
	started := false
	defer func() {
		if !started && logFile != nil {
			_ = logFile.Close()
		}
	}()
	log := slog.New(secrets.Redacting(handler))
	st, err := store.Open(filepath.Join(dataDir, "store"))
	if err != nil {
		return nil, err
	}
	db, err := library.Open(dataDir)
	if err != nil {
		return nil, err
	}
	if err := db.FailUnfinished(context.Background()); err != nil {
		log.Warn("could not end the jobs the last run left unfinished", "err", err)
	}

	cfgFn := func(project string) (*config.Loaded, error) { return config.Load(project, nil) }
	eng := localengine.New(func() (*config.Loaded, error) { return config.Load("", nil) }, st.Root, filepath.Join(dataDir, "logs"), log)
	providers := provider.NewRegistry()
	providers.Add(engine.New(jobs.LocalProvider, provider.KindLocal, false, true, eng.Endpoint, st))
	for _, pc := range base.Providers {
		p, err := configured.Build(pc, st)
		if err != nil {
			log.Warn("skipping provider", "provider", pc.ID, "err", err)
			continue
		}
		providers.Add(p)
	}

	hub := events.NewHub()
	cat := catalog.New(providers, db,
		func(id string) bool {
			if id == jobs.LocalProvider {
				return !eng.Running()
			}
			p, _ := providers.Get(id)
			sleeper, ok := p.(provider.Sleeper)
			return ok && sleeper.Asleep()
		},
		func(id string) { hub.Publish(api.EvModelsChanged, map[string]string{"provider": id}) })
	// Models installed while the engine was stopped show up as soon as it runs again.
	eng.Started = func() { _, _ = cat.Ask(context.Background(), jobs.LocalProvider) }
	// Every change of the engine's state (starting, up, stopped, failed, a restart waiting) reaches
	// the app, which asks again what it shows.
	eng.Changed = func() { hub.Publish(api.EvProvidersChanged, map[string]string{"provider": jobs.LocalProvider}) }
	runner := jobs.NewRunner(db, st, hub, providers, cat, cfgFn, log, opts.Version, 8)
	tracker := installs.New(providers, cat, hub, log)
	// New engine settings are read by restarting the engine, which waits while a job or an install
	// uses it.
	eng.WhenIdle = func(fn func()) bool {
		if runner.Busy() || tracker.Busy(jobs.LocalProvider) {
			return false
		}
		return runner.WhenIdle(fn)
	}
	tracker.OnDone = func(string) { eng.Settle() }
	sweeper := sweep.New(db, st, runner, log)
	runner.OnIdle = func() {
		sweeper.Nudge()
		eng.Settle()
	}
	addr := opts.Address
	if addr == "" {
		addr = ipc.DefaultAddress(dataDir)
	}
	c := &Core{Address: addr, engine: eng, runner: runner, hub: hub, sweeper: sweeper, db: db, done: make(chan struct{})}
	srv := &api.Server{
		Version: opts.Version, DataDir: dataDir, DB: db, Store: st, Hub: hub, Runner: runner, Sweeper: sweeper,
		Providers: providers, Catalog: cat, Installs: tracker, Engine: eng, Config: cfgFn, Log: log, Shutdown: c.Stop,
	}
	c.server = &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := ipc.Listen(addr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	// Only a core that serves starts work of its own: nothing is left running by one that
	// could not listen.
	sweeper.Start()
	// Jobs that only waited for their turn when the core last stopped run now. Not before the
	// socket answers: planning them may have to wait for a provider.
	go runner.Resume(context.Background())
	if base.Engine.Share {
		// A shared GPU answers other computers even when nobody here is generating.
		go func() {
			if _, _, err := eng.Endpoint(context.Background()); err != nil {
				log.Warn("could not start the shared engine", "err", err)
			}
		}()
	}
	go func() {
		if err := c.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("core server", "err", err)
		}
	}()
	log.Info("core listening", "address", addr, "data", dataDir)
	started, c.logFile = true, logFile
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
		// Event streams never end by themselves, and Shutdown waits for every open request.
		c.hub.Close()
		// Jobs end as interrupted while the engine can still be told to drop their runs; then the
		// engine goes for good, so nothing that still asks for it starts one that outlives the core.
		c.runner.Close(5 * time.Second)
		c.engine.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.server.Shutdown(ctx)
		c.sweeper.Close()
		_ = c.db.Close()
		if c.logFile != nil {
			_ = c.logFile.Close()
		}
		if c.lock != nil {
			_ = c.lock.Unlock()
		}
		close(c.done)
	})
}

func (c *Core) Done() <-chan struct{} { return c.done }
