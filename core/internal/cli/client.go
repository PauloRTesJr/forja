package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"forja/internal/ipc"
)

// Client talks to a core over the local socket.
type Client struct {
	addr string
	http *http.Client
}

func NewClient(addr string) *Client { return &Client{addr: addr, http: ipc.HTTPClient(addr)} }

func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	body, err := c.Open(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer body.Close()
	if out != nil {
		err = json.NewDecoder(body).Decode(out)
		if errors.Is(err, io.EOF) { // 202 and 204 answers may have no body
			err = nil
		}
	}
	return err
}

// Open sends a request and returns the answer's body, for answers that are not JSON (an
// artifact, the diagnostics report). The caller closes it.
func (c *Client) Open(ctx context.Context, method, path string, in any) (io.ReadCloser, error) {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, ipc.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		e := &APIError{Status: resp.StatusCode, Body: map[string]any{}}
		if json.Unmarshal(raw, &e.Body) == nil {
			e.Detail, _ = e.Body["detail"].(string)
		}
		if e.Detail == "" {
			e.Detail = fmt.Sprintf("%s: %s", resp.Status, raw)
		}
		return nil, e
	}
	return resp.Body, nil
}

// APIError is a problem document from the core; Body holds its extension members.
type APIError struct {
	Status int
	Detail string
	Body   map[string]any
}

func (e *APIError) Error() string { return e.Detail }

type Event struct {
	Seq  uint64          `json:"seq"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Events streams core events until ctx ends.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	conn, _, err := websocket.Dial(ctx, "ws://forja/v1/events", &websocket.DialOptions{HTTPClient: c.http})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(16 << 20)
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		defer conn.CloseNow()
		for {
			var ev Event
			if err := wsjson.Read(ctx, conn, &ev); err != nil {
				return
			}
			ch <- ev
		}
	}()
	return ch, nil
}
