package cli

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/config"
)

func (a *app) pricesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "prices",
		Short: "Show and change the price table that estimates and spending limits use",
		Long: "What paid providers charge, in USD: a second or an hour of a rented GPU, or one generation of a\n" +
			"hosted model. Vendors change their prices, so the table is yours to edit: an entry named after a\n" +
			"hosted model replaces the price its provider reports. Entries go to config.toml.",
	}
	cmd.AddCommand(calls(&cobra.Command{
		Use: "ls", Short: "List the price table",
		RunE: func(*cobra.Command, []string) error {
			var view api.PricesView
			if err := a.get("/v1/prices", &view); err != nil {
				return err
			}
			a.print(view, func() {
				a.table("ID\tPRICE\tFOR\tSET BY", func(row func(...any)) {
					for _, p := range view.Prices {
						row(p.ID, price(p.USD, p.Per), orDash(strings.Join(p.For, ", "), p.Label), p.Source)
					}
				})
			})
			return nil
		},
	}, "GET /v1/prices"))

	var per, label string
	set := calls(&cobra.Command{
		Use:   "set <id> <usd>",
		Short: "Set a price: forja prices set runpod-serverless-4090 0.00034 --per second",
		Long: "The id is a price table entry, such as runpod-serverless-4090, or a hosted model's id, such as\n" +
			"fal-ai/trellis-2, whose own price it then replaces.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			usd, err := strconv.ParseFloat(strings.TrimPrefix(args[1], "$"), 64)
			if err != nil {
				return fmt.Errorf("%q is not an amount in USD", args[1])
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var view api.PricesView
			body := config.Price{USD: usd, Per: per, Label: label}
			if err := c.Do(context.Background(), "PUT", pricePath(args[0]), body, &view); err != nil {
				return err
			}
			a.print(view, func() { a.printf("%s is now %s.\n", args[0], price(usd, per)) })
			return nil
		},
	}, "PUT /v1/prices/{id...}")
	set.Flags().StringVar(&per, "per", config.PerGeneration, "what the price is per: "+strings.Join(config.Units, ", "))
	set.Flags().StringVar(&label, "label", "", "a name for the entry, such as the GPU it rents")
	cmd.AddCommand(set)

	cmd.AddCommand(calls(&cobra.Command{
		Use:   "rm <id>",
		Short: "Remove your price: a built-in entry goes back to its default, a hosted model to its provider's price",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var view api.PricesView
			if err := c.Do(context.Background(), "DELETE", pricePath(args[0]), nil, &view); err != nil {
				return err
			}
			a.print(view, func() {})
			return nil
		},
	}, "DELETE /v1/prices/{id...}"))
	return cmd
}

// pricePath is an entry's address; a hosted model's id keeps its slashes.
func pricePath(id string) string {
	parts := strings.Split(id, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/v1/prices/" + strings.Join(parts, "/")
}

func price(usd float64, per string) string {
	amount := strconv.FormatFloat(usd, 'f', -1, 64)
	if per == config.PerGeneration {
		per = "image"
	}
	return "$" + amount + " per " + strings.TrimSuffix(per, "s")
}
