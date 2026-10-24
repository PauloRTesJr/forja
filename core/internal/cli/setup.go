package cli

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/library"
)

func (a *app) setupCmd() *cobra.Command {
	var yes, accept, noTest bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install what the built-in presets need on this computer, then test them end to end",
		Long: "Shows, for each kind of stage the built-in presets need, the model that covers it or the\n" +
			"best one this computer can run. After you agree (or with --yes) it installs those models,\n" +
			"then generates a sample image to prove they work together. Safe to re-run.",
		RunE: func(*cobra.Command, []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			ctx := context.Background()
			var view api.SetupView
			if err := c.Do(ctx, "GET", "/v1/setup", nil, &view); err != nil {
				return err
			}
			a.print(view, func() { a.showSetup(view) })
			if len(view.Install) > 0 {
				what := fmt.Sprintf("Install %s (%.1f GB)?", strings.Join(view.Install, ", "), view.DiskGB)
				switch {
				case yes:
				case a.tty && !a.json:
					if !a.confirm(what) {
						return nil
					}
				default:
					if !a.json {
						a.printf("Re-run with --yes to install them.\n")
					}
					return nil
				}
				for _, id := range view.Install {
					if err := a.runTask(id, "POST", "/v1/models/"+url.PathEscape(id)+"/install", map[string]any{}, accept); err != nil {
						return err
					}
				}
				if err := c.Do(ctx, "GET", "/v1/setup", nil, &view); err != nil {
					return err
				}
			}
			if !view.Ready {
				if !a.json {
					a.printf("Some stages still have no model here; add another computer (forja providers add) or a hub (forja hubs connect).\n")
				}
				return &exitError{1, "setup is not complete"}
			}
			if noTest {
				return nil
			}
			if !a.json {
				a.printf("Testing with a sample image...\n")
			}
			sctx, stop := signalContext()
			defer stop()
			evs, err := c.Events(sctx)
			if err != nil {
				return err
			}
			var job library.Job
			if err := c.Do(sctx, "POST", "/v1/setup/test", nil, &job); err != nil {
				return err
			}
			return a.watch(sctx, c, evs, []*library.Job{&job})
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install without asking")
	cmd.Flags().BoolVar(&accept, "accept-license", false, "accept restrictive model licenses without asking")
	cmd.Flags().BoolVar(&noTest, "no-test", false, "skip the test generation at the end")
	routes := []string{"GET /v1/setup", "POST /v1/models/{id}/install", "POST /v1/setup/test"}
	return calls(cmd, slices.Concat(routes, followRoutes, watchRoutes)...)
}

func (a *app) showSetup(v api.SetupView) {
	if os, _ := v.System["os"].(string); os != "" {
		a.printf("This computer: %s\n", os)
	}
	devices, _ := v.System["devices"].([]any)
	for _, d := range devices {
		dev, _ := d.(map[string]any)
		if dev["backend"] == "cpu" {
			continue
		}
		vram, _ := dev["vram_bytes"].(float64)
		a.printf("  %v (%v), %.1f GB VRAM\n", dev["name"], dev["backend"], vram/(1<<30))
	}
	if free, ok := v.System["disk_free_bytes"].(float64); ok {
		a.printf("  %.0f GB free on disk\n", free/(1<<30))
	}
	a.printf("\n")
	a.table("STAGE TYPE\tSTATUS\tMODEL\tWHERE\tDISK", func(row func(...any)) {
		for _, s := range v.Stages {
			where, disk := orDash(s.Provider), "-"
			switch s.Status {
			case "install":
				where = "this computer, " + s.Fit
				disk = fmt.Sprintf("%.1f GB", s.DiskGB)
			case "remote":
				where = "needs another computer or a hub"
			}
			row(s.Use, s.Status, orDash(s.ModelName, s.Model), where, disk)
		}
	})
	if v.Ready {
		a.printf("\nEvery built-in preset can run.\n")
	}
}
