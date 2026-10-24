package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/config"
	"forja/internal/daemon"
	"forja/internal/localengine"
	"forja/internal/secrets"
)

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
	cmd.AddCommand(calls(&cobra.Command{
		Use: "status", Short: "Say whether a core is running, and where",
		RunE: func(*cobra.Command, []string) error {
			addr := a.addr()
			running := false
			if daemon.Reachable(addr) {
				var health map[string]string
				running = NewClient(addr).Do(context.Background(), "GET", "/v1/health", nil, &health) == nil && health["status"] == "ok"
			}
			a.print(map[string]any{"running": running, "address": addr}, func() {
				if running {
					a.printf("A core is running on %s.\n", addr)
				} else {
					a.printf("No core is running on %s. Commands start one for as long as they run.\n", addr)
				}
			})
			if !running {
				return &exitError{1, "no core is running"}
			}
			return nil
		},
	}, "GET /v1/health"))
	cmd.AddCommand(calls(&cobra.Command{
		Use: "stop", Short: "Stop the running core and its engine (the app's jobs stop too)",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.daemonClient("stopping it")
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "POST", "/v1/shutdown", nil, nil)
		},
	}, "POST /v1/shutdown"))
	return cmd
}

// projectFlag finds the project the way generate does: the flag, else forja.toml above the cwd.
func projectFlag(project string) (string, error) {
	if project != "" {
		return filepath.Abs(project)
	}
	if wd, err := os.Getwd(); err == nil {
		return config.FindProject(wd), nil
	}
	return "", nil
}

func (a *app) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show and change settings",
		Long: "Settings are layered: built-in defaults, then the system, user (config.toml) and project\n" +
			"(forja.toml) files, then FORJA_* environment variables. `forja config get` shows the settings\n" +
			"the app edits and which layer set each; `forja config show` prints everything the core uses.",
	}
	var effective bool
	var showProject string
	show := calls(&cobra.Command{
		Use: "show", Short: "Print the merged configuration the core uses",
		RunE: func(*cobra.Command, []string) error {
			project, err := projectFlag(showProject)
			if err != nil {
				return err
			}
			var cfg config.Loaded
			if err := a.get("/v1/config/effective?project="+url.QueryEscape(project), &cfg); err != nil {
				return err
			}
			a.print(cfg, func() {
				raw, _ := json.MarshalIndent(cfg.Config, "", "  ")
				a.printf("%s\n", raw)
				if effective {
					a.printf("\n# where each value came from\n")
					for _, line := range cfg.SortedSources() {
						a.printf("%s\n", line)
					}
				}
			})
			return nil
		},
	}, "GET /v1/config/effective")
	show.Flags().BoolVar(&effective, "effective", false, "also show which layer set each value")
	show.Flags().StringVar(&showProject, "project", "", "project root holding forja.toml (default: found from the cwd)")
	cmd.AddCommand(show)

	var getProject string
	get := calls(&cobra.Command{
		Use: "get [key]...", Short: "Show the settings the app edits: value, default and the layer that set it",
		RunE: func(_ *cobra.Command, args []string) error {
			project, err := projectFlag(getProject)
			if err != nil {
				return err
			}
			var view api.SettingsView
			if err := a.get("/v1/settings?project="+url.QueryEscape(project), &view); err != nil {
				return err
			}
			list := view.Settings
			if len(args) > 0 {
				list = nil
				for _, key := range args {
					i := slices.IndexFunc(view.Settings, func(s api.SettingView) bool { return s.Key == key })
					if i < 0 {
						return fmt.Errorf("%s is not a setting the app edits; `forja config show` prints every value", key)
					}
					list = append(list, view.Settings[i])
				}
			}
			a.print(list, func() {
				if len(args) == 1 {
					a.printf("%s\n", setting(list[0].Value))
					return
				}
				a.table("KEY\tVALUE\tDEFAULT\tSET BY", func(row func(...any)) {
					for _, s := range list {
						row(s.Key, setting(s.Value), setting(s.Default), s.Source)
					}
				})
			})
			return nil
		},
	}, "GET /v1/settings")
	get.Flags().StringVar(&getProject, "project", "", "project root holding forja.toml (default: found from the cwd)")
	cmd.AddCommand(get)

	var setProject string
	write := func(changes map[string]any) error {
		body := map[string]any{"set": changes}
		if setProject != "" {
			project, err := filepath.Abs(setProject)
			if err != nil {
				return err
			}
			body["project"] = project
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		var view api.SettingsView
		if err := c.Do(context.Background(), "PATCH", "/v1/settings", body, &view); err != nil {
			return err
		}
		a.print(view, func() {
			switch view.Engine {
			case "restarted":
				a.printf("Saved. The engine restarts with it the next time it runs.\n")
			case "next-start":
				a.printf("Saved. It applies once the running jobs finish and the engine restarts.\n")
			}
		})
		return nil
	}
	set := calls(&cobra.Command{
		Use:   "set <key>=<value>...",
		Short: "Change settings the app edits, in config.toml (or a project's forja.toml)",
		Long: "Values are read as JSON when they parse (5, 0.5, true), else as text. Changes are checked\n" +
			"before they are saved, and comments in the file are kept. Keys: forja config get.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			changes := map[string]any{}
			for _, kv := range args {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return fmt.Errorf("%q: want key=value", kv)
				}
				changes[k] = parseValue(v)
			}
			return write(changes)
		},
	}, "PATCH /v1/settings")
	unset := calls(&cobra.Command{
		Use: "unset <key>...", Short: "Remove settings from the file, back to the layer below", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			changes := map[string]any{}
			for _, k := range args {
				changes[k] = nil
			}
			return write(changes)
		},
	}, "PATCH /v1/settings")
	for _, c := range []*cobra.Command{set, unset} {
		c.Flags().StringVar(&setProject, "project", "", "write this project's forja.toml instead of config.toml")
		cmd.AddCommand(c)
	}

	cmd.AddCommand(calls(&cobra.Command{
		Use:   "edit",
		Short: "Open config.toml in $VISUAL, $EDITOR or the system's editor",
		Long:  "Creates config.toml when there is none. With --json, prints its path instead of opening it.",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var files struct {
				Path    string `json:"path"`
				DataDir string `json:"data_dir"`
			}
			if err := c.Do(context.Background(), "POST", "/v1/settings/file", nil, &files); err != nil {
				return err
			}
			if a.json {
				a.print(files, func() {})
				return nil
			}
			return a.openFile(files.Path)
		},
	}, "POST /v1/settings/file"))
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print config file locations",
		Run: func(*cobra.Command, []string) { a.printf("%s\n", config.UserFile()) },
	})
	return cmd
}

