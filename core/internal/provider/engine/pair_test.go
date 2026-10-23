package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"forja/internal/engineapi"
	"forja/internal/tlscert"
)

// shared fakes an engine shared on the network: self-signed TLS, a pairing code, a token.
func shared(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/pair":
			var body struct{ Code string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Code != "482913" {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"detail":"wrong pairing code"}`))
				return
			}
			_, _ = w.Write([]byte(`{"token":"tok-0123456789","name":"studio"}`))
		case "/v1/info":
			if r.Header.Get("Authorization") != "Bearer tok-0123456789" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(engineapi.Info{APIVersion: engineapi.APIVersion, Version: "test"})
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestPairingPinsTheCertificateItSaw(t *testing.T) {
	ts := shared(t)
	ctx := context.Background()
	if _, err := Pair(ctx, ts.URL, "000000"); err == nil || !strings.Contains(err.Error(), "wrong pairing code") {
		t.Fatalf("wrong code: %v", err)
	}
	paired, err := Pair(ctx, ts.URL, "482 913")
	if err != nil {
		t.Fatal(err)
	}
	want := tlscert.Fingerprint(ts.Certificate().Raw)
	if paired.Token != "tok-0123456789" || paired.Fingerprint != want || paired.Name != "studio" {
		t.Fatalf("got %+v", paired)
	}
	if _, err := Test(ctx, ts.URL, paired.Token, want); err != nil {
		t.Fatalf("pinned: %v", err)
	}
	if _, err := Test(ctx, ts.URL, paired.Token, strings.Repeat("0", 64)); err == nil {
		t.Fatal("a different certificate was accepted")
	}
	if _, err := Test(ctx, ts.URL, "nope", want); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Fatalf("bad token: %v", err)
	}
	// Without a pin, a self-signed engine is reached and its fingerprint returned to pin.
	probe, err := Test(ctx, ts.URL, paired.Token, "")
	if err != nil || probe.Fingerprint != want {
		t.Fatalf("probe %+v, %v", probe, err)
	}
}

func TestAddressesPeopleTypeBecomeEngineURLs(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.20":          "https://192.168.1.20:7861",
		"studio.local:9000":     "https://studio.local:9000",
		"https://box:7861/":     "https://box:7861",
		"http://127.0.0.1:7860": "http://127.0.0.1:7860",
	} {
		if got, err := NormalizeURL(in); err != nil || got != want {
			t.Errorf("%s: got %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "http://192.168.1.20", "ftp://x"} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
