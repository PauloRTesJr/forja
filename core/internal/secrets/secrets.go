// Package secrets stores API keys and tokens in the OS keychain. Config refers to them by name.
package secrets

import (
	"errors"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const service = "forja"

var ErrNotFound = errors.New("secret not found")

// Get checks FORJA_SECRET_<NAME> first (headless servers, CI), then the OS keychain.
func Get(name string) (string, error) {
	if v := os.Getenv(envName(name)); v != "" {
		return v, nil
	}
	v, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func Set(name, value string) error { return keyring.Set(service, name, value) }

func Delete(name string) error { return keyring.Delete(service, name) }

func envName(name string) string {
	return "FORJA_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
}
