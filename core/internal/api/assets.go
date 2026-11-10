package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"forja/internal/library"
)

// EvAssetUpdated tells clients an asset changed in place (its name, its thumbnail); the data
// is the asset.
const EvAssetUpdated = "asset.updated"

// TotalHeader carries how many assets match a list's search in all, whatever the page.
const TotalHeader = "X-Total-Count"

// fold makes names comparable as people read them: case does not matter, and a space, a
// hyphen and an underscore are the same separator.
func fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(s) {
		if r == '-' || r == '_' || unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// assetMatch is the search of the library: the words typed, found in the asset's name, its
// preset's id or the name its preset shows.
func (s *Server) assetMatch(q string) func(*library.Asset) bool {
	want := fold(q)
	if strings.TrimSpace(q) == "" {
		return nil
	}
	if want == "" {
		// Only separators were typed ("_", "-"): the names that have one.
		return func(a *library.Asset) bool { return strings.Contains(fold(a.Name), " ") }
	}
	shown := map[string]string{}
	if all, err := s.Runner.Presets(""); err == nil {
		for id, p := range all {
			shown[id] = fold(p.Name)
		}
	}
	return func(a *library.Asset) bool {
		return strings.Contains(fold(a.Name), want) || strings.Contains(fold(a.Preset), want) ||
			strings.Contains(shown[a.Preset], want)
	}
}

func (s *Server) assetList(w http.ResponseWriter, r *http.Request) {
	limit, before := page(r)
	query := r.URL.Query()
	offset, _ := strconv.Atoi(query.Get("offset"))
	list, total, err := s.DB.FindAssets(r.Context(), library.AssetQuery{
		Limit: limit, Offset: max(0, offset), Before: before, Sort: query.Get("sort"), Match: s.assetMatch(query.Get("q")),
	})
	if err != nil {
		// The only thing a caller can get wrong is the sort.
		problem(w, 422, err.Error())
		return
	}
	if list == nil {
		list = []*library.Asset{}
	}
	for _, a := range list {
		s.withStats(r.Context(), a)
	}
	w.Header().Set(TotalHeader, strconv.Itoa(total))
	w.Header().Set("Access-Control-Expose-Headers", TotalHeader)
	writeJSON(w, 200, list)
}

// assetRename gives an asset another name. Files exported from it keep theirs.
func (s *Server) assetRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name *string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Name == nil {
		problem(w, 400, `want {"name": "<the new name>"}`)
		return
	}
	name, err := library.CheckName(*body.Name)
	if err != nil {
		problem(w, 422, err.Error())
		return
	}
	a, err := s.DB.RenameAsset(r.Context(), r.PathValue("id"), name)
	if errors.Is(err, library.ErrNotFound) {
		problem(w, 404, "no asset "+r.PathValue("id"))
		return
	}
	if err != nil {
		problem(w, 500, err.Error())
		return
	}
	s.withStats(r.Context(), a)
	// The copy made for dragging out carries the old name.
	dropDrag(a.ID)
	s.Hub.Publish(EvAssetUpdated, a)
	writeJSON(w, 200, a)
}

// dropDrag removes the copy of an asset made for dragging it out of the app.
func dropDrag(id string) {
	if id != "" && id == filepath.Base(id) {
		_ = os.RemoveAll(filepath.Join(dragDir(), id))
	}
}