// setting prints a setting's value as config.toml would hold it.
func setting(v any) string {
	if v == nil {
		return "-"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// openFile opens path in the user's editor and waits, or hands it to the system to open.
func (a *app) openFile(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	var cmd *exec.Cmd
	switch {
	case editor != "":
		args := strings.Fields(editor)
		cmd = exec.Command(args[0], append(args[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, a.out, a.errOut
	case runtime.GOOS == "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case runtime.GOOS == "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	return nil
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
	cmd.AddCommand(calls(&cobra.Command{
		Use: "unload", Short: "Free all GPU memory now (pause while gaming)",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "POST", "/v1/engine/unload", nil, nil)
		},
	}, "POST /v1/engine/unload"))
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

func (a *app) secretCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "secret", Short: "Store API keys and tokens in the OS keychain"}
	cmd.AddCommand(&cobra.Command{
		Use: "set <name>", Short: "Read a secret from stdin and store it", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			value, err := a.readSecret("secret")
			if err != nil {
				return err
			}
			return secrets.Set(args[0], value)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "rm <name>", Short: "Delete a stored secret", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error { return secrets.Delete(args[0]) },
	})
	return cmd
}

func (a *app) infoCmd() *cobra.Command {
	return calls(&cobra.Command{
		Use: "info", Short: "Show core, engine and hardware status",
		RunE: func(*cobra.Command, []string) error {
			var info map[string]any
			if err := a.get("/v1/info", &info); err != nil {
				return err
			}
			raw, _ := json.MarshalIndent(info, "", "  ")
			a.printf("%s\n", raw)
			return nil
		},
	}, "GET /v1/info")
}

func (a *app) diagnosticsCmd() *cobra.Command {
	return calls(&cobra.Command{
		Use:   "diagnostics",
		Short: "Print a report for bug reports: versions, hardware, settings and the engine log",
		Long:  "The home folder is replaced by ~ and no secret is included. Save it with: forja diagnostics > report.txt",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			body, err := c.Open(context.Background(), "GET", "/v1/diagnostics", nil)
			if err != nil {
				return err
			}
			defer body.Close()
			_, err = io.Copy(a.out, body)
			return err
		},
	}, "GET /v1/diagnostics")
}

func (a *app) eventsCmd() *cobra.Command {
	var types []string
	cmd := calls(&cobra.Command{
		Use:   "events",
		Short: "Print the core's events as JSON lines until interrupted",
		Long: "The live stream the app updates itself from: jobs, stages, installs, models, settings.\n" +
			"--type keeps the events whose type starts with one of the given prefixes, e.g. --type job.",
		RunE: func(*cobra.Command, []string) error {
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
			enc := json.NewEncoder(a.out)
			for ev := range evs {
				if len(types) > 0 && !slices.ContainsFunc(types, func(t string) bool { return strings.HasPrefix(ev.Type, t) }) {
					continue
				}
				if err := enc.Encode(ev); err != nil {
					return err
				}
			}
			if ctx.Err() == nil {
				return errors.New("lost the connection to the core")
			}
			return nil
		},
	}, "GET /v1/events")
	cmd.Flags().StringArrayVar(&types, "type", nil, "keep events whose type starts with this")
	return cmd
}

func (a *app) socketCmd() *cobra.Command {
	return &cobra.Command{
		Use: "socket", Short: "Print the core's socket or pipe address (used by the desktop app)",
		Run: func(*cobra.Command, []string) { a.printf("%s\n", a.addr()) },
	}
}
