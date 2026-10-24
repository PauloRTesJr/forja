package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/library"
)

func (a *app) assetsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "assets", Short: "Browse the library of finished assets and save their GLBs"}
	var limit int
	var before string
	ls := calls(&cobra.Command{
		Use: "ls [search]", Short: "List assets, newest first; a search matches names and presets", Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			q, err := pageQuery(url.Values{}, limit, before)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				q.Set("q", args[0])
			}
			var list []*library.Asset
			if err := a.get("/v1/assets?"+q.Encode(), &list); err != nil {
				return err
			}
			a.print(list, func() {
				a.table("ID\tNAME\tPRESET\tTRIANGLES\tCREATED", func(row func(...any)) {
					for _, as := range list {
						row(as.ID, as.Name, as.Preset, stat(as.Stats, "triangles"), as.CreatedAt.Local().Format(time.DateTime))
					}
				})
			})
			return nil
		},
	}, "GET /v1/assets")
	ls.Flags().IntVar(&limit, "limit", 50, "how many assets (up to 500)")
	ls.Flags().StringVar(&before, "before", "", "only assets created before this date or time")
	cmd.AddCommand(ls)

	cmd.AddCommand(calls(&cobra.Command{
		Use: "show <asset-id>", Short: "Show an asset: its mesh, where it was exported and the job that made it",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			var as library.Asset
			if err := a.get("/v1/assets/"+url.PathEscape(args[0]), &as); err != nil {
				return err
			}
			a.print(as, func() {
				a.printf("%s  %s\n", as.ID, as.Name)
				a.printf("  preset     %s\n", as.Preset)
				a.printf("  created    %s\n", as.CreatedAt.Local().Format(time.DateTime))
				a.printf("  job        %s\n", as.JobID)
				a.printf("  glb        %s\n", as.GLB)
				for _, k := range []string{"triangles", "vertices", "materials", "lods"} {
					if v := stat(as.Stats, k); v != "-" {
						a.printf("  %-10s %s\n", k, v)
					}
				}
				if as.ExportPath != "" {
					a.printf("  exported   %s\n", as.ExportPath)
				}
			})
			return nil
		},
	}, "GET /v1/assets/{id}"))

	var out string
	get := calls(&cobra.Command{
		Use:   "get <asset-id>...",
		Short: "Save assets' GLBs",
		Long: "Copies each asset's GLB out of the library: to --output when it names a .glb file, into it\n" +
			"when it is a folder, else into the current folder, named after the asset.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			if len(args) > 1 && strings.EqualFold(filepath.Ext(out), ".glb") {
				return fmt.Errorf("--output %s names one file, but %d assets were given: name a folder", out, len(args))
			}
			var saved []string
			for _, id := range args {
				var as library.Asset
				if err := c.Do(context.Background(), "GET", "/v1/assets/"+url.PathEscape(id), nil, &as); err != nil {
					return fmt.Errorf("%s: %w", id, err)
				}
				dest := out
				if !strings.EqualFold(filepath.Ext(dest), ".glb") {
					dest = filepath.Join(dest, fileStem(as.Name)+".glb")
				}
				if err := a.save(c, as.GLB, dest); err != nil {
					return err
				}
				saved = append(saved, dest)
			}
			a.print(saved, func() {
				for _, p := range saved {
					a.printf("%s\n", p)
				}
			})
			return nil
		},
	}, "GET /v1/assets/{id}", "GET /v1/artifacts/{ref}")
	get.Flags().StringVarP(&out, "output", "o", "", "a .glb file, or a folder to save into")
	cmd.AddCommand(get)
	return cmd
}

// save copies an artifact from the core's store to dest, through a temporary file so a
// failed copy never leaves half a GLB.
func (a *app) save(c *Client, ref, dest string) error {
	body, err := c.Open(context.Background(), "GET", "/v1/artifacts/"+url.PathEscape(ref), nil)
	if err != nil {
		return err
	}
	defer body.Close()
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".forja-*.part")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

// fileStem makes a file name from an asset's name, which may be the path of its image.
func fileStem(name string) string {
	base := filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '-'
		}
		return r
	}, base)
	if base == "" || base == "." {
		return "asset"
	}
	return base
}

// stat prints one of an asset's stats, or "-" when it has none.
func stat(stats map[string]any, key string) string {
	switch v := stats[key].(type) {
	case float64:
		return fmt.Sprintf("%.0f", v)
	case nil:
		return "-"
	default:
		return fmt.Sprint(v)
	}
}
