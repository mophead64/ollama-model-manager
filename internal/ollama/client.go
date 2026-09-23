// Package ollama is a small client for the parts of the Ollama HTTP API
// (https://github.com/ollama/ollama/blob/main/docs/api.md) the app uses.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base string
	http *http.Client
}

// New returns a client for the Ollama server at base, e.g.
// "http://localhost:11434". A bare "host:port" (as OLLAMA_HOST is often set)
// is accepted and treated as http.
func New(base string) *Client {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &Client{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) BaseURL() string { return c.base }

// Details is the summary block Ollama attaches to both list and show results.
type Details struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
	// Only reported by /api/tags on newer Ollama versions; zero otherwise.
	ContextLength   int `json:"context_length"`
	EmbeddingLength int `json:"embedding_length"`
}

// Model is one entry from GET /api/tags.
type Model struct {
	Name         string    `json:"name"`
	Model        string    `json:"model"`
	ModifiedAt   time.Time `json:"modified_at"`
	Size         int64     `json:"size"`
	Digest       string    `json:"digest"`
	Details      Details   `json:"details"`
	Capabilities []string  `json:"capabilities"` // only reported by newer Ollama versions
}

// ModelInfo is the full metadata from POST /api/show. ModelInfo holds the raw
// GGUF key/value metadata, whose keys vary by architecture.
type ModelInfo struct {
	License      string         `json:"license"`
	Modelfile    string         `json:"modelfile"`
	Parameters   string         `json:"parameters"`
	Template     string         `json:"template"`
	System       string         `json:"system"`
	Details      Details        `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
	ModifiedAt   time.Time      `json:"modified_at"`
}

// Version returns the Ollama server's version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/version", nil, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// List returns every model stored locally on the Ollama server.
func (c *Client) List(ctx context.Context) ([]Model, error) {
	var out struct {
		Models []Model `json:"models"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/tags", nil, &out); err != nil {
		return nil, err
	}
	return out.Models, nil
}

// Show returns the full metadata for one local model.
func (c *Client) Show(ctx context.Context, name string) (*ModelInfo, error) {
	var out ModelInfo
	if err := c.do(ctx, http.MethodPost, "/api/show", map[string]string{"model": name}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete removes a local model and the blobs no other model uses.
func (c *Client) Delete(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/delete", map[string]string{"model": name}, nil)
}

// StatusError is returned when Ollama answers with a non-2xx status.
type StatusError struct {
	StatusCode int
	Message    string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ollama returned %d: %s", e.StatusCode, e.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contact ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		// Ollama errors are {"error": "..."}; fall back to the status text.
		var e struct {
			Error string `json:"error"`
		}
		msg := resp.Status
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &StatusError{StatusCode: resp.StatusCode, Message: msg}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
