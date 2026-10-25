package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"forja/internal/config"
	"forja/internal/jobs"
	"forja/internal/secrets"
)

func row(v PricesView, id string) (PriceRow, bool) {
	for _, p := range v.Prices {
		if p.ID == id {
			return p, true
		}
	}
	return PriceRow{}, false
}

func TestThePriceTableIsEditedFromTheAppAndKeepsComments(t *testing.T) {
	secrets.InMemory()
	fakeFal(t)
	ts, eng, _ := server(t)
	eng.installed["bg"], eng.installed["shape"], eng.installed["paint"] = true, true, true
	if code := call(t, ts, "POST", "/v1/providers", map[string]string{"kind": "api", "vendor": "fal", "token": "fal-test"}, nil); code != 201 {
		t.Fatalf("connect: %d", code)
	}
	call(t, ts, "GET", "/v1/models?all=1&fresh=1", nil, nil)

	var view PricesView
	if code := call(t, ts, "GET", "/v1/prices", nil, &view); code != 200 {
		t.Fatalf("prices: %d", code)
	}
	if p, ok := row(view, "runpod-serverless-4090"); !ok || p.Source != "default" || p.Per != config.PerSecond {
		t.Fatalf("serverless row %+v", p)
	}
	trellis, ok := row(view, "fal-ai/trellis-2")
	if !ok || trellis.Source != "vendor" || trellis.USD != 0.3 || trellis.Provider != "fal" || len(trellis.For) != 1 {
		t.Fatalf("hosted row %+v", trellis)
	}

	raw, _ := os.ReadFile(config.UserFile())
	os.WriteFile(config.UserFile(), append([]byte("# prices I checked myself\n"), raw...), 0o644)
	for _, bad := range []map[string]any{{"usd": -1, "per": "generation"}, {"usd": 1, "per": "minute"}} {
		if code := call(t, ts, "PUT", "/v1/prices/fal-ai/trellis-2", bad, nil); code != 422 {
			t.Fatalf("%v: %d", bad, code)
		}
	}
	if code := call(t, ts, "PUT", "/v1/prices/fal-ai/trellis-2", map[string]any{"usd": 0.5, "per": "generation"}, &view); code != 200 {
		t.Fatalf("set: %d", code)
	}
	if p, _ := row(view, "fal-ai/trellis-2"); p.Source != "user" || p.USD != 0.5 || p.Vendor == nil || p.Vendor.Amount != 0.3 {
		t.Fatalf("after setting %+v", p)
	}
	if raw, _ := os.ReadFile(config.UserFile()); !strings.Contains(string(raw), "# prices I checked myself") {
		t.Fatalf("lost the comment:\n%s", raw)
	}

	// The estimate before queueing uses the price the user set.
	if code := call(t, ts, "PUT", "/v1/models/stages/shape", map[string]string{"model": "fal-ai/trellis-2", "provider": "fal"}, nil); code != 204 {
		t.Fatalf("pin: %d", code)
	}
	var est jobs.Estimate
	if code := call(t, ts, "POST", "/v1/estimate", map[string]any{"preset": "mobile-prop", "images": 2}, &est); code != 200 {
		t.Fatalf("estimate: %d", code)
	}
	if est.CostUSD != 0.5 || est.Images != 2 || !est.Paid {
		t.Fatalf("estimate %+v", est)
	}
	if code := call(t, ts, "POST", "/v1/estimate", map[string]any{"preset": "nope"}, nil); code != 422 {
		t.Fatalf("an unknown preset: %d", code)
	}

	if code := call(t, ts, "DELETE", "/v1/prices/runpod-serverless-4090", nil, nil); code != 409 {
		t.Fatalf("removing a built-in price: %d", code)
	}
	if code := call(t, ts, "DELETE", "/v1/prices/fal-ai/trellis-2", nil, &view); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	if p, _ := row(view, "fal-ai/trellis-2"); p.Source != "vendor" || p.USD != 0.3 {
		t.Fatalf("after removing %+v", p)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(config.UserFile()), "config.toml.bak")); err == nil {
		t.Fatal("the price table rewrote config.toml whole")
	}
}
