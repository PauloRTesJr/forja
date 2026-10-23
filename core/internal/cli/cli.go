// Package cli implements the forja command line. Every command talks to a core over the
// local socket; when no daemon is running, the command starts a core in-process.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/config"
	"forja/internal/daemon"
	"forja/internal/engineapi"
	"forja/internal/ipc"
	"forja/internal/library"
	"forja/internal/localengine"
	"forja/internal/secrets"
)

type app struct {
	version string
	json    bool
	address string
	core    *daemon.Core // set when this process runs the core in-process
}

func Execute(version string) int {
	a := &app{version: version}
	root := &cobra.Command{
		Use:           "forja",
		Short:         "Local-first 3D asset generator",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print machine-readable JSON")
	// FORJA_SOCKET lets a development build run next to an installed app without sharing its core.
	root.PersistentFlags().StringVar(&a.address, "socket", os.Getenv("FORJA_SOCKET"), "core socket or pipe (default: $FORJA_SOCKET, else per-user)")
	root.AddCommand(a.daemonCmd(), a.generateCmd(), a.jobsCmd(), a.modelsCmd(), a.presetsCmd(),
		a.configCmd(), a.engineCmd(), a.secretCmd(), a.infoCmd(), a.socketCmd())
	err := root.Execute()
	if a.core != nil {
		a.core.Stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		return 1
	}
	return 0
}

// Exit codes, documented for scripts and CI.
const (
	exitJobFailed = 3
	exitCancelled = 4
)

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func logger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func (a *app) addr() string {
	if a.address != "" {
		return a.address
	}
	cfg, err := config.Load("", nil)
	if err != nil {
		return ipc.DefaultAddress(".")
	}
	return ipc.DefaultAddress(cfg.DataDir())
}

// client attaches to a running core, or starts one in this process.
func (a *app) client() (*Client, error) {
	addr := a.addr()
	if daemon.Reachable(addr) {
		return NewClient(addr), nil
	}
	core, err := daemon.Start(daemon.Options{Version: a.version, Address: addr, Log: logger(slog.LevelWarn)})
	if err != nil {
		return nil, err
	}
	a.core = core
	return NewClient(addr), nil
}

func (a *app) print(v any, human func()) {
	if a.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	human()
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func (a *app) daemonCmd() *cobra.Command {
	var verbose bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the core in the foreground (the desktop app starts it for you)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			core, err := daemon.Start(daemon.Options{Version: a.version, Address: a.address, Log: logger(level)})
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			select {
			case <-ctx.Done():
				core.Stop()
			case <-core.Done():
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "debug logging")
	return cmd
}

func (a *app) generateCmd() *cobra.Command {
	var preset, project, priority string
	var sets []string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "generate <image>...",
		Short: "Turn reference images into game-ready assets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			overrides := map[string]any{}
			for _, s := range sets {
				k, v, ok := strings.Cut(s, "=")
				if !ok {
					return fmt.Errorf("--set %q: want stage.param=value", s)
				}
				var parsed any
				if json.Unmarshal([]byte(v), &parsed) != nil {
					parsed = v
				}
				overrides[k] = parsed
			}
			paths := make([]string, len(args))
			for i, p := range args {
				abs, err := filepath.Abs(p)
				if err != nil {
					return err
				}
				paths[i] = abs
			}
			if project == "" {
				if wd, err := os.Getwd(); err == nil {
					project = config.FindProject(wd)
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			evs, err := c.Events(ctx) // subscribe before submitting, so no event is missed
			if err != nil {
				return err
			}
			var created []*library.Job
			body := map[string]any{"paths": paths, "preset": preset, "project": project, "priority": priority,
				"overrides": overrides}
			if err := c.Do(ctx, "POST", "/v1/jobs", body, &created); err != nil {
				return err
			}
			if noWait {
				a.print(created, func() {
					for _, j := range created {
						fmt.Println(j.ID)
					}
				})
				return nil
			}
			return a.watch(ctx, c, evs, created)
		},
	}
	cmd.Flags().StringVarP(&preset, "preset", "p", "", "preset id (default: the project's default_preset)")
	cmd.Flags().StringVar(&project, "project", "", "project root holding forja.toml (default: found from the cwd)")
	cmd.Flags().StringVar(&priority, "priority", "interactive", "interactive or batch")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "override a preset value, e.g. --set lowpoly.target_tris=3000")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "submit and print job ids without waiting")
	return cmd
}

