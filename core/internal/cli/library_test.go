package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"forja/internal/library"
)

// An image named "Baú do Tesouro.png" became the asset "ba-do-tesouro", and "宝箱.png" the
// asset "asset".
func TestAssetsKeepTheirNamesAndAreRenamedSortedAndPaged(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"Baú do Tesouro.png", "宝箱.png", "apple.png"} {
		h.ok("generate", h.image(name), "--preset", "draft")
	}
	var list []*library.Asset
	h.okJSON(&list, "assets", "ls", "--sort", "name")
	if len(list) != 3 || list[0].Name != "apple" || list[1].Name != "Baú do Tesouro" || list[2].Name != "宝箱" {
		t.Fatalf("assets by name: %+v", list)
	}
	var jobs []*library.Job
	h.okJSON(&jobs, "jobs", "ls")
	if jobs[2].Name != "Baú do Tesouro" {
		t.Fatalf("the job is named %q", jobs[2].Name)
	}
	h.okJSON(&list, "assets", "ls", "bau do")
	if len(list) != 0 {
		t.Fatalf("a search without the accent found %d assets (accents are letters)", len(list))
	}
	h.okJSON(&list, "assets", "ls", "baú-do")
	if len(list) != 1 {
		t.Fatalf("searching baú-do found %d assets", len(list))
	}
	apple := list[0]
	h.okJSON(&list, "assets", "ls", "apple")
	apple = list[0]

	if out := h.ok("assets", "rename", apple.ID, "Golden Apple"); !strings.Contains(out, "Golden Apple") {
		t.Fatalf("rename printed %q", out)
	}
	if r := h.cli("", "assets", "rename", apple.ID, "  "); r.code == 0 || !strings.Contains(r.err, "give the asset a name") {
		t.Fatalf("an empty name: exit %d, %s", r.code, r.err)
	}
	out := h.ok("assets", "ls", "--sort", "oldest", "--limit", "2")
	if !strings.Contains(out, "Showing 1 to 2 of 3 assets; --offset 2 shows the next.") || strings.Contains(out, "Golden Apple") {
		t.Fatalf("the first page:\n%s", out)
	}
	if out := h.ok("assets", "ls", "--sort", "oldest", "--limit", "2", "--offset", "2"); !strings.Contains(out, "Golden Apple") ||
		strings.Contains(out, "Showing") {
		t.Fatalf("the last page:\n%s", out)
	}
	if r := h.cli("", "assets", "ls", "--sort", "size"); r.code == 0 {
		t.Fatal("an unknown sort was accepted")
	}

	// Exported files keep the letters; a second export beside the first does not replace it.
	dest := filepath.Join(h.dir, "exports")
	want := filepath.Join(dest, "Golden Apple.glb")
	if out := h.ok("assets", "export", apple.ID, "-o", dest); strings.TrimSpace(out) != want {
		t.Fatalf("export printed %q, want %q", out, want)
	}
	if out := h.ok("assets", "export", apple.ID, "-o", dest); strings.TrimSpace(out) != filepath.Join(dest, "Golden Apple (2).glb") {
		t.Fatalf("the second export printed %q", out)
	}
	if out := h.ok("assets", "export", apple.ID, "-o", dest, "--replace"); strings.TrimSpace(out) != want {
		t.Fatalf("export --replace printed %q", out)
	}
}
