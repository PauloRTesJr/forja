package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"forja/internal/api"
)

func (a *app) materialsCmd() *cobra.Command {
	var sets []string
	var overwrite bool
	cmd := calls(&cobra.Command{
		Use:   "materials <asset-id>",
		Short: "Show an asset's materials, or set how rough and how metallic each is",
		Long: "Materials are the few dominant colors of the asset's base color texture. Local painters make\n" +
			"base color only, so every material ships equally rough; set roughness and metallic (0 to 1)\n" +
			"for each and Forja writes them into the asset's metallic-roughness map, in place.\n\n" +
			"  forja assets materials <asset>\n" +
			"  forja assets materials <asset> --set 1.roughness=0.3 --set 1.metallic=1\n\n" +
			"An asset whose model painted its own roughness and metallic is left alone unless you pass\n" +
			"--overwrite.",
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path := "/v1/assets/" + url.PathEscape(args[0]) + "/materials"
			var view api.AssetMaterials
			if len(sets) == 0 {
				if overwrite {
					return errors.New("--overwrite goes with --set")
				}
				if err := a.get(path, &view); err != nil {
					return err
				}
			} else {
				req, err := parseMaterialSets(sets)
				if err != nil {
					return err
				}
				req.Overwrite = overwrite
				c, err := a.client()
				if err != nil {
					return err
				}
				if err := c.Do(context.Background(), "PUT", path, req, &view); err != nil {
					var apiErr *APIError
					if errors.As(err, &apiErr) && apiErr.Status == 409 {
						return fmt.Errorf("%w; pass --overwrite to replace it", apiErr)
					}
					return err
				}
			}
			a.print(view, func() {
				if view.Reason != "" {
					a.printf("%s\n", view.Reason)
					return
				}
				a.table("INDEX\tCOLOR\tSHARE\tROUGHNESS\tMETALLIC", func(row func(...any)) {
					for _, m := range view.Materials {
						row(m.Index, m.Color, fmt.Sprintf("%.0f%%", m.Share*100), m.Roughness, m.Metallic)
					}
				})
				if view.Painted {
					a.printf("\nThis asset's roughness and metallic were painted by its model; --set needs --overwrite.\n")
				}
			})
			return nil
		},
	}, "GET /v1/assets/{id}/materials", "PUT /v1/assets/{id}/materials")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "set a value, as <index>.roughness=<0 to 1> or <index>.metallic=<0 to 1>; repeat for more")
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace roughness and metallic that the asset's model painted")
	return cmd
}

// parseMaterialSets reads "1.roughness=0.3" flags into one request, a material's two values
// in one entry.
func parseMaterialSets(flags []string) (api.MaterialsRequest, error) {
	byIndex := map[int]*api.MaterialSet{}
	for _, f := range flags {
		bad := fmt.Errorf("--set %q: want <index>.roughness=<0 to 1> or <index>.metallic=<0 to 1>", f)
		key, val, ok := strings.Cut(f, "=")
		idx, field, ok2 := strings.Cut(key, ".")
		if !ok || !ok2 {
			return api.MaterialsRequest{}, bad
		}
		i, err := strconv.Atoi(strings.TrimSpace(idx))
		v, err2 := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil || err2 != nil {
			return api.MaterialsRequest{}, bad
		}
		m := byIndex[i]
		if m == nil {
			m = &api.MaterialSet{Index: i}
			byIndex[i] = m
		}
		switch strings.ToLower(strings.TrimSpace(field)) {
		case "roughness":
			m.Roughness = &v
		case "metallic":
			m.Metallic = &v
		default:
			return api.MaterialsRequest{}, bad
		}
	}
	var req api.MaterialsRequest
	for _, m := range byIndex {
		req.Materials = append(req.Materials, *m)
	}
	sort.Slice(req.Materials, func(i, j int) bool { return req.Materials[i].Index < req.Materials[j].Index })
	return req, nil
}
