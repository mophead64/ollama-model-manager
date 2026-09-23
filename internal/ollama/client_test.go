package ollama

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewNormalisesBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:11434":  "http://localhost:11434",
		"http://localhost:11434/": "http://localhost:11434",
		"ollama:11434":            "http://ollama:11434",
		" https://x.example/ ":    "https://x.example",
	}
	for in, want := range cases {
		if got := New(in).BaseURL(); got != want {
			t.Errorf("New(%q).BaseURL() = %q, want %q", in, got, want)
		}
	}
}

func TestListAndShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"llama3.2:latest","size":2019393189,"details":{"family":"llama","parameter_size":"3.2B","context_length":131072},"capabilities":["completion","tools"]}]}`))
		case "/api/show":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"model 'nope' not found"}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL)

	models, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "llama3.2:latest" || models[0].Details.ContextLength != 131072 || len(models[0].Capabilities) != 2 {
		t.Fatalf("unexpected list result: %+v", models)
	}

	_, err = c.Show(context.Background(), "nope")
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound || se.Message != "model 'nope' not found" {
		t.Fatalf("Show error = %v, want 404 StatusError with Ollama's message", err)
	}
}
