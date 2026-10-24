package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/jobs"
	"forja/internal/library"
)

// pageQuery is ?limit=&before= for the job and asset lists; before is a date or a time.
func pageQuery(q url.Values, limit int, before string) (url.Values, error) {
	q.Set("limit", strconv.Itoa(limit))
	if before != "" {
		var t time.Time
		var err error
		for _, layout := range []string{time.RFC3339, time.DateTime, time.DateOnly} {
			if t, err = time.ParseInLocation(layout, before, time.Local); err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("--before %q: want a date such as 2026-10-01 or a time such as 2026-10-01T18:30:00Z", before)
		}
		q.Set("before", strconv.FormatInt(t.UnixMilli(), 10))
	}
	return q, nil
}

func (a *app) jobsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "jobs", Short: "List, inspect, watch and cancel jobs"}
	var limit int
	var before string
	ls := calls(&cobra.Command{
		Use: "ls", Short: "List recent jobs, newest first",
		RunE: func(*cobra.Command, []string) error {
			q, err := pageQuery(url.Values{}, limit, before)
			if err != nil {
				return err
			}
			var list []*library.Job
			if err := a.get("/v1/jobs?"+q.Encode(), &list); err != nil {
				return err
			}
			a.print(list, func() {
				a.table("ID\tNAME\tPRESET\tSTATUS\tCREATED", func(row func(...any)) {
					for _, j := range list {
						row(j.ID, j.Name, j.Preset, j.Status, j.CreatedAt.Local().Format(time.DateTime))
					}
				})
			})
			return nil
		},
	}, "GET /v1/jobs")
	ls.Flags().IntVar(&limit, "limit", 50, "how many jobs (up to 500)")
	ls.Flags().StringVar(&before, "before", "", "only jobs created before this date or time")
	cmd.AddCommand(ls)

	cmd.AddCommand(calls(&cobra.Command{
		Use: "show <job-id>", Short: "Show a job's stages, models, timings and outputs", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var j library.Job
			if err := a.get("/v1/jobs/"+url.PathEscape(args[0]), &j); err != nil {
				return err
			}
			a.print(j, func() { a.showJob(&j) })
			return nil
		},
	}, "GET /v1/jobs/{id}"))

	cmd.AddCommand(calls(&cobra.Command{
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
			if err := c.Do(ctx, "GET", "/v1/jobs/"+url.PathEscape(args[0]), nil, &j); err != nil {
				return err
			}
			return a.watch(ctx, c, evs, []*library.Job{&j})
		},
	}, watchRoutes...))

	cmd.AddCommand(calls(&cobra.Command{
		Use: "cancel <job-id>...", Short: "Cancel running or waiting jobs", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, id := range args {
				if err := c.Do(context.Background(), "DELETE", "/v1/jobs/"+url.PathEscape(id), nil, nil); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
			}
			return nil
		},
	}, "DELETE /v1/jobs/{id}"))

	cmd.AddCommand(a.sheetCmd())
	return cmd
}

func (a *app) showJob(j *library.Job) {
	a.printf("%s  %s\n", j.ID, j.Name)
	a.printf("  preset    %s\n", j.Preset)
	a.printf("  status    %s\n", j.Status)
	if j.Error != "" {
		a.printf("  error     %s\n", j.Error)
	}
	a.printf("  created   %s\n", j.CreatedAt.Local().Format(time.DateTime))
	if j.FinishedAt != nil {
		a.printf("  finished  %s\n", j.FinishedAt.Local().Format(time.DateTime))
	}
	if j.Project != "" {
		a.printf("  project   %s\n", j.Project)
	}
	a.printf("  input     %s\n", j.Input)
	for _, v := range []string{"front", "left", "back", "right"} {
		if ref := j.Views[v]; ref != "" {
			a.printf("  %-9s %s\n", v, ref)
		}
	}
	if j.AssetID != "" {
		a.printf("  asset     %s\n", j.AssetID)
	}
	if sh := j.Sheet; sh != nil && sh.Found {
		answer := sh.Answer
		if answer == "" {
			answer = "waiting for an answer"
		}
		a.printf("  sheet     %d figures; %s\n", len(sh.Parts), answer)
	}
	a.printf("\n")
	a.table("STAGE\tUSE\tMODEL\tPROVIDER\tSTATUS\tSECONDS\tOUTPUT", func(row func(...any)) {
		for _, s := range j.Stages {
			status := string(s.Status)
			if s.Cached {
				status += " (cached)"
			}
			if s.Status == library.Running && s.Total > 0 {
				status += fmt.Sprintf(" %d/%d", s.Step, s.Total)
			}
			out := "-"
			if len(s.Outputs) > 0 {
				out = s.Outputs[0].Hash
				if len(s.Outputs) > 1 {
					out += fmt.Sprintf(" (+%d)", len(s.Outputs)-1)
				}
			}
			row(s.ID, s.Use, orDash(s.Model), orDash(s.Provider), status, fmt.Sprintf("%.2f", s.Seconds), out)
		}
	})
	for _, s := range j.Stages {
		for _, f := range s.Fallbacks {
			a.printf("%s moved from %s to %s: it %s\n", s.ID, f.From, f.To, f.Reason)
		}
		if s.Error != "" {
			a.printf("%s failed: %s\n", s.ID, s.Error)
		}
	}
}

