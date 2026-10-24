package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Price is one entry of the price table, in USD: what a second or an hour of a provider's GPU
// costs, or one generation of a hosted model. Vendors change prices, so the user edits them;
// the built-in entries are defaults.
type Price struct {
	Label string  `toml:"label" json:"label,omitempty"`
	USD   float64 `toml:"usd" json:"usd"`
	Per   string  `toml:"per" json:"per"`
}

// The units a price is per.
const (
	PerSecond     = "second"
	PerHour       = "hour"
	PerGeneration = "generation"
)

// Units lists the units a price may be per.
var Units = []string{PerSecond, PerHour, PerGeneration}

// PerSecondUSD is what one second costs at this price; 0 for a price per generation.
func (p Price) PerSecondUSD() float64 {
	switch p.Per {
	case PerSecond:
		return p.USD
	case PerHour:
		return p.USD / 3600
	}
	return 0
}

func (c *Config) validatePrices() error {
	for id, p := range c.Prices {
		// A table whose keys were all removed is left in the file as an empty header.
		if p == (Price{}) {
			delete(c.Prices, id)
			continue
		}
		if !slices.Contains(Units, p.Per) {
			return fmt.Errorf("prices.%s.per: %q is not %s", id, p.Per, strings.Join(Units, ", "))
		}
		if p.USD < 0 {
			return fmt.Errorf("prices.%s.usd: %v is below 0", id, p.USD)
		}
	}
	return nil
}

// ProviderConfig returns the [[providers]] entry with this id.
func (c *Config) ProviderConfig(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Rate is what one second on a provider costs and the price table entry it comes from ("" for
// the provider's own price_usd_per_second): the entry its `price` names, else the first one
// named after its vendor and kind, such as "runpod-serverless-4090" for a RunPod serverless
// endpoint. 0 when nothing prices its time.
func (c *Config) Rate(providerID string) (float64, string) {
	pc, ok := c.ProviderConfig(providerID)
	if !ok {
		return 0, ""
	}
	if pc.PriceUSDPerSecond > 0 {
		return pc.PriceUSDPerSecond, ""
	}
	if pc.Price != "" {
		if p, ok := c.Prices[pc.Price]; ok && p.Per != PerGeneration {
			return p.PerSecondUSD(), pc.Price
		}
		return 0, ""
	}
	if pc.Vendor == "" {
		return 0, ""
	}
	prefix := pc.Vendor + "-" + pc.Kind
	ids := make([]string, 0, len(c.Prices))
	for id := range c.Prices {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if p := c.Prices[id]; (id == prefix || strings.HasPrefix(id, prefix+"-")) && p.Per != PerGeneration {
			return p.PerSecondUSD(), id
		}
	}
	return 0, ""
}