// watch prints stage transitions until every job ends.
func (a *app) watch(ctx context.Context, c *Client, evs <-chan Event, jobs []*library.Job) error {
	waiting := map[string]*library.Job{}
	for _, j := range jobs {
		waiting[j.ID] = j
	}
	// A job may have finished between submit and subscribe (all stages cached).
	for id := range waiting {
		var j library.Job
		if c.Do(ctx, "GET", "/v1/jobs/"+id, nil, &j) == nil && j.Status.Done() {
			waiting[id] = &j
			a.report(&j)
			delete(waiting, id)
		}
	}
	seen := map[string]library.Status{}
	var final []*library.Job
	for _, j := range jobs {
		final = append(final, j)
	}
	for len(waiting) > 0 {
		select {
		case <-ctx.Done():
			for id := range waiting {
				_ = c.Do(context.Background(), "DELETE", "/v1/jobs/"+id, nil, nil)
			}
			return &exitError{exitCancelled, "cancelled"}
		case ev, ok := <-evs:
			if !ok {
				return errors.New("lost the connection to the core")
			}
			if ev.Type != "job.updated" && ev.Type != "job.finished" {
				continue
			}
			var j library.Job
			if json.Unmarshal(ev.Data, &j) != nil || waiting[j.ID] == nil {
				continue
			}
			if !a.json {
				for _, s := range j.Stages {
					key := j.ID + "/" + s.ID
					if seen[key] == s.Status {
						continue
					}
					seen[key] = s.Status
					switch {
					case s.Status == library.Running:
						fmt.Printf("  %-10s %-20s running\n", j.Name, s.ID)
					case s.Status == library.Succeeded && s.Cached:
						fmt.Printf("  %-10s %-20s cached\n", j.Name, s.ID)
					case s.Status == library.Succeeded && s.PeakVRAM > 0:
						fmt.Printf("  %-10s %-20s done in %.2fs, peak %.1f GB VRAM\n", j.Name, s.ID, s.Seconds,
							float64(s.PeakVRAM)/(1<<30))
					case s.Status == library.Succeeded:
						fmt.Printf("  %-10s %-20s done in %.2fs\n", j.Name, s.ID, s.Seconds)
					case s.Status == library.Failed:
						fmt.Printf("  %-10s %-20s failed: %s\n", j.Name, s.ID, s.Error)
					}
				}
			}
			if ev.Type == "job.finished" {
				a.report(&j)
				for i := range final {
					if final[i].ID == j.ID {
						final[i] = &j
					}
				}
				delete(waiting, j.ID)
			}
		}
	}
	if a.json {
		a.print(final, func() {})
	}
	for _, j := range final {
		if j.Status == library.Failed {
			return &exitError{exitJobFailed, "a job failed"}
		}
	}
	return nil
}

func (a *app) report(j *library.Job) {
	if a.json {
		return
	}
	total := 0.0
	for _, s := range j.Stages {
		total += s.Seconds
	}
	switch j.Status {
	case library.Succeeded:
		fmt.Printf("✓ %s (%s) in %.1fs  job %s\n", j.Name, j.Preset, total, j.ID)
	default:
		fmt.Printf("✗ %s %s: %s  job %s\n", j.Name, j.Status, j.Error, j.ID)
	}
}

