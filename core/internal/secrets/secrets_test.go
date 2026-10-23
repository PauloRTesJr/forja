package secrets

import (
	"bytes"
	"errors"
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
