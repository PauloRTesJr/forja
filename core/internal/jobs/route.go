package jobs

import (
	"context"
	"math"
	"slices"
	"sort"

	"forja/internal/config"
	"forja/internal/provider"
)

// failed holds the providers a job's stages failed on, by stage id. A stage no longer goes to
// a provider it failed on; the job falls back to the next place that can run it.
type failed map[string]map[string]string

func (f failed) has(stage, provider string) bool {
	_, ok := f[stage][provider]
	return ok
}

func (f failed) add(stage, provider, reason string) {
	if f[stage] == nil {
		f[stage] = map[string]string{}
	}
	f[stage][provider] = reason
}

// offline is false for providers the offline-only policy keeps a stage away from: anything
// but this computer and the user's own engines.
func offline(cfg *config.Loaded, p provider.Provider) bool {
	return cfg.Routing.Policy != "offline-only" || p.Kind() == provider.KindLocal || p.Kind() == provider.KindEngine
}

// routing lists the configured providers in routing.priority that a stage may use: allowed by
// the policy and not failed on. A priority list that names no configured provider means this
// computer.
func (r *Runner) routing(cfg *config.Loaded, stage string, avoid failed) []string {
	priority := slices.DeleteFunc(slices.Clone(cfg.Routing.Priority), func(id string) bool {
		_, ok := r.providers.Get(id)
		return !ok
	})
	if len(priority) == 0 {
		priority = []string{LocalProvider}
	}
	var ids []string
	for _, id := range priority {
		p, ok := r.providers.Get(id)
		if !ok || !offline(cfg, p) || avoid.has(stage, id) {
			continue
		}
		ids = append(ids, id)
	}
	return ids
}

// label names a provider for people, as Settings shows it.
func (r *Runner) label(cfg *config.Loaded, id string) string {
	if id == LocalProvider {
		return "This computer"
	}
	if pc, ok := cfg.ProviderConfig(id); ok && pc.Label != "" {
		return pc.Label
	}
	return id
}

// byPolicy sorts the places a model stage can run by the routing policy (spec section 8):
// cheapest by what the stage is expected to cost there (free providers cost nothing), fastest
// by how long it took there before. prefer-local and offline-only keep the routing order, as do
// ties; a place without a number goes after those with one.
func (r *Runner) byPolicy(ctx context.Context, cfg *config.Loaded, opts []Pick) []Pick {
	var score func(Pick) (float64, bool)
	switch cfg.Routing.Policy {
	case "cheapest":
		score = func(p Pick) (float64, bool) {
			c := r.chargeOf(ctx, cfg, p)
			return c.USD(), c.Known()
		}
	case "fastest":
		score = func(p Pick) (float64, bool) {
			s := r.db.EstimateSeconds(ctx, p.Use, p.Model, p.Provider)
			return s, s > 0
		}
	default:
		return opts
	}
	keys := make([]float64, len(opts))
	for i, p := range opts {
		v, ok := score(p)
		if !ok {
			v = math.Inf(1)
		}
		keys[i] = v
	}
	order := make([]int, len(opts))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return keys[order[a]] < keys[order[b]] })
	out := make([]Pick, len(opts))
	for i, j := range order {
		out[i] = opts[j]
	}
	return out
}

// expect is how long a stage should take on a provider: the median of its runs there, else of
// its runs anywhere. from is "here", "elsewhere", or "" when it never ran.
func (r *Runner) expect(ctx context.Context, use, model, providerID string) (seconds float64, from string) {
	if s := r.db.EstimateSeconds(ctx, use, model, providerID); s > 0 {
		return s, "here"
	}
	if s := r.db.EstimateSecondsAnywhere(ctx, use, model); s > 0 {
		return s, "elsewhere"
	}
	return 0, ""
}
