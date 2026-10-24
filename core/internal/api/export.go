package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"forja/internal/export"
	"forja/internal/library"
)

// EvAssetDeleted tells clients that an asset, and the job that made it, left the library.
const EvAssetDeleted = "asset.deleted"

// AssetDeleted is the data of an asset.deleted event.
type AssetDeleted struct {
	ID  string `json:"id"`
	Job string `json:"job"`
}

// ExportFormat is a format the library exports to, and whether this computer can now.
type ExportFormat struct {
	export.Format
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
}

// ExportFormats lists the export formats and the Blender that makes all but GLB.
type ExportFormats struct {
	Blender *export.Blender `json:"blender,omitempty"`
	// BlenderProblem says why there is no Blender to use: not found, or too old.
	BlenderProblem string         `json:"blender_problem,omitempty"`
	Formats        []ExportFormat `json:"formats"`
}

// ExportRequest exports assets: Path is the file to write when there is one asset and it
// ends in the format's extension, else the folder to write into.
type ExportRequest struct {
	IDs    []string `json:"ids"`
	Format string   `json:"format"`
	Path   string   `json:"path"`
}

// ExportResult says what became of each asset, in the order asked.
type ExportResult struct {
	Format string            `json:"format"`
	Assets []export.Exported `json:"assets"`
}

// blender finds the Blender to run, as configured or where its installers put it.
func (s *Server) blender() (*export.Blender, error) {
	cfg, err := s.Config("")
	if err != nil {
		return nil, err
	}
	return export.FindBlender(cfg.Blender.Path)
}

func (s *Server) exportFormats(w http.ResponseWriter, _ *http.Request) {
	b, err := s.blender()
	view := ExportFormats{Blender: b}
	if err != nil {
		view.BlenderProblem = err.Error()
	}
	for _, f := range export.Formats() {
		row := ExportFormat{Format: f, Available: !f.Blender || b != nil}
		if !row.Available {
			row.Problem = view.BlenderProblem
		}
		view.Formats = append(view.Formats, row)
	}
	writeJSON(w, 200, view)
}

func (s *Server) exportAssets(w http.ResponseWriter, r *http.Request) {
	var req ExportRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		problem(w, 400, "invalid JSON: "+err.Error())
		return
	}
	f, ok := export.LookupFormat(req.Format)
	if !ok {
		var ids []string
		for _, f := range export.Formats() {
			ids = append(ids, f.ID)
		}
		problem(w, 422, "no export format "+req.Format+": choose "+strings.Join(ids, ", "))
		return
	}
	if len(req.IDs) == 0 {
		problem(w, 422, "name the assets to export")
		return
	}
	// The core's working folder is not the caller's: a relative path would land elsewhere.
	if !filepath.IsAbs(req.Path) {
		problem(w, 422, "path must be a full path to a file or folder, not "+req.Path)
		return
	}
	items := make([]export.Item, len(req.IDs))
	for i, id := range req.IDs {
		a, err := s.DB.Asset(r.Context(), id)
		if errors.Is(err, library.ErrNotFound) {
			problem(w, 404, "no asset "+id)
			return
		}
		if err != nil {
			problem(w, 500, err.Error())
			return
		}
		glb, err := s.Store.Path(a.GLB)
		if err != nil || !s.Store.Has(a.GLB) {
			problem(w, 422, "the GLB of "+a.Name+" is missing from the store")
			return
		}
		items[i] = export.Item{Asset: a.ID, Name: a.Name, GLB: glb}
	}
	var b *export.Blender
	if f.Blender {
		var err error
		if b, err = s.blender(); err != nil {
			problem(w, 422, err.Error())
			return
		}
	}
	done, err := export.ToFiles(r.Context(), b, f, items, req.Path)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, ExportResult{Format: f.ID, Assets: done})
}

// assetBlender opens an asset in the user's Blender, in a new unsaved file.
func (s *Server) assetBlender(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.Asset(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	glb, err := s.Store.Path(a.GLB)
	if err != nil || !s.Store.Has(a.GLB) {
		problem(w, 422, "the GLB of "+a.Name+" is missing from the store")
		return
	}
	b, err := s.blender()
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	if err := b.Open([]string{glb}); err != nil {
		problem(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, b)
}

// assetDelete removes an asset and the job that made it from the library. Files exported
// into projects stay where they are.
func (s *Server) assetDelete(w http.ResponseWriter, r *http.Request) {
	a, err := s.DB.DeleteAsset(r.Context(), r.PathValue("id"))
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.Hub.Publish(EvAssetDeleted, AssetDeleted{ID: a.ID, Job: a.JobID})
	w.WriteHeader(204)
}
