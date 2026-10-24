package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPutIsContentAddressedAndIdempotent(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Put(strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Put(strings.NewReader("hello"))
	if a != b || !strings.HasPrefix(a, Prefix) || !s.Has(a) {
		t.Fatalf("refs differ or missing: %s %s", a, b)
	}
	p, _ := s.Path(a)
	if rel, _ := filepath.Rel(s.Root, p); len(strings.Split(rel, string(filepath.Separator))) != 3 {
		t.Fatalf("unexpected layout %s", rel)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "hello" {
		t.Fatal("content mismatch")
	}
}

func TestMatchesEngineHash(t *testing.T) {
	// blake3("hello"), the same value Python's blake3 package produces.
	const want = "blake3:ea8f163db38682925e4491c5e58d4bb3506ef8c14eb78a86e908c5624a67200f"
	s, _ := Open(t.TempDir())
	if got, _ := s.Put(strings.NewReader("hello")); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestRejectsBadRefs(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, ref := range []string{"", "sha256:00", "blake3:zz", "blake3:" + strings.Repeat("g", 64)} {
		if _, err := s.Path(ref); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestStoringAgainMarksAnObjectUsedAndWalkListsObjectsOnly(t *testing.T) {
	s, _ := Open(t.TempDir())
	ref, _ := s.Put(strings.NewReader("hello"))
	p, _ := s.Path(ref)
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put(strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); time.Since(st.ModTime()) > time.Minute {
		t.Fatalf("storing it again left it used at %s", st.ModTime())
	}
	other, _ := s.PutBytes([]byte("world"))
	_ = os.WriteFile(filepath.Join(s.Root, "objects", "zz-not-an-object"), []byte("x"), 0o644)
	var seen []Object
	if err := s.Walk(func(o Object) error { seen = append(seen, o); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Size != 5 || (seen[0].Ref != ref && seen[0].Ref != other) {
		t.Fatalf("walked %+v", seen)
	}
	if err := s.Remove(ref); err != nil || s.Has(ref) {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Remove(ref); err != nil {
		t.Fatalf("removing it twice: %v", err)
	}
}
