package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestThePriceTableHasRunPodDefaultsAndRejectsOddUnits(t *testing.T) {
	isolate(t)
	l, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := l.Prices["runpod-serverless-4090"]; p.USD != 0.00031 || p.Per != PerSecond {
		t.Fatalf("serverless default %+v", p)
	}
	if p := l.Prices["runpod-pod-4090"]; p.Per != PerHour || math.Abs(p.PerSecondUSD()-0.34/3600) > 1e-12 {
		t.Fatalf("pod default %+v", p)
	}
	if l.Sources["prices.runpod-pod-4090.usd"] != "default" {
		t.Fatalf("source %q", l.Sources["prices.runpod-pod-4090.usd"])
	}
	if _, err := Load("", map[string]any{"prices": map[string]any{"x": map[string]any{"usd": 1.0, "per": "minute"}}}); err == nil ||
		!strings.Contains(err.Error(), "prices.x.per") {
		t.Fatalf("a price per minute: %v", err)
	}
}

func TestAProviderIsPricedByItsOwnRateItsEntryOrItsVendor(t *testing.T) {
	isolate(t)
	l, err := Load("", map[string]any{"providers": []map[string]any{
		{"id": "rp", "kind": "serverless", "vendor": "runpod"},
		{"id": "pod", "kind": "engine", "vendor": "runpod", "price": "runpod-pod-4090"},
		{"id": "own", "kind": "serverless", "vendor": "runpod", "price_usd_per_second": 0.001},
		{"id": "lan", "kind": "engine"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]struct {
		rate  float64
		entry string
	}{
		"rp":  {0.00031, "runpod-serverless-4090"},
		"pod": {0.34 / 3600, "runpod-pod-4090"},
		"own": {0.001, ""},
		"lan": {0, ""},
	} {
		if rate, entry := l.Rate(id); math.Abs(rate-want.rate) > 1e-12 || entry != want.entry {
			t.Errorf("%s: %v %q, want %v %q", id, rate, entry, want.rate, want.entry)
		}
	}
}

func TestAPriceForAHostedModelIsWrittenUnderItsQuotedID(t *testing.T) {
	dir := isolate(t)
	path := filepath.Join(dir, "user", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("# my prices\n[prices.runpod-serverless-4090]\nusd = 0.0004 # checked in May\n"), 0o644)
	id := "fal-ai/hunyuan-3d/v3.1/pro/image-to-3d"
	err := Edit(path, map[string]any{Key("prices", id, "usd"): 0.375, Key("prices", id, "per"): PerGeneration}, func() error {
		_, err := Load("", nil)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Fatalf("the file was rewritten whole:\n%s", got)
	}
	if !strings.Contains(got, "# checked in May") || !strings.Contains(got, `[prices."`+id+`"]`) {
		t.Fatalf("got:\n%s", got)
	}
	l, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p := l.Prices[id]; p.USD != 0.375 || p.Per != PerGeneration || l.Prices["runpod-serverless-4090"].USD != 0.0004 {
		t.Fatalf("prices %+v", l.Prices)
	}
	if err := Edit(path, map[string]any{Key("prices", id, "usd"): nil, Key("prices", id, "per"): nil}, nil); err != nil {
		t.Fatal(err)
	}
	if l, _ := Load("", nil); len(l.Prices) != 2 {
		t.Fatalf("after removing it: %+v", l.Prices)
	}
}
