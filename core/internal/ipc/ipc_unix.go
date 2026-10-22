//go:build !windows

package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// maxSocketPath stays under the smallest sun_path limit (104 bytes on macOS).
const maxSocketPath = 100

// DefaultAddress is the per-user socket path inside the data directory, or a short path in
// the temp dir keyed by the data directory when that would exceed the OS limit.
func DefaultAddress(dataDir string) string {
	p := filepath.Join(dataDir, "forja.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(dataDir))
	return filepath.Join(os.TempDir(), fmt.Sprintf("forja-%d-%s.sock", os.Getuid(), hex.EncodeToString(sum[:6])))
}

func Listen(addr string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(addr), 0o700); err != nil {
		return nil, err
	}
	// A socket file left by a crashed daemon blocks Listen; remove it only if nobody answers.
	if conn, err := net.Dial("unix", addr); err == nil {
		conn.Close()
		return nil, errors.New("another forja core is already listening on " + addr)
	}
	_ = os.Remove(addr)
	old := syscall.Umask(0o077)
	l, err := net.Listen("unix", addr)
	syscall.Umask(old)
	return l, err
}

func Dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", addr)
}