func (a *app) jobsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "jobs", Short: "List, watch and cancel jobs"}
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Short: "List recent jobs",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var list []*library.Job
			if err := c.Do(context.Background(), "GET", "/v1/jobs?limit=50", nil, &list); err != nil {
				return err
			}
			a.print(list, func() {
				tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tNAME\tPRESET\tSTATUS\tCREATED")
				for _, j := range list {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", j.ID, j.Name, j.Preset, j.Status, j.CreatedAt.Format(time.DateTime))
				}
				tw.Flush()
			})
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "watch <job-id>", Short: "Follow a job until it ends", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx, stop := signalContext()
			defer stop()
			evs, err := c.Events(ctx)
			if err != nil {
				return err
			}
			var j library.Job
			if err := c.Do(ctx, "GET", "/v1/jobs/"+args[0], nil, &j); err != nil {
				return err
			}
			return a.watch(ctx, c, evs, []*library.Job{&j})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "cancel <job-id>", Short: "Cancel a running job", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "DELETE", "/v1/jobs/"+args[0], nil, nil)
		},
	})
	return cmd
}

func (a *app) modelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "Install and inspect models"}
	var (
		accept, noWeights bool
		lane, prov        string
	)
	install := &cobra.Command{
		Use:   "install <model>...",
		Short: "Build a model's environment and fetch its pinned weights",
		Long: "Builds the model family's isolated environment with uv, downloads pinned upstream code\n" +
			"and weights, verifies them by sha256, and smoke-tests the device. Safe to re-run.\n" +
			"Run `forja models checks <model>` first to see what this computer still needs.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			body := map[string]any{"provider": prov, "lane": lane}
			if noWeights {
				body["weights"] = false
			}
			for _, id := range args {
				if err := a.runTask(id, "POST", "/v1/models/"+id+"/install", body, accept); err != nil {
					return err
				}
			}
			return nil
		},
	}
	install.Flags().BoolVar(&accept, "accept-license", false, "accept restrictive model licenses without asking")
	install.Flags().BoolVar(&noWeights, "no-weights", false, "build the environment only")
	install.Flags().StringVar(&lane, "lane", "", "torch lane: cuda, rocm, mps or cpu (default: detected)")
	install.Flags().StringVar(&prov, "provider", "", "provider to install on (default: this computer)")
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use: "rm <model>...", Short: "Remove a model's weights, and its environment once unused",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, id := range args {
				if err := a.runTask(id, "DELETE", "/v1/models/"+id, nil, false); err != nil {
					return err
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "checks [model]...", Short: "Show what this computer needs before models install",
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var out engineapi.Checks
			if err := c.Do(context.Background(), "GET", "/v1/checks?models="+strings.Join(args, ","), nil, &out); err != nil {
				return err
			}
			a.print(out, func() {
				for _, ch := range out.Checks {
					mark := "ok "
					if !ch.OK {
						mark = "NO "
					}
					fmt.Printf("%s %-16s found %s; needs %s\n", mark, ch.Label, ch.Found, ch.Need)
					if ch.Fix != nil {
						for _, s := range ch.Fix.Steps {
							fmt.Println("      " + s)
						}
						if ch.Fix.Command != "" {
							fmt.Println("      or run: " + ch.Fix.Command)
						}
					}
				}
			})
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Short: "List models the local engine knows",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var models []map[string]any
			if err := c.Do(context.Background(), "GET", "/v1/models?fresh=1", nil, &models); err != nil {
				return err
			}
			a.print(models, func() {
				tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tTASKS\tLICENSE\tSTATUS\tDEVICE")
				for _, m := range models {
					lic, _ := m["license"].(map[string]any)
					fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", m["id"], m["tasks"], lic["spdx"], m["status"], m["device"])
				}
				tw.Flush()
			})
			return nil
		},
	})
	return cmd
}

