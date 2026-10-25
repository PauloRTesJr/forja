package config

// Defaults is the lowest config layer. Every hardcoded default lives here and nowhere else.
func Defaults() map[string]any {
	return map[string]any{
		"general": map[string]any{
			"language": "en",
		},
		"engine": map[string]any{
			"idle_unload_minutes": int64(15),
			"vram_reserve_gb":     1.0,
			// Offline beats speed when they conflict (docs/decisions.md, decision 5).
			"fit_mode":      "fit-first",
			"fairness_cap":  int64(8),
			"start_timeout": "60s",
			"share":         false,
			"share_port":    int64(7861),
		},
		"routing": map[string]any{
			"policy":              "prefer-local",
			"allow_paid_fallback": false,
			"priority":            []any{"local"},
			// Spending limits for paid providers, in USD; 0 means no limit.
			"budget_usd": map[string]any{"per_job": 0.0, "per_day": 0.0, "per_month": 0.0},
		},
		"models": map[string]any{
			"stubs": false,
		},
		"blender": map[string]any{
			"path": "",
		},
		"project": map[string]any{
			"export_dir":     "Assets/Generated",
			"default_preset": "mobile-prop",
		},
		// A release fails on more than 10% regression in time or VRAM (spec, section 10).
		"bench": map[string]any{
			"threshold_pct": 10.0,
			"min_seconds":   0.05,
			"min_vram_mb":   64.0,
		},
		// What paid providers charge, for estimates and spending limits (spec section 8). Hosted
		// models report their own price; an entry named after a model id replaces it.
		"prices": map[string]any{
			"runpod-serverless-4090": map[string]any{"label": "RunPod serverless, RTX 4090 24 GB (flex worker)", "usd": 0.00031, "per": "second"},
			"runpod-pod-4090":        map[string]any{"label": "RunPod pod, RTX 4090 on demand", "usd": 0.34, "per": "hour"},
		},
	}
}
