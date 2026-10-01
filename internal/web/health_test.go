package web

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestHealthz(t *testing.T) {
	health := func(h http.Handler) (int, map[string]string) {
		rec := do(h, "GET", "/healthz", nil, nil) // no session
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
		}
		return rec.Code, out
	}

	code, out := health(newTestServer(t, fakeOllama(t, 1).URL))
	if code != http.StatusOK || out["status"] != "ok" || out["database"] != "ok" || out["ollama"] != "ok" {
		t.Errorf("healthy: %d %v", code, out)
	}

	code, out = health(newTestServer(t, "http://127.0.0.1:1")) // Ollama down
	if code != http.StatusServiceUnavailable || out["status"] != "unhealthy" || out["database"] != "ok" || out["ollama"] != "unreachable" {
		t.Errorf("Ollama down: %d %v", code, out)
	}

	h := newTestServer(t, fakeOllama(t, 1).URL)
	testStore.Close() // the database gone
	code, out = health(h)
	if code != http.StatusServiceUnavailable || out["database"] != "unreachable" || out["ollama"] != "ok" {
		t.Errorf("database down: %d %v", code, out)
	}
}
