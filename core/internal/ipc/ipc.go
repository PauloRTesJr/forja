// Package ipc is the UI/CLI-to-core transport: a Unix socket on macOS and Linux, a named
// pipe on Windows. No TCP port is ever opened; OS permissions restrict it to the user.
package ipc

import (
	"context"
	"net"
	"net/http"
	"time"
)

// HTTPClient returns a client whose every request goes to the core at addr.
func HTTPClient(addr string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:  func(ctx context.Context, _, _ string) (net.Conn, error) { return Dial(ctx, addr) },
		MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
	}}
}

// BaseURL is the placeholder host for requests sent over the socket.
const BaseURL = "http://forja"
