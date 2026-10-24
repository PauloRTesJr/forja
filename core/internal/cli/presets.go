package cli

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/config"
)

func (a *app) presetsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "presets", Short: "Inspect presets and how they would run now"}
	var project, views string
	projectOr := func() string {
		if project != "" {
			return project
		}
		if wd, err := os.Getwd(); err == nil {
			return config.FindProject(wd)
		}
		return ""
	}
	viewList := func() []string {
		if views == "" {
			return nil
		}
		return strings.Split(views, ",")
	}
	ls := calls(&cobra.Command{
		Use: "ls", Short: "List presets, whether each can run, and the models it would use",
		RunE: func(*cobra.Command, []string) error {
			q := url.Values{"fresh": {"1"}}
			if p := projectOr(); p != "" {
				q.Set("project", p)
			}
			if views != "" {
				q.Set("views", views)
			}
			var list []api.PresetView
			if err := a.get("/v1/presets?"+q.Encode(), &list); err != nil {
				return err
			}
			a.print(list, func() {
				a.table("ID\tNAME\tREADY\tMODELS\tSOURCE", func(row func(...any)) {
					for _, p := range list {
						var models []string
						for _, pick := range p.Plan {
							switch {
							case pick.Missing:
								models = append(models, fmt.Sprintf("%s: none ready", pick.Use))
							case pick.Model != "" && !pick.Skipped:
								models = append(models, pick.Model)
							}
						}
						ready := "yes"
						if !p.Ready {
							ready = "no"
						}
						row(p.ID, p.Name, ready, strings.Join(models, ", "), p.Source)
					}
				})
			})
			return nil
		},
	}, "GET /v1/presets")
	show := calls(&cobra.Command{
		Use: "show <preset>", Short: "Show a preset's stages: the model, provider and price of each", Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p, err := a.preset(args[0], projectOr(), viewList())
			if err != nil {
				return err
			}
			a.print(p, func() { a.showPlan(p) })
			return nil
		},
	}, "GET /v1/presets")
	for _, c := range []*cobra.Command{ls, show} {
		c.Flags().StringVar(&project, "project", "", "project root holding forja.toml (default: found from the cwd)")
		c.Flags().StringVar(&views, "views", "", "plan for a job that also brings these views, e.g. back,left")
		cmd.AddCommand(c)
	}
	return cmd
}
