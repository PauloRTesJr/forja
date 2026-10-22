//go:build windows

package ipc

import (
	"context"
	"net"
	"os"
	"strings"

	"github.com/Microsoft/go-winio"
)

// DefaultAddress is a per-user named pipe.
func DefaultAddress(string) string {
	user := strings.NewReplacer(`\`, "-", " ", "-").Replace(os.Getenv("USERNAME"))
	return `\\.\pipe\forja-` + user
}

func Listen(addr string) (net.Listener, error) {
	// Owner and SYSTEM only.
	return winio.ListenPipe(addr, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;OW)(A;;GA;;;SY)"})
}

func Dial(ctx context.Context, addr string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, addr)
}
