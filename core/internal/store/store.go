// Package store is the content-addressed object store shared with the engine.
//
// Layout: <root>/objects/<2 hex>/<62 hex>, identical to engine/src/forja_engine/store.py,
// so a co-located engine reads and writes the same files with zero copies.
package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zeebo/blake3"
)

const Prefix = "blake3:"

var ErrBadRef = errors.New("not a blake3 ref")

type Store struct {
	Root string
}

func Open(root string) (*Store, error) {
	for _, d := range []string{"objects", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{Root: root}, nil
}

func digest(ref string) (string, error) {
	d, ok := strings.CutPrefix(ref, Prefix)
	if !ok || len(d) != 64 {
		return "", fmt.Errorf("%w: %q", ErrBadRef, ref)
	}
	if _, err := hex.DecodeString(d); err != nil {
		return "", fmt.Errorf("%w: %q", ErrBadRef, ref)
	}
	return d, nil
}

func (s *Store) Path(ref string) (string, error) {
	d, err := digest(ref)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, "objects", d[:2], d[2:]), nil
}

func (s *Store) Has(ref string) bool {
	p, err := s.Path(ref)
	if err != nil {
		return false
	}
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// Put streams r into the store while hashing it, then renames into place.
func (s *Store) Put(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(filepath.Join(s.Root, "tmp"), "put-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	h := blake3.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ref := Prefix + hex.EncodeToString(h.Sum(nil))
	dst, _ := s.Path(ref)
	if s.Has(ref) {
		return ref, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	return ref, os.Rename(tmp.Name(), dst)
}

func (s *Store) PutFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return s.Put(f)
}

func (s *Store) PutBytes(b []byte) (string, error) {
	return s.Put(bytes.NewReader(b))
}

func (s *Store) Open(ref string) (*os.File, error) {
	p, err := s.Path(ref)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// HashFile returns the ref of a file without storing it.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), nil
}