func (a *app) sheetCmd() *cobra.Command {
	var split, keep, noWait bool
	var views string
	cmd := &cobra.Command{
		Use:   "sheet <job-id>",
		Short: "Answer a job whose image holds several views of its subject",
		Long: "Shows the figures the core found in the job's image and the view it proposes for each. Then:\n" +
			"  --split               build from the proposed views\n" +
			"  --views front,-,back  build from a view per figure, in the order shown (- leaves one out)\n" +
			"  --keep                use the image whole\n" +
			"Without one, a terminal asks. The job then runs and is followed until it ends.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			given := 0
			for _, b := range []bool{split, keep, views != ""} {
				if b {
					given++
				}
			}
			if given > 1 {
				return errors.New("give one of --split, --keep and --views")
			}
			var c *Client
			var err error
			if noWait {
				c, err = a.daemonClient("--no-wait")
			} else {
				c, err = a.client()
			}
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
			if err := c.Do(ctx, "GET", "/v1/jobs/"+url.PathEscape(args[0]), nil, &j); err != nil {
				return err
			}
			if j.Status != library.Waiting || j.Sheet == nil || !j.Sheet.Found {
				return fmt.Errorf("job %s is %s, not waiting for an answer about its views", j.ID, j.Status)
			}
			var ans jobs.SheetAnswer
			switch {
			case keep:
				ans.Keep = true
			case split:
				if ans = proposed(j.Sheet); len(ans.Views) == 0 || ans.Views[0].View != "front" {
					return errors.New("no figure is proposed as the front: give the views with --views")
				}
			case views != "":
				if ans, err = relabel(j.Sheet.Parts, views); err != nil {
					return err
				}
			default:
				if !a.tty || a.json {
					a.print(j.Sheet, func() {
						a.showSheet(&j)
						a.printf("Answer with --split, --keep or --views (a view per figure, such as %s).\n",
							strings.TrimSuffix(strings.Repeat("-,", len(j.Sheet.Parts)), ","))
					})
					return nil
				}
				answered, err := a.askSheet(ctx, c, &j)
				if err != nil || !answered {
					return err
				}
				return a.followAnswered(ctx, c, evs, j.ID, noWait)
			}
			var next library.Job
			if err := c.Do(ctx, "POST", "/v1/jobs/"+url.PathEscape(j.ID)+"/sheet", ans, &next); err != nil {
				return err
			}
			return a.followAnswered(ctx, c, evs, j.ID, noWait)
		},
	}
	cmd.Flags().BoolVar(&split, "split", false, "build from the proposed views")
	cmd.Flags().BoolVar(&keep, "keep", false, "use the image whole")
	cmd.Flags().StringVar(&views, "views", "", "a view per figure, in order: front, left, back, right or -")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "answer without following the job")
	return calls(cmd, watchRoutes...)
}

func (a *app) followAnswered(ctx context.Context, c *Client, evs <-chan Event, id string, noWait bool) error {
	var j library.Job
	if err := c.Do(ctx, "GET", "/v1/jobs/"+url.PathEscape(id), nil, &j); err != nil {
		return err
	}
	if noWait {
		a.print(j, func() { a.printf("%s %s\n", j.ID, j.Status) })
		return nil
	}
	return a.watch(ctx, c, evs, []*library.Job{&j})
}
