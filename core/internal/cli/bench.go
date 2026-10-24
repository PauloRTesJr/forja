package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/bench"
	"forja/internal/library"
)

// exitRegression means `forja bench compare` found a regression past the threshold.
const exitRegression = 6

// Files `forja bench --out <dir>` writes.
const (
	benchJSON = "bench.json"
	benchHTML = "bench.html"
)

func (a *app) benchCmd() *cobra.Command {
	var req bench.Request
	var out string
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Measure every stage of a suite of reference images, per preset",
		Long: "Runs a suite (a YAML file naming images and presets) as ordinary jobs, measured: every stage\n" +
			"runs, none comes from the cache, and each records its time, model load, peak VRAM and RAM, GPU\n" +
			"utilization and output stats. The result is kept with the core's data and, with --out, written\n" +
			"there as " + benchJSON + " and a self-contained " + benchHTML + ". Without --suite, the suite is\n" +
			"bench/suites/default.yaml in the Forja checkout around the current folder.\n\n" +
			"Exit codes: 3 when a sample failed, 4 when cancelled.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			suite, err := findSuite(req.Suite)
			if err != nil {
				return err
			}
			req.Suite = suite
			return a.runBench(req, out)
		},
	}
	cmd.Flags().StringVar(&req.Suite, "suite", "", "suite file (default: bench/suites/default.yaml in this checkout)")
	cmd.Flags().StringArrayVarP(&req.Presets, "preset", "p", nil, "run these presets instead of the suite's")
	cmd.Flags().BoolVar(&req.Cold, "cold", false, "unload the models before every image, so each pays its model loads")
	cmd.Flags().BoolVar(&req.Batch, "batch", false, "queue each preset's images together, to measure throughput and GPU use")
	cmd.Flags().IntVar(&req.Repeat, "repeat", 1, "run every image this many times")
	cmd.Flags().StringVar(&req.Label, "label", "", "a name for this run, such as the change it measures")
	cmd.Flags().StringVarP(&out, "out", "o", "", "also write "+benchJSON+" and "+benchHTML+" into this folder")
	calls(cmd, "POST /v1/bench", "GET /v1/bench/{id}", "GET /v1/bench/{id}/report", "DELETE /v1/bench/{id}",
		"GET /v1/events")

	cmd.AddCommand(calls(&cobra.Command{
		Use: "ls", Short: "List benchmark results, newest first", Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			var list bench.List
			if err := a.get("/v1/bench", &list); err != nil {
				return err
			}
			a.print(list, func() { a.benchList(list) })
			return nil
		},
	}, "GET /v1/bench"))

	cmd.AddCommand(calls(&cobra.Command{
		Use: "show <id>", Short: "Show one result: budgets and stages", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var res bench.Result
			if err := a.get("/v1/bench/"+url.PathEscape(args[0]), &res); err != nil {
				return err
			}
			a.print(res, func() { a.showBench(&res) })
			return nil
		},
	}, "GET /v1/bench/{id}"))

	var reportOut string
	report := calls(&cobra.Command{
		Use: "report <id>", Short: "Write a result's HTML report, to a file or stdout", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if reportOut == "" {
				return a.copyReport(c, args[0], a.out)
			}
			f, err := os.Create(reportOut)
			if err != nil {
				return err
			}
			if err := a.copyReport(c, args[0], f); err != nil {
				f.Close()
				return err
			}
			return f.Close()
		},
	}, "GET /v1/bench/{id}/report")
	report.Flags().StringVarP(&reportOut, "output", "o", "", "file to write (default: stdout)")
	cmd.AddCommand(report)

	cmd.AddCommand(calls(&cobra.Command{
		Use: "cancel <id>", Short: "Stop the running benchmark", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			return c.Do(context.Background(), "DELETE", "/v1/bench/"+url.PathEscape(args[0]), nil, nil)
		},
	}, "DELETE /v1/bench/{id}"))

	cmd.AddCommand(a.benchCompareCmd())
	return cmd
}

