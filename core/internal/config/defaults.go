package config

// Defaults is the lowest config layer. Every hardcoded default lives here and nowhere else.
func Defaults() map[string]any {
	return map[string]any{
		"general": map[string]any{
			"language":     "en",
			"cache_max_gb": int64(50),
		},
		"engine": map[string]any{
			"idle_unload_minutes": int64(15),
			"vram_reserve_gb":     1.0,
			// Offline beats speed when they conflict (docs/decisions.md, decision 5).
			"fit_mode":      "fit-first",
			"fairness_cap":  int64(8),
			"start_timeout": "60s",
		},
		"routing": map[string]any{
			"policy":              "prefer-local",
			"allow_paid_fallback": false,
			"priority":            []any{"local"},
		},
		"project": map[string]any{
			"export_dir":     "Assets/Generated",
			"default_preset": "mobile-prop",
		},
	}
}
