package plain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommonFileAndConnectionFailuresReadAsSentences(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "config.toml")
	_, notFound := os.ReadFile(missing)
	denied := &fs.PathError{Op: "open", Path: missing, Err: fs.ErrPermission}
	refused := fmt.Errorf("Post %q: %w", "http://192.0.2.10:7861/v1", &net.OpError{Op: "dial", Net: "tcp",
		Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 7861}, Err: errors.New("connectex: refused")})
	for _, c := range []struct {
		err  error
		want string
		not  string
	}{
		{notFound, "config.toml was not found. Check the path.", "CreateFile"},
		{fmt.Errorf("saving: %w", denied), "Forja is not allowed to use " + missing, "permission denied"},
		{refused, "Could not connect: nothing answers at 192.0.2.10:7861.", "connectex"},
		{&net.DNSError{Name: "studio.invalid", Err: "no such host"}, "the address studio.invalid was not found", "no such host"},
		{context.DeadlineExceeded, "it did not answer in time", "deadline"},
		{errors.New("pick a preset"), "pick a preset", ""},
	} {
		got := Err(c.err)
		if !strings.Contains(got, c.want) || c.not != "" && strings.Contains(got, c.not) {
			t.Errorf("%v\n got %q\nwant %q without %q", c.err, got, c.want, c.not)
		}
	}
	if got := Provider("Studio PC", refused); got != "Studio PC could not be reached: nothing answers at 192.0.2.10:7861. Check that it is running and connected." {
		t.Errorf("provider: %q", got)
	}
}