// engineExec runs a forja-engine subcommand in the foreground against this core's data folder.
func engineExec(sub string, args []string) error {
	cfg, err := config.Load("", nil)
	if err != nil {
		return err
	}
	base, err := localengine.Command(cfg)
	if err != nil {
		return err
	}
	full := append(append([]string{}, base[1:]...), sub)
	if !slices.Contains(args, "--store") {
		full = append(full, "--store", filepath.Join(cfg.DataDir(), "store"))
	}
	for _, d := range cfg.Engine.ManifestDirs {
		full = append(full, "--manifests", d)
	}
	c := exec.Command(base[0], append(full, args...)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func (a *app) presetsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "presets", Short: "Inspect presets"}
	cmd.AddCommand(&cobra.Command{
		Use: "ls", Short: "List presets",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var list []map[string]any
			if err := c.Do(context.Background(), "GET", "/v1/presets?fresh=1", nil, &list); err != nil {
				return err
			}
			a.print(list, func() {
				tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tNAME\tREADY\tMODELS\tSOURCE")
				for _, p := range list {
					var models []string
					plan, _ := p["plan"].([]any)
					for _, s := range plan {
						pick, _ := s.(map[string]any)
						switch {
						case pick["missing"] == true:
							models = append(models, fmt.Sprintf("%v: none ready", pick["use"]))
						case pick["model"] != nil:
							models = append(models, fmt.Sprint(pick["model"]))
						}
					}
					ready := "yes"
					if p["ready"] != true {
						ready = "no"
					}
					fmt.Fprintf(tw, "%v\t%v\t%v\t%v\t%v\n", p["id"], p["name"], ready, strings.Join(models, ", "), p["source"])
				}
				tw.Flush()
			})
			return nil
		},
	})
	return cmd
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Show configuration"}
	var effective bool
	show := &cobra.Command{
		Use: "show", Short: "Print the merged configuration",
		RunE: func(*cobra.Command, []string) error {
			wd, _ := os.Getwd()
			cfg, err := config.Load(config.FindProject(wd), nil)
			if err != nil {
				return err
			}
			a.print(cfg, func() {
				raw, _ := json.MarshalIndent(cfg.Config, "", "  ")
				fmt.Println(string(raw))
				if effective {
					fmt.Println("\n# where each value came from")
					for _, line := range cfg.SortedSources() {
						fmt.Println(line)
					}
				}
			})
			return nil
		},
	}
	show.Flags().BoolVar(&effective, "effective", false, "also show which layer set each value")
	cmd.AddCommand(show)
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print config file locations",
		Run: func(*cobra.Command, []string) { fmt.Println(config.UserFile()) },
	})
	return cmd
}

func (a *app) engineCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "engine", Short: "Manage the inference engine"}
	cmd.AddCommand(&cobra.Command{
		Use:                "serve [forja-engine serve flags]",
		Short:              "Run an engine for other machines (e.g. --host 0.0.0.0 --port 7860)",
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			return engineExec("serve", args)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "unload", Short: "Free all GPU memory now (pause while gaming)",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "POST", "/v1/engine/unload", nil, nil)
		},
	})
	return cmd
}

func (a *app) secretCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "secret", Short: "Store API keys and tokens in the OS keychain"}
	cmd.AddCommand(&cobra.Command{
		Use: "set <name>", Short: "Read a secret from stdin and store it", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
			if err != nil {
				return err
			}
			return secrets.Set(args[0], strings.TrimSpace(string(raw)))
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "rm <name>", Short: "Delete a stored secret", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return secrets.Delete(args[0]) },
	})
	return cmd
}

func (a *app) infoCmd() *cobra.Command {
	return &cobra.Command{
		Use: "info", Short: "Show core, engine and hardware status",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var info map[string]any
			if err := c.Do(context.Background(), "GET", "/v1/info", nil, &info); err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(info, "", "  ")
			fmt.Println(string(raw))
			return nil
		},
	}
}

func (a *app) socketCmd() *cobra.Command {
	return &cobra.Command{
		Use: "socket", Short: "Print the core's socket or pipe address (used by the desktop app)",
		Run: func(*cobra.Command, []string) { fmt.Println(a.addr()) },
	}
}
