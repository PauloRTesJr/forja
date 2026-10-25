// Package s3 is a small client for S3-compatible object storage: HEAD, GET and PUT of single
// objects with Signature Version 4, path-style. It is what reaches a RunPod network volume
// through RunPod's S3 API, without an SDK.
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// emptyHash is the SHA-256 of an empty body, which HEAD and GET sign.
const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type Client struct {
	Endpoint  string // https://host, no trailing slash
	Region    string
	Bucket    string
	AccessKey string
	Secret    string
	HTTP      *http.Client
	// Now is the signing clock; tests fix it.
	Now func() time.Time
}

// ErrNotFound is a missing object.
var ErrNotFound = errors.New("no such object")

// Error is a refusal from the store.
type Error struct {
	Status int
	Body   string
}

func (e *Error) Error() string {
	if e.Status == http.StatusForbidden || e.Status == http.StatusUnauthorized {
		return "the storage refused the S3 key"
	}
	return fmt.Sprintf("storage answered %d: %s", e.Status, strings.TrimSpace(e.Body))
}

func (c *Client) objectURL(key string) string {
	return c.Endpoint + "/" + escapePath(c.Bucket+"/"+key)
}

// Has reports whether an object exists.
func (c *Client) Has(ctx context.Context, key string) (bool, error) {
	resp, err := c.do(ctx, http.MethodHead, key, nil, 0, emptyHash)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return true, nil
}

// Get opens an object for reading.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, 0, emptyHash)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// PutFile uploads a file as one object, signing its SHA-256 so the store rejects a body that
// changed on the way.
func (c *Client) PutFile(ctx context.Context, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, key, f, size, hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *Client) do(ctx context.Context, method, key string, body io.Reader, size int64, payloadHash string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key), body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	c.sign(req, payloadHash)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the storage: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotFound
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &Error{Status: resp.StatusCode, Body: string(raw)}
	}
	return resp, nil
}

// sign adds Signature Version 4 headers: x-amz-date, x-amz-content-sha256, Authorization.
func (c *Client) sign(req *http.Request, payloadHash string) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	t := now().UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	names := []string{"host"}
	values := map[string]string{"host": req.URL.Host}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "content-length" || lower == "user-agent" {
			continue
		}
		names = append(names, lower)
		values[lower] = strings.TrimSpace(strings.Join(req.Header.Values(name), ","))
	}
	sort.Strings(names)
	var headers strings.Builder
	for _, n := range names {
		headers.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{req.Method, escapePath(req.URL.Path), canonicalQuery(req.URL.Query()),
		headers.String(), signed, payloadHash}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256(canonical)
	key := mac([]byte("AWS4"+c.Secret), day)
	for _, part := range []string{c.Region, "s3", "aws4_request"} {
		key = mac(key, part)
	}
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.AccessKey, scope, signed, hex.EncodeToString(mac(key, toSign))))
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// escapePath encodes each path segment as SigV4 asks: everything but unreserved characters.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = escape(s)
	}
	return strings.Join(parts, "/")
}

func escape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || strings.IndexByte("-_.~", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, escape(k)+"="+escape(v))
		}
	}
	return strings.Join(parts, "&")
}
