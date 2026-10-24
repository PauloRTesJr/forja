package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/engineapi"
	"forja/internal/installs"
)

func (a *app) modelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "Install, inspect and choose models"}
	var (
		accept, noWeights bool
		lane, prov        string
	)
	install := calls(&cobra.Command{
		Use:   "install <model>...",
		Short: "Build a model's environment and fetch its pinned weights (also repairs one)",
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
				if err := a.runTask(id, "POST", "/v1/models/"+url.PathEscape(id)+"/install", body, accept); err != nil {
					return err
				}
			}
			return nil
		},
	}, append([]string{"POST /v1/models/{id}/install"}, followRoutes...)...)
	install.Flags().BoolVar(&accept, "accept-license", false, "accept restrictive model licenses without asking")
	install.Flags().BoolVar(&noWeights, "no-weights", false, "build the environment only")
	install.Flags().StringVar(&lane, "lane", "", "torch lane: cuda, rocm, mps or cpu (default: detected)")
	install.Flags().StringVar(&prov, "provider", "", "provider to install on (default: this computer)")
	cmd.AddCommand(install)

	var rmProvider string
	rm := calls(&cobra.Command{
		Use: "rm <model>...", Short: "Remove a model's weights, and its environment once unused",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, id := range args {
				path := "/v1/models/" + url.PathEscape(id)
				if rmProvider != "" {
					path += "?provider=" + url.QueryEscape(rmProvider)
				}
				if err := a.runTask(id, "DELETE", path, nil, false); err != nil {
					return err
				}
			}
			return nil
		},
	}, append([]string{"DELETE /v1/models/{id}"}, followRoutes...)...)
	rm.Flags().StringVar(&rmProvider, "provider", "", "provider to remove it from (default: this computer)")
	cmd.AddCommand(rm)

	var checksProvider string
	checks := calls(&cobra.Command{
		Use: "checks [model]...", Short: "Show what this computer needs before models install",
		RunE: func(_ *cobra.Command, args []string) error {
			q := url.Values{"models": {strings.Join(args, ",")}}
			if checksProvider != "" {
				q.Set("provider", checksProvider)
			}
			var out engineapi.Checks
			if err := a.get("/v1/checks?"+q.Encode(), &out); err != nil {
				return err
			}
			a.print(out, func() {
				for _, ch := range out.Checks {
					mark := "ok "
					if !ch.OK {
						mark = "NO "
					}
					a.printf("%s %-16s found %s; needs %s\n", mark, ch.Label, ch.Found, ch.Need)
					if ch.OK || ch.Fix == nil {
						continue
					}
					for _, s := range ch.Fix.Steps {
						a.printf("      %s\n", s)
					}
					switch {
					case ch.Fix.Runnable:
						a.printf("      or let Forja do it: forja models fix %s\n", ch.ID)
					case ch.Fix.Command != "":
						a.printf("      or run: %s\n", ch.Fix.Command)
					}
					if ch.Fix.URL != "" {
						a.printf("      see %s\n", ch.Fix.URL)
					}
				}
			})
			return nil
		},
	}, "GET /v1/checks")
	checks.Flags().StringVar(&checksProvider, "provider", "", "provider to check (default: this computer)")
	cmd.AddCommand(checks)

	var fixProvider string
	fix := calls(&cobra.Command{
		Use:   "fix <check>",
		Short: "Run the fix for a failed check, such as installing a toolchain",
		Long: "Runs the fix `forja models checks` offers for a check, on the computer the check is for.\n" +
			"A fix installs software, so a provider runs one only for a caller on its own machine.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := "/v1/checks/" + url.PathEscape(args[0]) + "/fix"
			if fixProvider != "" {
				path += "?provider=" + url.QueryEscape(fixProvider)
			}
			return a.runTask(args[0], "POST", path, nil, false)
		},
	}, append([]string{"POST /v1/checks/{id}/fix"}, followRoutes...)...)
	fix.Flags().StringVar(&fixProvider, "provider", "", "provider to fix (default: this computer)")
	cmd.AddCommand(fix)

	var all bool
	var lsProvider string
	ls := calls(&cobra.Command{
		Use: "ls", Short: "List models: this computer's, or with --all every provider's",
		RunE: func(*cobra.Command, []string) error {
			q := url.Values{"fresh": {"1"}}
			if all {
				q.Set("all", "1")
			} else if lsProvider != "" {
				q.Set("provider", lsProvider)
			}
			var models []api.ModelView
			if err := a.get("/v1/models?"+q.Encode(), &models); err != nil {
				return err
			}
			a.print(models, func() {
				a.table("ID\tPROVIDER\tTASKS\tLICENSE\tSTATUS\tFIT\tUSED FOR", func(row func(...any)) {
					for _, m := range models {
						status := m.Status
						if m.Paid && m.Price != nil {
							status = fmt.Sprintf("%s, %s per %s", status, money(m.Price.Amount, m.Price.Currency), m.Price.Unit)
						}
						var uses []string
						for _, u := range m.Uses {
							if u.Pinned {
								uses = append(uses, u.Use+" (pinned)")
							} else {
								uses = append(uses, u.Use)
							}
						}
						row(m.ID, m.Provider, strings.Join(m.Tasks, ","), m.License.SPDX, status, orDash(m.Fit),
							orDash(strings.Join(uses, ", ")))
					}
				})
			})
			return nil
		},
	}, "GET /v1/models")
	ls.Flags().BoolVar(&all, "all", false, "every provider's models, hosted ones included")
	ls.Flags().StringVar(&lsProvider, "provider", "", "one provider's models (default: this computer)")
	cmd.AddCommand(ls)

	var pinProvider string
	pin := calls(&cobra.Command{
		Use:   "pin <stage-type> <model>",
		Short: "Use one model for a stage type, such as shape, whenever a preset leaves it open",
		Long: "Pins the model a stage type uses, in your config.toml. --provider also says where it runs,\n" +
			"which a hosted model needs. `forja models unpin` goes back to picking by rank.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return a.pin(args[0], args[1], pinProvider)
		},
	}, "PUT /v1/models/stages/{use}", "GET /v1/models")
	pin.Flags().StringVar(&pinProvider, "provider", "", "the provider that runs it (default: found from the models)")
	cmd.AddCommand(pin)
	cmd.AddCommand(calls(&cobra.Command{
		Use: "unpin <stage-type>...", Short: "Go back to picking a stage type's model by rank", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			for _, use := range args {
				if err := a.pin(use, "", ""); err != nil {
					return err
				}
			}
			return nil
		},
	}, "PUT /v1/models/stages/{use}"))
	return cmd
}

