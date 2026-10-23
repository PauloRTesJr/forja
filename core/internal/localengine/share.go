package localengine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"forja/internal/secrets"
	"forja/internal/tlscert"
)

// shareSecret holds the token other computers use; unlike the per-session token, it lasts,
// so paired computers keep working across restarts.
const shareSecret = "engine-share-token"

// Share is how the running engine serves other computers; Port is 0 when it does not.
type Share struct {
	Port        int64
	Fingerprint string
	cert, key   string
}

func (m *Manager) prepareShare(port int64) (Share, string, error) {
	token, err := secrets.Get(shareSecret)
	if errors.Is(err, secrets.ErrNotFound) {
		token = randomToken()
		err = secrets.Set(shareSecret, token)
	}
	if err != nil {
		return Share{}, "", fmt.Errorf("the keychain is unavailable: %w", err)
	}
	cert, key, fp, err := tlscert.Ensure(filepath.Join(filepath.Dir(m.storeDir), "tls"))
	if err != nil {
		return Share{}, "", err
	}
	return Share{Port: port, Fingerprint: fp, cert: cert, key: key}, token, nil
}

// Sharing reports whether the running engine serves other computers, and how.
func (m *Manager) Sharing() Share {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running() {
		return Share{}
	}
	return m.share
}

// Pairing gives the engine a code another computer can trade, once, for the share token.
func (m *Manager) Pairing(ctx context.Context, code string, ttl time.Duration) error {
	base, token, err := m.Endpoint(ctx)
	if err != nil {
		return err
	}
	if m.Sharing().Port == 0 {
		return errors.New("this computer is not sharing its GPU")
	}
	body, _ := json.Marshal(map[string]any{"code": code, "expires_in": ttl.Seconds()})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/pairing", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the engine refused the pairing code: %s", resp.Status)
	}
	return nil
}
