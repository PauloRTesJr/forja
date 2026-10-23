package engine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"forja/internal/engineapi"
	"forja/internal/tlscert"
)

// DefaultSharePort is where a shared engine listens unless its owner changed it.
const DefaultSharePort = "7861"

// Pin makes the provider trust exactly one certificate, by SHA-256 fingerprint: an engine
// that signs its own, as shared engines do.
func (p *Provider) Pin(fingerprint string) {
	if tr, ok := p.http.Transport.(*http.Transport); ok && fingerprint != "" {
		tr.TLSClientConfig = pinned(fingerprint)
	}
}

func pinned(fp string) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// No authority signed the engine's certificate; the fingerprint check replaces it.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || tlscert.Fingerprint(raw[0]) != fp {
				return errors.New("the engine's certificate is not the one paired with; pair again")
			}
			return nil
		},
	}
}

// NormalizeURL turns what a person types ("192.168.1.20", "studio:7861") into an engine URL.
// Plain HTTP is accepted only for this machine: engines on the network serve TLS.
func NormalizeURL(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("enter the other computer's address")
	}
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q is not an address like 192.168.1.20 or studio.local:7861", addr)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), DefaultSharePort)
	}
	if u.Scheme == "http" && !loopback(u.Hostname()) {
		return "", errors.New("engines on the network are reached over https")
	}
	return u.Scheme + "://" + u.Host, nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Probe is what a test connection to an engine found.
type Probe struct {
	Info *engineapi.Info `json:"info"`
	// Fingerprint is set when the engine's certificate is self-signed and must be pinned.
	Fingerprint string        `json:"fingerprint,omitempty"`
	Latency     time.Duration `json:"latency"`
}

// client trusts a pinned certificate, else the system's authorities; with neither it records
// the certificate it sees, for the caller to show and pin.
func client(fingerprint string, seen *string) *http.Client {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	switch {
	case fingerprint != "":
		cfg = pinned(fingerprint)
	case seen != nil:
		cfg.InsecureSkipVerify = true // the certificate is recorded below and pinned by the caller
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) > 0 {
				*seen = tlscert.Fingerprint(cs.PeerCertificates[0].Raw)
			}
			return nil
		}
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
}

// Test connects to an engine with a token and asks what it is, before anything is saved.
func Test(ctx context.Context, baseURL, token, fingerprint string) (*Probe, error) {
	out := &Probe{Fingerprint: fingerprint}
	start := time.Now()
	info, err := getInfo(ctx, client(fingerprint, nil), baseURL, token)
	if err != nil && fingerprint == "" && strings.HasPrefix(baseURL, "https://") && isUnknownAuthority(err) {
		start = time.Now()
		info, err = getInfo(ctx, client("", &out.Fingerprint), baseURL, token)
	}
	if err != nil {
		return nil, err
	}
	out.Info, out.Latency = info, time.Since(start)
	return out, engineapi.CheckCompatible(info.APIVersion)
}

func isUnknownAuthority(err error) bool {
	var ua x509.UnknownAuthorityError
	var cv *tls.CertificateVerificationError
	return errors.As(err, &ua) || errors.As(err, &cv)
}

func getInfo(ctx context.Context, c *http.Client, baseURL, token string) (*engineapi.Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/info", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errors.New("the engine refused the token")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("the engine answered %s", resp.Status)
	}
	var info engineapi.Info
	return &info, json.NewDecoder(resp.Body).Decode(&info)
}

// Paired is what an engine hands over for a correct pairing code.
type Paired struct {
	Token       string `json:"-"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
}

// Pair trades a code shown on the other computer for its engine's token, recording the
// certificate it served so later connections pin it.
func Pair(ctx context.Context, baseURL, code string) (*Paired, error) {
	var seen string
	body, _ := json.Marshal(map[string]string{"code": strings.ReplaceAll(strings.TrimSpace(code), " ", "")})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/pair", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client("", &seen).Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 400 {
		var prob struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &prob) == nil && prob.Detail != "" {
			return nil, errors.New(prob.Detail)
		}
		return nil, fmt.Errorf("pairing failed: %s", resp.Status)
	}
	var out struct {
		Token string `json:"token"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Token == "" {
		return nil, errors.New("the engine did not hand over a token")
	}
	return &Paired{Token: out.Token, Name: out.Name, Fingerprint: seen}, nil
}
