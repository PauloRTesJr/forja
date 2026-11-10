package secrets

import (
	"bytes"
	"errors"
	"github.com/zalando/go-keyring"
	"log/slog"
	"strings"
	"testing"
)

func TestLogsNeverCarryASecretValue(t *testing.T) {
	t.Setenv("FORJA_SECRET_PROVIDER_STUDIO", "sk-test-0123456789abcdef")
	if v, err := Get("provider-studio"); err != nil || v != "sk-test-0123456789abcdef" {
		t.Fatalf("got %q %v", v, err)
	}
	var buf bytes.Buffer
	log := slog.New(Redacting(slog.NewTextHandler(&buf, nil)))
	log.With("header", "Bearer sk-test-0123456789abcdef").Warn("calling with sk-test-0123456789abcdef",
		"err", errors.New("401 for token sk-test-0123456789abcdef"), slog.Group("req", "auth", "sk-test-0123456789abcdef"))
	if strings.Contains(buf.String(), "0123456789abcdef") || strings.Count(buf.String(), "[redacted]") != 4 {
		t.Fatalf("leaked: %s", buf.String())
	}
}

func TestHintsShowOnlyTheEnd(t *testing.T) {
	t.Setenv("FORJA_SECRET_HF", "hf_abcdefghijklmnop")
	if h, ok := Hint("hf"); !ok || h != "…mnop" {
		t.Fatalf("got %q", h)
	}
	if !Name.MatchString("provider-studio") || Name.MatchString("Bad Name") || Name.MatchString("../x") {
		t.Fatal("name rule")
	}
}

// Two data folders on one account shared the keychain entry "forja" and overwrote each other's keys.
func TestAMovedConfigFolderHasItsOwnKeychainGroup(t *testing.T) {
	InMemory()
	t.Setenv("FORJA_CONFIG_DIR", "")
	if got := service(); got != "forja" {
		t.Fatalf("the default folder must keep %q, got %q", "forja", got)
	}
	a, b := t.TempDir(), t.TempDir()
	t.Setenv("FORJA_CONFIG_DIR", a)
	svcA := service()
	t.Setenv("FORJA_CONFIG_DIR", b)
	if svcB := service(); svcA == "forja" || svcB == "forja" || svcA == svcB {
		t.Fatalf("groups %q and %q", svcA, svcB)
	}
	if err := Set("provider-x", "key-in-b-123456"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORJA_CONFIG_DIR", a)
	if _, err := Get("provider-x"); err == nil {
		t.Fatal("a key written for one folder is readable from another")
	}
}

// An installed app that runs with FORJA_CONFIG_DIR saved its keys under the legacy group before
// groups existed; they must keep working, and a dev build must never overwrite or remove them.
func TestKeysOfTheLegacyGroupCarryOverToAMovedFolderWithoutTouchingTheLegacyOnes(t *testing.T) {
	InMemory()
	t.Setenv("FORJA_CONFIG_DIR", "")
	if err := Set("hub-fal", "legacy-key-123456"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORJA_CONFIG_DIR", t.TempDir())
	own := service()
	if own == baseService {
		t.Fatal("not a group of its own")
	}
	if v, err := Get("hub-fal"); err != nil || v != "legacy-key-123456" {
		t.Fatalf("legacy key from a moved folder: %q %v", v, err)
	}
	if v, err := keyring.Get(own, "hub-fal"); err != nil || v != "legacy-key-123456" {
		t.Fatalf("not copied into %s: %q %v", own, v, err)
	}
	// A write here replaces only this folder's copy.
	if err := Set("hub-fal", "dev-key-7890123"); err != nil {
		t.Fatal(err)
	}
	if v, _ := keyring.Get(baseService, "hub-fal"); v != "legacy-key-123456" {
		t.Fatalf("the legacy key was overwritten: %q", v)
	}
	if v, _ := Get("hub-fal"); v != "dev-key-7890123" {
		t.Fatalf("got %q", v)
	}
	// A delete here is not undone by the fallback, and leaves the legacy key alone.
	if err := Delete("hub-fal"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("hub-fal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted key came back: %v", err)
	}
	if v, _ := keyring.Get(baseService, "hub-fal"); v != "legacy-key-123456" {
		t.Fatalf("the legacy key was removed: %q", v)
	}
	// Saving again after a delete works.
	if err := Set("hub-fal", "again-key-1234567"); err != nil {
		t.Fatal(err)
	}
	if v, err := Get("hub-fal"); err != nil || v != "again-key-1234567" {
		t.Fatalf("%q %v", v, err)
	}
	// Deleting what only the legacy group has does not fetch it either.
	t.Setenv("FORJA_CONFIG_DIR", "")
	Set("pair-x", "legacy-pair-123456")
	t.Setenv("FORJA_CONFIG_DIR", t.TempDir())
	Delete("pair-x")
	if _, err := Get("pair-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
	// The default folder is as before: its group is the legacy one, with no tombstones.
	t.Setenv("FORJA_CONFIG_DIR", "")
	if v, err := Get("pair-x"); err != nil || v != "legacy-pair-123456" {
		t.Fatalf("default folder: %q %v", v, err)
	}
	Delete("pair-x")
	if _, err := keyring.Get(baseService, tombstone("pair-x")); err == nil {
		t.Fatal("the default folder wrote a tombstone")
	}
}
