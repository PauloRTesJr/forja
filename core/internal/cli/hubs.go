package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/engineapi"
	"forja/internal/provider"
)

func (a *app) hubsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hubs",
		Short: "Connect model hubs such as fal.ai, browse their catalogs and choose models",
		Long: "A model hub runs hosted models for a fee, with your API key. Hosted models run only when a\n" +
			"stage is pinned to them (forja models pin), and the budget limits in config.toml apply.",
	}
	cmd.AddCommand(calls(&cobra.Command{
		Use: "ls", Short: "List connected hubs",
		RunE: func(*cobra.Command, []string) error {
			var rows []api.ProviderRow
			if err := a.get("/v1/providers", &rows); err != nil {
				return err
			}
			hubs := []api.ProviderRow{}
			for _, r := range rows {
				if r.Kind == string(provider.KindAPI) {
					hubs = append(hubs, r)
				}
			}
			a.print(hubs, func() {
				a.table("ID\tNAME\tKEY\tHEALTH", func(row func(...any)) {
					for _, h := range hubs {
						row(h.ID, h.Label, keyHint(h.Key), health(h))
					}
				})
			})
			return nil
		},
	}, "GET /v1/providers"))

	cmd.AddCommand(calls(&cobra.Command{
		Use:   "connect <hub>",
		Short: "Connect a hub with its API key, read from stdin (fal is the one hub so far)",
		Long: "Reads the API key from stdin, tests it, keeps it in the OS keychain and adds Forja's\n" +
			"recommended models. For example: forja hubs connect fal < fal-key.txt",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			key, err := a.readSecret("API key")
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var added api.AddedProvider
			body := map[string]string{"kind": string(provider.KindAPI), "vendor": args[0], "token": key}
			if err := c.Do(context.Background(), "POST", "/v1/providers", body, &added); err != nil {
				return err
			}
			a.print(added, func() {
				a.printf("Connected %s as %s, with Forja's recommended models.\n", added.Label, added.ID)
				a.printf("Browse more: forja hubs search %s <words>\n", added.ID)
			})
			return nil
		},
	}, "POST /v1/providers"))

	var cursor string
	search := calls(&cobra.Command{
		Use: "search <hub> [words]...", Short: "Search a hub's catalog for models Forja can use", Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			q := url.Values{"q": {strings.Join(args[1:], " ")}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			var page engineapi.CatalogPage
			if err := a.get("/v1/providers/"+url.PathEscape(args[0])+"/catalog?"+q.Encode(), &page); err != nil {
				return err
			}
			a.print(page, func() {
				a.table("ID\tNAME\tTASKS\tPRICE\tNOTE", func(row func(...any)) {
					for _, m := range page.Models {
						price := "-"
						if m.Price != nil {
							price = fmt.Sprintf("%s per %s", money(m.Price.Amount, m.Price.Currency), m.Price.Unit)
						}
						var note []string
						if m.Added {
							note = append(note, "added")
						}
						if m.Recommended {
							note = append(note, "recommended")
						}
						if m.Problem != "" {
							note = append(note, "cannot use: "+m.Problem)
						}
						row(m.ID, m.Name, orDash(strings.Join(m.Tasks, ",")), price, orDash(strings.Join(note, "; ")))
					}
				})
				if page.NextCursor != "" {
					a.printf("\nMore: forja hubs search %s --cursor %s", args[0], page.NextCursor)
					if len(args) > 1 {
						a.printf(" %s", strings.Join(args[1:], " "))
					}
					a.printf("\n")
				}
			})
			return nil
		},
	}, "GET /v1/providers/{id}/catalog")
	search.Flags().StringVar(&cursor, "cursor", "", "the next page, as the previous search printed it")
	cmd.AddCommand(search)

	cmd.AddCommand(calls(&cobra.Command{
		Use: "add <hub> <model>...", Short: "Add catalog models to a hub's list, after checking Forja can use them",
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, m := range args[1:] {
				path := "/v1/providers/" + url.PathEscape(args[0]) + "/models"
				if err := c.Do(context.Background(), "POST", path, map[string]string{"model": m}, nil); err != nil {
					return fmt.Errorf("%s: %w", m, err)
				}
			}
			return nil
		},
	}, "POST /v1/providers/{id}/models"))
	cmd.AddCommand(calls(&cobra.Command{
		Use: "rm <hub> <model>...", Short: "Remove models from a hub's list (a stage pinned to one goes back to automatic)",
		Args: cobra.MinimumNArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			for _, m := range args[1:] {
				path := "/v1/providers/" + url.PathEscape(args[0]) + "/models?model=" + url.QueryEscape(m)
				if err := c.Do(context.Background(), "DELETE", path, nil, nil); err != nil {
					return fmt.Errorf("%s: %w", m, err)
				}
			}
			return nil
		},
	}, "DELETE /v1/providers/{id}/models"))
	return cmd
}

func (a *app) spendCmd() *cobra.Command {
	return calls(&cobra.Command{
		Use: "spend", Short: "Show what hosted models cost today and this month, against the budget",
		RunE: func(*cobra.Command, []string) error {
			var s api.SpendView
			if err := a.get("/v1/spend", &s); err != nil {
				return err
			}
			a.print(s, func() {
				limit := func(v float64) string {
					if v <= 0 {
						return "no limit"
					}
					return "limit " + money(v, s.Currency)
				}
				a.printf("today       %s  (%s)\n", money(s.Today, s.Currency), limit(s.Limits.PerDay))
				a.printf("this month  %s  (%s)\n", money(s.Month, s.Currency), limit(s.Limits.PerMonth))
				a.printf("per image   %s\n", limit(s.Limits.PerJob))
				a.printf("Change the limits with forja config set routing.budget_usd.per_day=<amount> (per_job, per_month).\n")
			})
			return nil
		},
	}, "GET /v1/spend")
}

func keyHint(k *api.KeyView) string {
	switch {
	case k == nil:
		return "-"
	case !k.Stored:
		return "missing from " + k.Store
	case k.Hint != "":
		return k.Hint
	}
	return "stored"
}

func health(r api.ProviderRow) string {
	switch {
	case r.Healthy:
		return fmt.Sprintf("ok, %d ms", r.Latency)
	case r.Error == "idle":
		return "idle"
	case r.Error != "":
		return r.Error
	}
	return "-"
}
