package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// PullProgress is one line of the progress stream from POST /api/pull.
// Digest/Total/Completed are set while a layer is downloading.
type PullProgress struct {
	Status    string `json:"status"`
	Digest    string `json:"digest"`
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error"`
}

// PullError is a failure Ollama reported mid-stream (the HTTP status is still
// 200 by then), e.g. "pull model manifest: file does not exist".
type PullError struct{ Message string }

func (e *PullError) Error() string { return e.Message }

// Pull downloads a model, calling fn for each progress update, and returns
// once Ollama reports success or an error. Cancel ctx to abort; Ollama keeps
// the layers downloaded so far, so pulling again resumes.
func (c *Client) Pull(ctx context.Context, name string, fn func(PullProgress)) error {
	body, _ := json.Marshal(map[string]any{"model": name, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// No overall timeout: a big model on a slow line can take hours. The
	// caller watches for a stalled stream instead.
	resp, err := c.stream.Do(req)
	if err != nil {
		return fmt.Errorf("contact ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := resp.Status
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &StatusError{StatusCode: resp.StatusCode, Message: msg}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p PullProgress
		if err := json.Unmarshal(line, &p); err != nil {
			return fmt.Errorf("unexpected response from ollama: %q", line)
		}
		if p.Error != "" {
			return &PullError{Message: p.Error}
		}
		fn(p)
		if p.Status == "success" {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("ollama closed the connection before the pull finished")
}