// pin pins model to a stage type; an empty model unpins it. A model only one provider has
// is pinned with that provider, as the app does.
func (a *app) pin(use, model, prov string) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if model != "" && prov == "" {
		var models []api.ModelView
		if err := c.Do(ctx, "GET", "/v1/models?all=1", nil, &models); err != nil {
			return err
		}
		var found []string
		for _, m := range models {
			if m.ID == model {
				found = append(found, m.Provider)
				if !m.Does(use) {
					return fmt.Errorf("%s does not do %s: it does %s", model, use, strings.Join(m.Tasks, ", "))
				}
			}
		}
		switch len(found) {
		case 0:
			return fmt.Errorf("no provider has a model %s; `forja models ls --all` lists them", model)
		case 1:
			prov = found[0]
		default:
			return fmt.Errorf("%s runs on %s: say which with --provider", model, strings.Join(found, " and "))
		}
	}
	return c.Do(ctx, "PUT", "/v1/models/stages/"+url.PathEscape(use), map[string]string{"model": model, "provider": prov}, nil)
}

func (a *app) installsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "installs", Short: "List, follow and cancel model installs, removals and fixes"}
	cmd.AddCommand(calls(&cobra.Command{
		Use: "ls", Short: "List installs, removals and fixes, running and recent",
		RunE: func(*cobra.Command, []string) error {
			var list []installs.Entry
			if err := a.get("/v1/installs", &list); err != nil {
				return err
			}
			a.print(list, func() {
				a.table("TASK\tKIND\tTARGET\tSTATUS\tSTARTED", func(row func(...any)) {
					for _, e := range list {
						started := time.Unix(int64(e.CreatedAt), 0).Local().Format(time.DateTime)
						row(e.Provider+"/"+e.ID, e.Kind, e.Target, e.Status, started)
					}
				})
			})
			return nil
		},
	}, "GET /v1/installs"))
	cmd.AddCommand(calls(&cobra.Command{
		Use: "watch <task>", Short: "Follow a task (provider/id, as `forja installs ls` shows it) until it ends",
		Args: cobra.ExactArgs(1),
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
			e, err := findTask(ctx, c, args[0])
			if err != nil {
				return err
			}
			return a.follow(ctx, c, evs, *e)
		},
	}, append([]string{"GET /v1/installs"}, followRoutes...)...))
	cmd.AddCommand(calls(&cobra.Command{
		Use: "cancel <task>...", Short: "Cancel tasks (provider/id, as `forja installs ls` shows them)",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, arg := range args {
				e, err := findTask(context.Background(), c, arg)
				if err != nil {
					return err
				}
				path := "/v1/installs/" + url.PathEscape(e.Provider) + "/" + url.PathEscape(e.ID)
				if err := c.Do(context.Background(), "DELETE", path, nil, nil); err != nil {
					return fmt.Errorf("%s: %w", arg, err)
				}
			}
			return nil
		},
	}, "GET /v1/installs", "DELETE /v1/installs/{provider}/{id}"))
	return cmd
}

// findTask finds a task by provider/id, or by id alone when only one provider has it.
func findTask(ctx context.Context, c *Client, ref string) (*installs.Entry, error) {
	var list []installs.Entry
	if err := c.Do(ctx, "GET", "/v1/installs", nil, &list); err != nil {
		return nil, err
	}
	prov, id, scoped := strings.Cut(ref, "/")
	if !scoped {
		id = ref
	}
	var found []installs.Entry
	for _, e := range list {
		if e.ID == id && (!scoped || e.Provider == prov) {
			found = append(found, e)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no task %s; `forja installs ls` lists them", ref)
	case 1:
		return &found[0], nil
	}
	return nil, errors.New("tasks on several providers have id " + id + ": give provider/id")
}
