package jobs

import (
	"log/slog"

	"forja/internal/glb"
)

// AssetStats reads an asset's triangle, vertex, material and LOD counts from its GLB. It is
// nil when the file is not a GLB the core can read; the asset is kept all the same.
func AssetStats(path string, log *slog.Logger) map[string]any {
	st, err := glb.ReadFile(path)
	if err != nil {
		log.Warn("could not read the asset's stats", "err", err)
		return nil
	}
	return st.Map()
}
