package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, ipc.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		var prob struct {
			Detail string `json:"detail"`
		}
		raw, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(raw, &prob) == nil && prob.Detail != "" {
			return fmt.Errorf("%s", prob.Detail)
		}
		return fmt.Errorf("%s: %s", resp.Status, raw)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

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
