package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"forja/internal/installs"
)

// runTask starts an install or removal through the core, asking about the model's license
// when it needs accepting, and prints its progress until it ends.
func (a *app) runTask(model, method, path string, body map[string]any, accept bool) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	evs, err := c.Events(ctx) // before starting, so no update is missed
	if err != nil {
		return err
	}
	var in any
	if body != nil {
		body["accept_license"] = accept
		in = body
	}
	var entry installs.Entry
	err = c.Do(ctx, method, path, in, &entry)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Body["license"] != nil && body != nil {
		if !confirmLicense(model, apiErr) {
			return errors.New("the license was not accepted; nothing was installed")
		}
		body["accept_license"] = true
		err = c.Do(ctx, method, path, body, &entry)
	}
	if err != nil {
		return err
	}
	return a.follow(ctx, c, evs, entry)
}

func confirmLicense(model string, prob *APIError) bool {
	lic, _ := prob.Body["license"].(map[string]any)
	fmt.Println(prob.Detail + ".")
	if rs, _ := lic["restrictions"].([]any); len(rs) > 0 {
		for _, r := range rs {
			fmt.Printf("  - %v\n", r)
		}
	}
	if u, _ := lic["url"].(string); u != "" {
		fmt.Println("  License text: " + u)
	}
	if st, err := os.Stdin.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		fmt.Println("Re-run with --accept-license to accept it.")
		return false
	}
	fmt.Printf("Accept the license of %s? [y/N] ", model)
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y") || strings.EqualFold(strings.TrimSpace(answer), "yes")
}

// follow prints a task's steps, new log lines and download progress until it ends; Ctrl-C
// cancels it.
func (a *app) follow(ctx context.Context, c *Client, evs <-chan Event, e installs.Entry) error {
	steps := map[string]string{}
	var lastLog []string
	tenth := int64(-1)
	show := func(e installs.Entry) {
		if a.json {
			return
		}
		for _, s := range e.Steps {
			if steps[s.ID] == s.Status {
				continue
			}
			steps[s.ID] = s.Status
			switch s.Status {
			case "running":
				fmt.Printf("→ %s\n", s.Label)
			case "done":
				fmt.Printf("✓ %s\n", s.Label)
			case "failed":
				fmt.Printf("✗ %s\n", s.Label)
			}
		}
		for _, line := range newLines(lastLog, e.Log) {
			fmt.Println("    " + line)
		}
		lastLog = e.Log
		if p := e.Progress; p != nil && p.Total > 0 && p.Done*10/p.Total != tenth {
			tenth = p.Done * 10 / p.Total
			fmt.Printf("    %.1f of %.1f GB\n", float64(p.Done)/(1<<30), float64(p.Total)/(1<<30))
		}
	}
	show(e)
	key := e.Provider + "/" + e.ID
	for !e.Task.Done() {
		select {
		case <-ctx.Done():
			cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.Do(cctx, "DELETE", "/v1/installs/"+e.Provider+"/"+e.ID, nil, nil)
			cancel()
			return &exitError{exitCancelled, "cancelled"}
		case ev, ok := <-evs:
			if !ok {
				return errors.New("lost the connection to the core")
			}
			if ev.Type != installs.EvUpdated {
				continue
			}
			var next installs.Entry
			if json.Unmarshal(ev.Data, &next) != nil || next.Provider+"/"+next.ID != key || next.Version < e.Version {
				continue
			}
			e = next
			show(e)
		}
	}
	if a.json {
		a.print(e, func() {})
	}
	switch e.Status {
	case "failed":
		msg := "failed"
		if e.Error != nil {
			msg = *e.Error
		}
		return fmt.Errorf("%s %s: %s", e.Kind, e.Target, msg)
	case "cancelled":
		return &exitError{exitCancelled, "cancelled"}
	}
	return nil
}

// newLines returns the lines of a log tail that came after the previous tail.
func newLines(prev, next []string) []string {
	if len(prev) == 0 {
		return next
	}
	last := prev[len(prev)-1]
	for i := len(next) - 1; i >= 0; i-- {
		if next[i] == last && slices.Equal(next[max(0, i+1-len(prev)):i+1], prev[max(0, len(prev)-(i+1)):]) {
			return next[i+1:]
		}
	}
	return next
}
