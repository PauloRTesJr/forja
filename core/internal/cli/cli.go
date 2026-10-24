// Package cli implements the forja command line. Every command talks to a core over the
// local socket; when no daemon is running, the command starts a core in-process.
//
// The CLI reaches every feature of the desktop app through the same /v1 endpoints the app
// uses, and never around them (docs/decisions.md, decision 18): each command names the
// endpoints it calls with calls(), and a test checks that together they cover the API.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"forja/internal/config"
	"forja/internal/daemon"
	"forja/internal/ipc"
)

type app struct {
	version string
	json    bool
	address string
	core    *daemon.Core // set when this process runs the core in-process

	in     io.Reader
	out    io.Writer
	errOut io.Writer
	// tty is true when someone at a terminal can answer questions on in.
	tty   bool
	lines *bufio.Reader
}

func Execute(version string) int {
	a := &app{version: version, in: os.Stdin, out: os.Stdout, errOut: os.Stderr, tty: isTerminal(os.Stdin)}
	return a.run(os.Args[1:])
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (a *app) run(args []string) int {
	root := a.root()
	root.SetArgs(args)
	root.SetIn(a.in)
	root.SetOut(a.out)
	root.SetErr(a.errOut)
	err := root.Execute()
	if a.core != nil {
		a.core.Stop()
	}
	if err != nil {
		fmt.Fprintln(a.errOut, "error:", err)
		var exit *exitError
		if errors.As(err, &exit) {
			return exit.code
		}
		return 1
	}
	return 0
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "forja",
		Short:         "Local-first 3D asset generator",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       a.version,
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "print machine-readable JSON")
	// FORJA_SOCKET lets a development build run next to an installed app without sharing its core.
	root.PersistentFlags().StringVar(&a.address, "socket", os.Getenv("FORJA_SOCKET"), "core socket or pipe (default: $FORJA_SOCKET, else per-user)")
	root.AddCommand(a.daemonCmd(), a.setupCmd(), a.generateCmd(), a.jobsCmd(), a.assetsCmd(), a.modelsCmd(),
		a.installsCmd(), a.presetsCmd(), a.hubsCmd(), a.spendCmd(), a.providersCmd(), a.shareCmd(), a.configCmd(),
		a.engineCmd(), a.cacheCmd(), a.secretCmd(), a.infoCmd(), a.diagnosticsCmd(), a.eventsCmd(), a.socketCmd(),
		a.pricesCmd())
	return root
}

// Exit codes, documented for scripts and CI.
const (
	exitJobFailed = 3
	exitCancelled = 4
	// exitWaiting means a job waits for an answer no one at a terminal could give
	// (`forja jobs sheet` gives it).
	exitWaiting = 5
)

type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

// routesKey is the command annotation that lists the core endpoints a command calls.
const routesKey = "forja.routes"

// calls records the core endpoints cmd reaches, such as "GET /v1/jobs/{id}".
func calls(cmd *cobra.Command, routes ...string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[routesKey] = strings.Join(routes, "\n")
	return cmd
}

// The endpoints that following jobs and install tasks calls.
var (
	watchRoutes  = []string{"GET /v1/events", "GET /v1/jobs/{id}", "DELETE /v1/jobs/{id}", "POST /v1/jobs/{id}/sheet"}
	followRoutes = []string{"GET /v1/events", "DELETE /v1/installs/{provider}/{id}"}
)

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

// daemonClient attaches to a running core only: what it starts must outlive this command.
func (a *app) daemonClient(what string) (*Client, error) {
	addr := a.addr()
	if !daemon.Reachable(addr) {
		return nil, fmt.Errorf("%s needs a running core: open the app or run `forja daemon` first", what)
	}
	return NewClient(addr), nil
}

// get is a GET that decodes the core's answer into out.
func (a *app) get(path string, out any) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	return c.Do(context.Background(), "GET", path, nil, out)
}

func (a *app) print(v any, human func()) {
	if a.json {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		_ = enc.Encode(v)
		return
	}
	human()
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

// table writes tab-separated rows under a header, aligned.
func (a *app) table(header string, rows func(row func(cells ...any))) {
	tw := tabwriter.NewWriter(a.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	rows(func(cells ...any) {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = fmt.Sprint(c)
		}
		fmt.Fprintln(tw, strings.Join(parts, "\t"))
	})
	tw.Flush()
}

// readLine reads one answer from the terminal; ok is false once input has ended.
func (a *app) readLine() (line string, ok bool) {
	if a.lines == nil {
		a.lines = bufio.NewReader(a.in)
	}
	s, err := a.lines.ReadString('\n')
	return strings.TrimSpace(s), err == nil || s != ""
}

// confirm asks a yes/no question; only yes is yes.
func (a *app) confirm(question string) bool {
	fmt.Fprintf(a.out, "%s [y/N] ", question)
	answer, _ := a.readLine()
	answer = strings.ToLower(answer)
	return answer == "y" || answer == "yes"
}

// readSecret reads a key or token from stdin: one pasted line at a terminal, else all of
// it. Secrets never come from flags, which shell history keeps.
func (a *app) readSecret(what string) (string, error) {
	if a.tty {
		fmt.Fprintf(a.errOut, "Paste the %s and press Enter: ", what)
		s, _ := a.readLine()
		if s == "" {
			return "", fmt.Errorf("no %s given", what)
		}
		return s, nil
	}
	raw, err := io.ReadAll(io.LimitReader(a.in, 64<<10))
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "", fmt.Errorf("no %s on stdin", what)
	}
	return s, nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
