package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"forja/internal/api"
	"forja/internal/export"
	"forja/internal/library"
)

func (a *app) assetsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "assets", Short: "Browse, export, open in Blender and delete the library's finished assets"}
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
	cmd.AddCommand(a.exportCmd(), a.formatsCmd(), a.blenderCmd(), a.rmCmd())
	return cmd
}

func (a *app) exportCmd() *cobra.Command {
	var format, out string
	cmd := calls(&cobra.Command{
		Use:   "export <asset-id>...",
		Short: "Export assets to GLB, FBX, OBJ, glTF, USDZ, STL or a Blender file",
		Long: "Writes each asset in --format: to --output when it names a file of that format, into it\n" +
			"when it is a folder, else into the current folder, named after the asset. Every format\n" +
			"but GLB is converted by Blender; `forja assets formats` says which this computer can write.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dest, err := filepath.Abs(out)
			if err != nil {
				return err
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			var res api.ExportResult
			req := api.ExportRequest{IDs: args, Format: strings.TrimPrefix(strings.ToLower(format), "."), Path: dest}
			if err := c.Do(context.Background(), "POST", "/v1/export", req, &res); err != nil {
				return err
			}
			failed := 0
			for _, as := range res.Assets {
				if as.Error != "" {
					failed++
				}
			}
			a.print(res, func() {
				for _, as := range res.Assets {
					for _, f := range as.Files {
						a.printf("%s\n", f)
					}
					if as.Error != "" {
						fmt.Fprintf(a.errOut, "%s: %s\n", as.Asset, as.Error)
					}
				}
			})
			if failed > 0 {
				return fmt.Errorf("%d of %d assets could not be exported", failed, len(res.Assets))
			}
			return nil
		},
	}, "POST /v1/export")
	cmd.Flags().StringVarP(&format, "format", "f", "glb", "glb, fbx, obj, gltf, usdz, stl or blend")
	cmd.Flags().StringVarP(&out, "output", "o", ".", "a file of that format, or a folder to write into")
	return cmd
}

func (a *app) formatsCmd() *cobra.Command {
	return calls(&cobra.Command{
		Use: "formats", Short: "List the formats assets export to, and the Blender that converts them", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			var view api.ExportFormats
			if err := a.get("/v1/export/formats", &view); err != nil {
				return err
			}
			a.print(view, func() {
				a.table("FORMAT\tFILE\tREADY\tFOR", func(row func(...any)) {
					for _, f := range view.Formats {
						ready := "yes"
						if !f.Available {
							ready = "needs Blender"
						}
						row(f.ID, f.Ext, ready, f.For)
					}
				})
				if view.Blender != nil {
					a.printf("\nBlender %s at %s\n", view.Blender.Version, view.Blender.Path)
				} else {
					a.printf("\n%s\n", view.BlenderProblem)
				}
			})
			return nil
		},
	}, "GET /v1/export/formats")
}

func (a *app) blenderCmd() *cobra.Command {
	return calls(&cobra.Command{
		Use:   "blender <asset-id>",
		Short: "Open an asset in Blender, in a new unsaved file",
		Long: "Starts Blender, with your add-ons and preferences, and imports the asset with its lower\n" +
			"LODs hidden. Set Blender's path with `forja config set blender.path=<path>` when it is\n" +
			"not where its installer puts it.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			var b export.Blender
			if err := c.Do(context.Background(), "POST", "/v1/assets/"+url.PathEscape(args[0])+"/blender", nil, &b); err != nil {
				return err
			}
			a.print(b, func() { a.printf("Opened %s in Blender %s\n", args[0], b.Version) })
			return nil
		},
	}, "POST /v1/assets/{id}/blender")
}

func (a *app) rmCmd() *cobra.Command {
	var yes bool
	cmd := calls(&cobra.Command{
		Use:   "rm <asset-id>...",
		Short: "Delete assets, and the jobs that made them, from the library",
		Long: "Removes each asset and its job from the library. Files exported into projects or saved\n" +
			"elsewhere stay where they are.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if !yes {
				if !a.tty {
					return errors.New("deleting asks first: pass --yes when no one at a terminal can answer")
				}
				what := "this asset"
				if len(args) > 1 {
					what = fmt.Sprintf("these %d assets", len(args))
				}
				if !a.confirm("Delete " + what + " from the library? Files exported from it stay.") {
					return errors.New("nothing deleted")
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			deleted := []string{}
			var failed []error
			for _, id := range args {
				if err := c.Do(context.Background(), "DELETE", "/v1/assets/"+url.PathEscape(id), nil, nil); err != nil {
					failed = append(failed, fmt.Errorf("%s: %w", id, err))
					continue
				}
				deleted = append(deleted, id)
			}
			a.print(deleted, func() {
				for _, id := range deleted {
					a.printf("Deleted %s\n", id)
				}
			})
			return errors.Join(failed...)
		},
	}, "DELETE /v1/assets/{id}")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking")
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
