package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"forja/internal/glb"
	"forja/internal/library"
	"forja/internal/materials"
)

// AssetMaterials are the materials of an asset and how rough and metallic each is now.
type AssetMaterials struct {
	// Materials are the dominant colors of the base color texture, largest share first. Empty
	// with a Reason when the asset cannot be edited.
	Materials []materials.Material `json:"materials"`
	// Painted says the asset's model painted its roughness and metallic: setting them replaces
	// that work, so a request must say Overwrite.
	Painted bool `json:"painted"`
	// Reason says why there are no materials to set, in words for a person.
	Reason string `json:"reason,omitempty"`
}

// MaterialSet changes one material's values; a value left out keeps what the material has.
type MaterialSet struct {
	Index     int      `json:"index"`
	Roughness *float64 `json:"roughness,omitempty"`
	Metallic  *float64 `json:"metallic,omitempty"`
}

// MaterialsRequest sets roughness and metallic for some of an asset's materials, each from 0
// to 1. Materials it leaves out keep their values.
type MaterialsRequest struct {
	Materials []MaterialSet `json:"materials"`
	// Overwrite lets the request replace roughness and metallic that the asset's model painted.
	Overwrite bool `json:"overwrite,omitempty"`
}

// analyze reads an asset's GLB and finds its materials.
func (s *Server) analyze(a *library.Asset) (*materials.Analysis, error) {
	path, err := s.Store.Path(a.GLB)
	if err != nil || !s.Store.Has(a.GLB) {
		return nil, errors.New("the GLB of " + a.Name + " is missing from the store")
	}
	f, err := glb.Load(path)
	if err != nil {
		return nil, err
	}
	return materials.Analyze(f, a.Materials)
}

// sentence makes an error read as a sentence.
func sentence(err error) string {
	msg := err.Error()
	if r, n := utf8.DecodeRuneInString(msg); n > 0 {
		msg = string(unicode.ToUpper(r)) + msg[n:]
	}
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	return msg
}

func (s *Server) assetMaterialsGet(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	an, err := s.analyze(a)
	if err != nil {
		// Not a failure of the request: this asset has no materials to set.
		writeJSON(w, 200, AssetMaterials{Materials: []materials.Material{}, Reason: sentence(err)})
		return
	}
	writeJSON(w, 200, AssetMaterials{Materials: an.Materials, Painted: an.Painted})
}

// assetMaterialsSet writes the roughness and metallic of an asset's materials into a new GLB
// and points the asset at it. The old GLB stays in the store until the next sweep.
func (s *Server) assetMaterialsSet(w http.ResponseWriter, r *http.Request) {
	var req MaterialsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		problem(w, 400, `want {"materials": [{"index": 0, "roughness": 0.5, "metallic": 0}]}`)
		return
	}
	sets := make([]materials.Set, len(req.Materials))
	for i, m := range req.Materials {
		sets[i] = materials.Set{Index: m.Index, Roughness: m.Roughness, Metallic: m.Metallic}
	}
	// One edit of an asset at a time: two would each start from the file the other replaces.
	s.materialsMu.Lock()
	defer s.materialsMu.Unlock()
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	an, err := s.analyze(a)
	if err != nil {
		problem(w, 422, sentence(err))
		return
	}
	out, choices, err := an.Edit(sets, req.Overwrite)
	if errors.Is(err, materials.ErrPainted) {
		problemWith(w, 409, sentence(err), map[string]any{"painted": true})
		return
	}
	if err != nil {
		// A request that names no such material, or a texture that cannot be read.
		problem(w, 422, sentence(err))
		return
	}
	ref, err := s.Store.PutBytes(out)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	// Triangles and vertices cannot change, but the stats are read from the file they describe.
	stats := a.Stats
	if st, err := glb.Read(bytes.NewReader(out)); err == nil {
		stats = st.Map()
	}
	a, err = s.DB.SetAssetGLB(r.Context(), a.ID, ref, stats, choices)
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	// A copy made for dragging out holds the old file.
	dropDrag(a.ID)
	s.Hub.Publish(EvAssetUpdated, a)
	writeJSON(w, 200, AssetMaterials{Materials: an.Materials, Painted: an.Painted})
}
