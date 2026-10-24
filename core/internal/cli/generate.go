package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/config"
	"forja/internal/engineapi"
	"forja/internal/jobs"
	"forja/internal/library"
)

func (a *app) generateCmd() *cobra.Command {
	var preset, project, priority, sheet string
	var sets, views, skip, with []string
	var seed int
	var noWait, dryRun bool
	cmd := &cobra.Command{
		Use:   "generate <image>...",
		Short: "Turn reference images into game-ready assets",
		Long: "Queues one job per image and follows them until they end. --dry-run shows the plan instead:\n" +
			"the model and provider each stage would use, and what a hosted model would charge.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			overrides := map[string]any{}
			for _, s := range sets {
				k, v, ok := strings.Cut(s, "=")
				if !ok {
					return fmt.Errorf("--set %q: want stage.param=value", s)
				}
				overrides[k] = parseValue(v)
			}
			// Optional stages, as the app's switches: "<stage>.enabled".
			for _, s := range skip {
				overrides[s+".enabled"] = false
			}
			for _, s := range with {
				overrides[s+".enabled"] = true
			}
			paths := make([]string, len(args))
			for i, p := range args {
				abs, err := filepath.Abs(p)
				if err != nil {
					return err
				}
				paths[i] = abs
			}
			viewPaths := map[string]string{}
			for _, v := range views {
				name, path, ok := strings.Cut(v, "=")
				if !ok {
					return fmt.Errorf("--view %q: want left=<image>, back=<image> or right=<image>", v)
				}
				abs, err := filepath.Abs(path)
				if err != nil {
					return err
				}
				viewPaths[name] = abs
			}
			if len(viewPaths) > 0 && len(paths) > 1 {
				return errors.New("--view gives other views of one subject: pass a single front image")
			}
			if !slices.Contains([]string{library.SheetKeep, library.SheetSplit, library.SheetAsk}, sheet) {
				return fmt.Errorf("--sheet %q: want keep, split or ask", sheet)
			}
			if project == "" {
				if wd, err := os.Getwd(); err == nil {
					project = config.FindProject(wd)
				}
			}
			if dryRun {
				return a.dryRun(preset, project, slices.Sorted(maps.Keys(viewPaths)))
			}
			var c *Client
			var err error
			if noWait {
				// An in-process core would stop, and the jobs with it, when this command ends.
				c, err = a.daemonClient("--no-wait")
			} else {
				c, err = a.client()
			}
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
				"overrides": overrides, "sheet": sheet}
			if len(viewPaths) > 0 {
				delete(body, "paths")
				body["path"], body["views"] = paths[0], viewPaths
			}
			if cmd.Flags().Changed("seed") {
				body["seed"] = seed
			}
			if err := c.Do(ctx, "POST", "/v1/jobs", body, &created); err != nil {
				return err
			}
			if noWait {
				a.print(created, func() {
					for _, j := range created {
						a.printf("%s\n", j.ID)
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
	cmd.Flags().StringArrayVar(&skip, "skip", nil, "turn an optional stage off, e.g. --skip lods")
	cmd.Flags().StringArrayVar(&with, "with", nil, "turn an optional stage on")
	cmd.Flags().IntVar(&seed, "seed", 0, "seed for the models (default: the preset's)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "submit and print job ids without waiting (needs a running core)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan and its price without queueing anything")
	cmd.Flags().StringArrayVar(&views, "view", nil, "another view of the same subject, e.g. --view back=back.png (left, back, right)")
	cmd.Flags().StringVar(&sheet, "sheet", library.SheetKeep,
		"when an image shows several views of its subject side by side: keep it whole, split it into those views,\n"+
			"or ask (at a terminal you answer here; otherwise the job waits for `forja jobs sheet`)")
	return calls(cmd, append([]string{"POST /v1/jobs", "GET /v1/presets", "GET /v1/config/effective"}, watchRoutes...)...)
}

// parseValue reads a command-line value as JSON (numbers, true, false, null, lists), else
// as text.
func parseValue(v string) any {
	var parsed any
	if json.Unmarshal([]byte(v), &parsed) != nil {
		return v
	}
	return parsed
}

// dryRun prints the plan of the preset a job would use, with the views it would bring.
func (a *app) dryRun(preset, project string, views []string) error {
	if preset == "" {
		var cfg config.Loaded
		if err := a.get("/v1/config/effective?project="+url.QueryEscape(project), &cfg); err != nil {
			return err
		}
		preset = cfg.Project.DefaultPreset
	}
	p, err := a.preset(preset, project, views)
	if err != nil {
		return err
	}
	a.print(p, func() { a.showPlan(p) })
	if !p.Ready {
		return &exitError{1, "preset " + p.ID + " cannot run now"}
	}
	return nil
}

// preset asks the core for one preset as it would run now.
func (a *app) preset(id, project string, views []string) (*api.PresetView, error) {
	q := url.Values{"fresh": {"1"}}
	if project != "" {
		q.Set("project", project)
	}
	if len(views) > 0 {
		q.Set("views", strings.Join(views, ","))
	}
	var list []api.PresetView
	if err := a.get("/v1/presets?"+q.Encode(), &list); err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == id {
			return &list[i], nil
		}
	}
	return nil, fmt.Errorf("no preset %s; `forja presets ls` lists them", id)
}

// showPlan prints where each stage of a preset would run and what one image would cost.
func (a *app) showPlan(p *api.PresetView) {
	a.printf("%s (%s)\n", p.ID, p.Name)
	if p.Description != "" {
		a.printf("%s\n", p.Description)
	}
	if p.Error != "" {
		a.printf("cannot run: %s\n", p.Error)
		return
	}
	optional := map[string]string{}
	for _, s := range p.Stages {
		if s.Optional != nil {
			state := "off"
			if s.Optional.Default {
				state = "on"
			}
			optional[s.ID] = fmt.Sprintf("optional, %s by default", state)
		}
	}
	cost, currency := 0.0, ""
	a.printf("\n")
	a.table("STAGE\tUSE\tMODEL\tPROVIDER\tNOTE", func(row func(...any)) {
		for _, pick := range p.Plan {
			model, note := orDash(pick.ModelName, pick.Model), optional[pick.Stage]
			switch {
			case pick.Missing:
				model, note = "none ready", "install one: forja models ls --all"
			case pick.Skipped:
				model, note = "-", "done by "+pick.By
			case pick.Price != nil:
				note = joinNote(note, fmt.Sprintf("%s per %s", money(pick.Price.Amount, pick.Price.Currency), pick.Price.Unit))
				if pick.Price.Unit == engineapi.PerCall {
					cost, currency = cost+pick.Price.Amount, pick.Price.Currency
				}
			}
			if len(pick.Views) > 0 {
				note = joinNote(note, "reads "+strings.Join(pick.Views, ", "))
			}
			if len(pick.NeedsViews) > 0 {
				note = joinNote(note, "needs the "+strings.Join(pick.NeedsViews, ", ")+" view")
			}
			row(pick.Stage, pick.Use, model, orDash(pick.Provider), note)
		}
	})
	for _, s := range p.Stages {
		if s.Optional != nil && s.Optional.Help != "" {
			a.printf("\n%s (%s.enabled): %s\n", s.Optional.Label, s.ID, s.Optional.Help)
		}
	}
	if cost > 0 {
		a.printf("\nEach image costs about %s on hosted models.\n", money(cost, currency))
	}
	if !p.Ready {
		a.printf("\nNot ready: a stage has no model. `forja setup` installs what the presets need.\n")
	}
}

func orDash(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return "-"
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func money(amount float64, currency string) string {
	if currency == "" || currency == "USD" {
		return fmt.Sprintf("$%.2f", amount)
	}
	return fmt.Sprintf("%.2f %s", amount, currency)
}

// watch prints stage transitions until every job ends. A job that waits for an answer about
// its sheet of views is asked about at a terminal, and left waiting otherwise.
func (a *app) watch(ctx context.Context, c *Client, evs <-chan Event, list []*library.Job) error {
	waiting := map[string]*library.Job{}
	for _, j := range list {
		waiting[j.ID] = j
	}
	final := slices.Clone(list)
	keep := func(j *library.Job) {
		for i := range final {
			if final[i].ID == j.ID {
				final[i] = j
			}
		}
	}
	parked := 0
	// settle handles a job that ended or waits; it reports whether to stop following it.
	settle := func(j *library.Job) (bool, error) {
		switch {
		case j.Status.Done():
			a.report(j)
		case j.Status == library.Waiting && j.Sheet != nil && j.Sheet.Found && j.Sheet.Answer == "":
			if a.tty && !a.json {
				answered, err := a.askSheet(ctx, c, j)
				if err != nil || answered {
					return false, err
				}
			}
			if !a.json {
				a.printf("  %s waits for an answer: forja jobs sheet %s --split, --keep or --views ...\n", j.Name, j.ID)
			}
			parked++
		default:
			return false, nil
		}
		keep(j)
		delete(waiting, j.ID)
		return true, nil
	}
	// A job may have ended, or come to wait, between submit and subscribe.
	for id := range waiting {
		var j library.Job
		if c.Do(ctx, "GET", "/v1/jobs/"+id, nil, &j) == nil {
			if _, err := settle(&j); err != nil {
				return err
			}
		}
	}
	seen := map[string]library.Status{}
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
			if ev.Type != jobs.EvJobUpdated && ev.Type != jobs.EvJobFinished {
				continue
			}
			var j library.Job
			if json.Unmarshal(ev.Data, &j) != nil || waiting[j.ID] == nil {
				continue
			}
			if !a.json {
				a.stageLines(&j, seen)
			}
			if _, err := settle(&j); err != nil {
				return err
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
	if parked > 0 {
		return &exitError{exitWaiting, "a job waits for an answer"}
	}
	return nil
}

// stageLines prints each stage of j whose status changed since the last call.
func (a *app) stageLines(j *library.Job, seen map[string]library.Status) {
	for _, s := range j.Stages {
		key := j.ID + "/" + s.ID
		if seen[key] == s.Status {
			continue
		}
		seen[key] = s.Status
		switch {
		case s.Status == library.Running:
			a.printf("  %-10s %-20s running\n", j.Name, s.ID)
		case s.Status == library.Succeeded && s.Cached:
			a.printf("  %-10s %-20s cached\n", j.Name, s.ID)
		case s.Status == library.Succeeded && s.PeakVRAM > 0:
			a.printf("  %-10s %-20s done in %.2fs, peak %.1f GB VRAM\n", j.Name, s.ID, s.Seconds,
				float64(s.PeakVRAM)/(1<<30))
		case s.Status == library.Succeeded:
			a.printf("  %-10s %-20s done in %.2fs\n", j.Name, s.ID, s.Seconds)
		case s.Status == library.Failed:
			a.printf("  %-10s %-20s failed: %s\n", j.Name, s.ID, s.Error)
		}
	}
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
		a.printf("✓ %s (%s) in %.1fs  job %s", j.Name, j.Preset, total, j.ID)
		if j.AssetID != "" {
			a.printf("  asset %s", j.AssetID)
		}
		a.printf("\n")
	default:
		a.printf("✗ %s %s: %s  job %s\n", j.Name, j.Status, j.Error, j.ID)
	}
	if sh := j.Sheet; sh != nil && sh.Found && sh.Answer == library.SheetKeep {
		views := 0
		for _, p := range sh.Parts {
			if p.View != "" {
				views++
			}
		}
		a.printf("  %s looks like %d views of one subject; --sheet split builds from them\n", j.Name, views)
	}
}

// showSheet prints the figures found in a job's image, numbered, with the view proposed for
// each.
func (a *app) showSheet(j *library.Job) {
	sh := j.Sheet
	a.printf("%s holds %d figures (%dx%d):\n", j.Name, len(sh.Parts), sh.Size[0], sh.Size[1])
	for i, p := range sh.Parts {
		kind := p.Kind
		if kind == "detail" {
			kind = "close-up"
		}
		a.printf("  %d  %-6s %-9s %d,%d to %d,%d\n", i+1, orDash(p.View), orDash(kind), p.Box[0], p.Box[1], p.Box[2], p.Box[3])
	}
}

// proposed is the views the core proposed for a sheet, as an answer.
func proposed(sh *library.Sheet) jobs.SheetAnswer {
	labels := make([]string, len(sh.Parts))
	for i, p := range sh.Parts {
		labels[i] = p.View
	}
	return chosen(sh.Parts, labels)
}

// chosen is the parts to cut out, in view order, given a view (or "") per part.
func chosen(parts []library.SheetPart, labels []string) jobs.SheetAnswer {
	var ans jobs.SheetAnswer
	for _, view := range engineapi.Views {
		if i := slices.Index(labels, view); i >= 0 {
			ans.Views = append(ans.Views, library.SheetPart{Box: parts[i].Box, Kind: parts[i].Kind, View: view})
		}
	}
	return ans
}

// relabel reads a view per figure, in order, such as "front,-,left,back": "-" leaves one out.
func relabel(parts []library.SheetPart, spec string) (jobs.SheetAnswer, error) {
	labels := strings.Split(spec, ",")
	if len(labels) != len(parts) {
		return jobs.SheetAnswer{}, fmt.Errorf("give a view for each of the %d figures, such as %s", len(parts),
			strings.TrimSuffix(strings.Repeat("-,", len(parts)), ","))
	}
	for i, l := range labels {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "-" || l == "none" {
			l = ""
		}
		if l != "" && !slices.Contains(engineapi.Views, l) {
			return jobs.SheetAnswer{}, fmt.Errorf("figure %d: %q is not a view: want front, left, back, right or -", i+1, l)
		}
		if l != "" && slices.Contains(labels[:i], l) {
			return jobs.SheetAnswer{}, fmt.Errorf("the %s view is given twice", l)
		}
		labels[i] = l
	}
	if !slices.Contains(labels, "front") {
		return jobs.SheetAnswer{}, errors.New("say which figure is the front")
	}
	return chosen(parts, labels), nil
}

// askSheet asks at the terminal how to build from a sheet of views, and sends the answer.
func (a *app) askSheet(ctx context.Context, c *Client, j *library.Job) (bool, error) {
	a.showSheet(j)
	for {
		ans := proposed(j.Sheet)
		var names []string
		for _, p := range ans.Views {
			names = append(names, p.View)
		}
		if len(names) > 0 && names[0] == "front" {
			a.printf("Build from %s? [Y]es, [k]eep the image whole, [l]ater, or a view per figure (front,-,left,...): ",
				strings.Join(names, ", "))
		} else {
			a.printf("Which figure is the front? A view per figure (front,-,left,...), [k]eep the image whole or [l]ater: ")
		}
		answer, ok := a.readLine()
		answer = strings.ToLower(answer)
		switch {
		case !ok:
			a.printf("\n")
			return false, nil
		case answer == "l" || answer == "later":
			return false, nil
		case answer == "k" || answer == "keep":
			ans = jobs.SheetAnswer{Keep: true}
		case answer == "" || answer == "y" || answer == "yes":
			if len(names) == 0 || names[0] != "front" {
				continue
			}
		default:
			var err error
			if ans, err = relabel(j.Sheet.Parts, answer); err != nil {
				a.printf("%s\n", err)
				continue
			}
		}
		var next library.Job
		err := c.Do(ctx, "POST", "/v1/jobs/"+j.ID+"/sheet", ans, &next)
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == 422 {
			a.printf("%s\n", apiErr.Detail)
			continue
		}
		return err == nil, err
	}
}
