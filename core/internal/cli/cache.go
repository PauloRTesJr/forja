package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/cache"
)

func (a *app) cacheCmd() *cobra.Command {
	cmd := calls(&cobra.Command{
		Use:   "cache",
		Short: "Show the disk the cache and the library use",
		Long: "The cache holds stage outputs a new job can reuse, and is trimmed, least recently used\n" +
			"first, to stay under general.cache_max_gb (`forja config set general.cache_max_gb=<GB>`;\n" +
			"0 means no limit). The library's own files (assets, their images, unfinished jobs)\n" +
			"never count against it and are never trimmed.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			var u cache.Usage
			if err := a.get("/v1/cache", &u); err != nil {
				return err
			}
			a.print(u, func() { a.usage(u) })
			return nil
		},
	}, "GET /v1/cache")
	cmd.AddCommand(calls(&cobra.Command{
		Use:   "clear",
		Short: "Remove every reusable stage output now; the library keeps its files",
		Long: "Frees the cache's disk at once. Jobs run later compute their stages again. Files used in\n" +
			"the last hour stay, and nothing is removed while jobs run.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var res api.CacheCleared
			if err := c.Do(context.Background(), "DELETE", "/v1/cache", nil, &res); err != nil {
				return err
			}
			a.print(res, func() {
				a.printf("Freed %s (%d files).\n", size(res.Freed.Bytes), res.Freed.Objects)
				a.usage(res.Usage)
			})
			return nil
		},
	}, "DELETE /v1/cache"))
	return cmd
}

func (a *app) usage(u cache.Usage) {
	limit := "no limit"
	if u.LimitBytes > 0 {
		limit = "of " + size(u.LimitBytes)
	}
	a.printf("cache    %s %s: stage outputs new jobs can reuse\n", size(u.CacheBytes), limit)
	a.printf("library  %s: assets, their images and unfinished jobs\n", size(u.LibraryBytes))
}

// size writes a byte count the way a file manager does.
func size(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v, unit := float64(n)/1024, 0
	for v >= 1024 && unit < 3 {
		v /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", v, []string{"KB", "MB", "GB", "TB"}[unit])
}