func (a *app) benchCompareCmd() *cobra.Command {
	var t bench.Thresholds
	cmd := &cobra.Command{
		Use:   "compare <baseline> <current>",
		Short: "Compare a result against a baseline; exit 6 on a regression past the threshold",
		Long: "Each side is a result file (" + benchJSON + ") or the id of a result the core keeps. Per preset it\n" +
			"compares the median image-to-GLB time and the failures, per stage the median time and peak\n" +
			"VRAM, and the core's overhead per job. A value regresses when it grows by more than the\n" +
			"threshold and by at least --min-seconds or --min-vram-mb. Defaults: the bench section of the\n" +
			"config (`forja config set bench.threshold_pct=5`).",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			req := api.CompareRequest{Baseline: resultRef(args[0]), Current: resultRef(args[1]), Thresholds: &t}
			c, err := a.client()
			if err != nil {
				return err
			}
			var cmp bench.Comparison
			if err := c.Do(context.Background(), "POST", "/v1/bench/compare", req, &cmp); err != nil {
				return err
			}
			a.print(cmp, func() { a.showComparison(&cmp) })
			if cmp.Regressions > 0 {
				return &exitError{exitRegression, fmt.Sprintf("%d regressions past %.0f%%", cmp.Regressions, cmp.Thresholds.Pct)}
			}
			return nil
		},
	}
	cmd.Flags().Float64Var(&t.Pct, "threshold", 0, "percent of growth that fails (default: bench.threshold_pct, 10)")
	cmd.Flags().Float64Var(&t.MinSeconds, "min-seconds", 0, "smaller time changes are noise (default: bench.min_seconds)")
	cmd.Flags().Float64Var(&t.MinVRAMMB, "min-vram-mb", 0, "smaller VRAM changes are noise (default: bench.min_vram_mb)")
	return calls(cmd, "POST /v1/bench/compare")
}

// resultRef passes a result file as its full path, and anything else as an id.
func resultRef(arg string) string {
	if st, err := os.Stat(arg); err == nil && !st.IsDir() {
		if abs, err := filepath.Abs(arg); err == nil {
			return abs
		}
	}
	return arg
}

// findSuite is the suite to run: the one given, else the default suite of the Forja checkout
// around the current folder.
func findSuite(given string) (string, error) {
	if given != "" {
		return filepath.Abs(given)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, "bench", "suites", "default.yaml")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no suite: give --suite <file> (a Forja checkout has bench/suites/default.yaml)")
		}
		dir = parent
	}
}

// runBench starts a run and follows it to the end, printing each sample as it ends.
func (a *app) runBench(req bench.Request, out string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	// Follow until the run ends, even after Ctrl-C, so its result is saved before an in-process
	// core stops.
	evs, err := c.Events(context.Background())
	if err != nil {
		return err
	}
	var res bench.Result
	if err := c.Do(ctx, "POST", "/v1/bench", req, &res); err != nil {
		return err
	}
	if !a.json {
		a.printf("Benchmark %s v%d: %d samples on %s\n", res.Suite.Name, res.Suite.Version, res.Total,
			orDash(res.Device.Provider))
		for _, s := range res.Skipped {
			a.printf("  skipped %s: %s\n", s.Path, s.Reason)
		}
	}
	phase, printed := "", 0
	for done := false; !done; {
		select {
		case <-ctx.Done():
			_ = c.Do(context.Background(), "DELETE", "/v1/bench/"+res.ID, nil, nil)
			// Cancelled once; a background context never ends, so this case does not come again.
			ctx = context.Background()
		case ev, ok := <-evs:
			if !ok {
				return errors.New("lost the connection to the core")
			}
			if ev.Type != bench.EvUpdated && ev.Type != bench.EvFinished {
				continue
			}
			var got bench.Result
			if json.Unmarshal(ev.Data, &got) != nil || got.ID != res.ID {
				continue
			}
			res, done = got, ev.Type == bench.EvFinished
			if a.json {
				continue
			}
			if res.Phase != phase && res.Phase == "warm-up" {
				a.printf("  loading the models with one image per preset (not measured)\n")
			}
			phase = res.Phase
			for printed < len(res.Jobs) && res.Jobs[printed].Status.Done() {
				a.sampleLine(&res.Jobs[printed])
				printed++
			}
		}
	}
	if err := c.Do(context.Background(), "GET", "/v1/bench/"+res.ID, nil, &res); err != nil {
		return err
	}
	if out != "" {
		if err := a.writeBench(c, &res, out); err != nil {
			return err
		}
	}
	a.print(res, func() {
		a.printf("\n")
		a.showBench(&res)
		if out != "" {
			a.printf("\nWrote %s and %s in %s\n", benchJSON, benchHTML, out)
		} else if res.Report != "" {
			a.printf("\nReport: %s\n", res.Report)
		}
	})
	switch res.Status {
	case bench.Cancelled:
		return &exitError{exitCancelled, "cancelled"}
	case bench.Failed:
		return &exitError{exitJobFailed, "a sample failed"}
	}
	return nil
}

