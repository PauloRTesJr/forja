// Package tlscert makes the self-signed certificate a shared engine serves other computers
// with. Clients pin its fingerprint when they pair, so no certificate authority is involved
// and a changing LAN address does not break the connection.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Ensure returns the certificate and key files in dir, creating them the first time, and the
// certificate's fingerprint.
func Ensure(dir string) (certPath, keyPath, fingerprint string, err error) {
	certPath, keyPath = filepath.Join(dir, "engine.crt"), filepath.Join(dir, "engine.key")
	if raw, err := os.ReadFile(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			if fp, err := FingerprintPEM(raw); err == nil {
				return certPath, keyPath, fp, nil
			}
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Forja engine on " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", host},
		IPAddresses:  append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, Addresses()...),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", "", err
	}
	return certPath, keyPath, Fingerprint(der), nil
}

// Fingerprint is the SHA-256 of a certificate's DER, in lowercase hex.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func FingerprintPEM(raw []byte) (string, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("not a PEM certificate")
	}
	return Fingerprint(block.Bytes), nil
}

// Short groups the start of a fingerprint for people to compare by eye: "3f2a 9c1e 5b7d 4e8a".
func Short(fp string) string {
	if len(fp) < 16 {
		return fp
	}
	parts := []string{fp[0:4], fp[4:8], fp[8:12], fp[12:16]}
	return strings.ToUpper(strings.Join(parts, " "))
}

// Addresses lists this machine's LAN addresses, private IPv4 first.
func Addresses() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4, v6 []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if ip4 := ipn.IP.To4(); ip4 != nil {
				v4 = append(v4, ip4)
			} else {
				v6 = append(v6, ipn.IP)
			}
		}
	}
	return append(v4, v6...)
}
