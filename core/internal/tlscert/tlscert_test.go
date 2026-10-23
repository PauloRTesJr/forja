package tlscert

import (
	"crypto/tls"
	"testing"
)

func TestTheCertificateIsMadeOnceAndLoads(t *testing.T) {
	dir := t.TempDir()
	cert, key, fp, err := Ensure(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		t.Fatal(err)
	}
	_, _, again, err := Ensure(dir)
	if err != nil || again != fp {
		t.Fatalf("a second call made a new certificate: %s vs %s (%v)", again, fp, err)
	}
	if len(fp) != 64 || len(Short(fp)) != 19 {
		t.Fatalf("fingerprint %q, short %q", fp, Short(fp))
	}
}