func (a *app) writeBench(c *Client, res *bench.Result, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, benchJSON), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, benchHTML))
	if err != nil {
		return err
	}
	if err := a.copyReport(c, res.ID, f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (a *app) copyReport(c *Client, id string, w io.Writer) error {
	body, err := c.Open(context.Background(), "GET", "/v1/bench/"+url.PathEscape(id)+"/report", nil)
	if err != nil {
		return err
	}
	defer body.Close()
	_, err = io.Copy(w, body)
	return err
}

func (a *app) sampleLine(j *bench.JobResult) {
	if j.Status != library.Succeeded {
		a.printf("  %-14s %-20s ✗ %s: %s\n", j.Preset, j.Image, j.Status, j.Error)
		return
	}
	a.printf("  %-14s %-20s ✓ %.2fs  stages %.2fs  loads %.2fs  core %.0f ms\n", j.Preset, j.Image, j.WallSeconds,
		j.StageSeconds, j.LoadSeconds, j.CoreOverheadSeconds*1000)
}

func (a *app) benchList(list bench.List) {
	if r := list.Running; r != nil {
		a.printf("Running: %s, %s, %d of %d samples\n\n", r.ID, r.Suite.Name, len(r.Jobs), r.Total)
	}
	a.table("ID\tSTARTED\tSUITE\tDEVICE\tCOMMIT\tSTATUS\tSAMPLES\tMEDIANS", func(row func(...any)) {
		for _, e := range list.Results {
			var medians []string
			for _, p := range e.Presets {
				medians = append(medians, fmt.Sprintf("%s %.2fs", p.Preset, p.Seconds.Median))
			}
			row(e.ID, e.StartedAt.Local().Format(time.DateTime), fmt.Sprintf("%s v%d", e.Suite.Name, e.Suite.Version),
				e.Device.ID, orDash(short(e.Forja.Commit)), e.Status, e.Samples, orDash(strings.Join(medians, ", ")))
		}
	})
}

func short(commit string) string { return commit[:min(len(commit), 12)] }

// showBench prints a result's budgets and stages.
func (a *app) showBench(r *bench.Result) {
	a.printf("%s v%d on %s, %s, %s\n", r.Suite.Name, r.Suite.Version, r.Device.ID, r.StartedAt.Local().Format(time.DateTime),
		r.Status)
	if r.Forja.Commit != "" {
		a.printf("forja %s at %s, engine %s\n", r.Forja.Version, short(r.Forja.Commit), orDash(r.Forja.Engine))
	}
	if r.Error != "" {
		a.printf("%s\n", r.Error)
	}
	s := r.Summary
	if s == nil {
		a.printf("%d of %d samples so far\n", len(r.Jobs), r.Total)
		return
	}
	a.printf("\n")
	a.table("PRESET\tIMAGES\tFAILED\tMEDIAN\tP90\tMAX\tBUDGET\tOVER", func(row func(...any)) {
		for _, p := range s.Presets {
			budget := "-"
			if p.BudgetSeconds > 0 {
				budget = fmt.Sprintf("%.0fs", p.BudgetSeconds)
			}
			row(p.Preset, p.Images, p.Failed, secs(p.Seconds.Median), secs(p.Seconds.P90), secs(p.Seconds.Max), budget, p.OverBudget)
		}
	})
	budget := ""
	if s.CoreOverheadBudgetMS > 0 {
		budget = fmt.Sprintf(" (budget %.0f ms)", s.CoreOverheadBudgetMS)
	}
	a.printf("\ncore overhead per job: median %.0f ms, p90 %.0f ms, max %.0f ms%s\n", s.CoreOverhead.Median*1000,
		s.CoreOverhead.P90*1000, s.CoreOverhead.Max*1000, budget)
	for _, d := range s.Devices {
		util := ""
		if d.GPUUtil != nil {
			util = fmt.Sprintf(", GPU utilization %.0f%%", *d.GPUUtil*100)
		}
		a.printf("%s busy %.0f%% of %.1fs%s\n", d.Device, d.Busy*100, s.WallSeconds, util)
	}
	a.printf("\n")
	a.table("PRESET\tSTAGE\tMODEL\tRUNS\tMEDIAN\tP90\tLOAD\tVRAM\tRAM\tGPU\tTRIS\tWATERTIGHT\tUV", func(row func(...any)) {
		for _, st := range s.Stages {
			load := "-"
			if st.LoadSeconds.N > 0 {
				load = secs(st.LoadSeconds.Median)
			}
			row(st.Preset, st.Stage, orDash(st.Model), st.Seconds.N, secs(st.Seconds.Median), secs(st.Seconds.P90), load,
				gb(st.PeakVRAMBytes), gb(st.PeakRAMBytes), pctOf(st.GPUUtil, 1), num(st.Triangles), pctOf(st.Watertight, 100),
				pctOf(st.UVCoverage, 100))
		}
	})
}

func (a *app) showComparison(c *bench.Comparison) {
	a.printf("%s (%s) against the baseline %s (%s)\n", c.Current.ID, orDash(short(c.Current.Forja.Commit)), c.Baseline.ID,
		orDash(short(c.Baseline.Forja.Commit)))
	for _, w := range c.Warnings {
		a.printf("warning: %s\n", w)
	}
	a.printf("\n")
	a.table("PRESET\tSTAGE\tMETRIC\tBASELINE\tCURRENT\tCHANGE\t", func(row func(...any)) {
		for _, d := range c.Deltas {
			change := "-"
			if d.Pct != nil {
				change = fmt.Sprintf("%+.1f%%", *d.Pct)
			}
			mark := ""
			if d.Regression {
				mark = "REGRESSION"
			}
			row(orDash(d.Preset), orDash(d.Stage), d.Metric, metric(d.Metric, d.Baseline), metric(d.Metric, d.Current), change, mark)
		}
	})
	if c.Regressions == 0 {
		a.printf("\nNo regression past %.0f%%.\n", c.Thresholds.Pct)
	}
}

func metric(name string, v float64) string {
	switch name {
	case bench.MetricVRAM:
		return fmt.Sprintf("%.2f GB", v/(1<<30))
	case bench.MetricFailed:
		return fmt.Sprintf("%.0f", v)
	case bench.MetricOverhead:
		return fmt.Sprintf("%.0f ms", v*1000)
	}
	return secs(v)
}

func secs(s float64) string { return fmt.Sprintf("%.2fs", s) }

func gb(n *int64) string {
	if n == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f GB", float64(*n)/(1<<30))
}

// pctOf prints a ratio as a percent, scale being 100 for a 0..1 ratio and 1 for a percent.
func pctOf(p *float64, scale float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", *p*scale)
}

func num(p *float64) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f", *p)
}
