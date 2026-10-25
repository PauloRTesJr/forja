package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func exampleClient() *Client {
	return &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE",
		Secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		Now:    func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
}

// The examples in AWS's Signature Version 4 documentation for S3 ("Examples: Signature
// Calculations"), signed the same way.
func TestSignsAWSsDocumentedExamples(t *testing.T) {
	get, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	get.Header.Set("Range", "bytes=0-9")
	exampleClient().sign(get, emptyHash)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := get.Header.Get("Authorization"); got != want {
		t.Fatalf("GET signed\n%s\nwant\n%s", got, want)
	}

	put, _ := http.NewRequest("PUT", "https://examplebucket.s3.amazonaws.com/test$file.text", strings.NewReader("Welcome to Amazon S3."))
	put.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	put.Header.Set("x-amz-storage-class", "REDUCED_REDUNDANCY")
	exampleClient().sign(put, "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072")
	want = "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, " +
		"Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"
	if got := put.Header.Get("Authorization"); got != want {
		t.Fatalf("PUT signed\n%s\nwant\n%s", got, want)
	}
}

func TestObjectsGoUpAndComeBackPathStyle(t *testing.T) {
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=user_fake/") {
			w.WriteHeader(403)
			return
		}
		switch r.Method {
		case "PUT":
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case "HEAD", "GET":
			body, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write(body)
		}
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL, Region: "EU-RO-1", Bucket: "vol123", AccessKey: "user_fake", Secret: "rps_fake"}
	ctx := context.Background()
	if ok, err := c.Has(ctx, "forja/a/b"); ok || err != nil {
		t.Fatalf("has before put: %v %v", ok, err)
	}
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, []byte("bytes"), 0o644)
	if err := c.PutFile(ctx, "forja/a/b", file); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects["/vol123/forja/a/b"]; !ok {
		t.Fatalf("stored as %v", objects)
	}
	rc, err := c.Get(ctx, "forja/a/b")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "bytes" {
		t.Fatalf("got %q", got)
	}
	c.AccessKey = "someone_else"
	if _, err := c.Has(ctx, "forja/a/b"); err == nil || !strings.Contains(err.Error(), "refused the S3 key") {
		t.Fatalf("a wrong key: %v", err)
	}
}
