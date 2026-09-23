package web

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// fakeOllama serves n models named model-000:latest... plus one namespaced
// model, and a /api/show that knows only "user/custom:v1".
func fakeOllama(t *testing.T, n int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			var parts []string
			for i := 0; i < n; i++ {
				parts = append(parts, fmt.Sprintf(`{"name":"model-%03d:latest","size":1024,"details":{"family":"llama"},"capabilities":["completion","tools"]}`, i))
			}
			parts = append(parts, `{"name":"user/custom:v1","size":2048,"digest":"abc123","details":{"family":"qwen"},"capabilities":["completion","vision"]}`)
			fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(parts, ","))
		case "/api/show":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "user/custom:v1") {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":"not found"}`))
				return
			}
			w.Write([]byte(`{"parameters":"stop \"<eot>\"","details":{"family":"qwen"},"model_info":{"general.parameter_count":3212749888,"tokenizer.ggml.tokens":[]}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestServer(t *testing.T, base string) http.Handler {
	t.Helper()
	s, err := NewServer(ollama.New(base), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return s.Routes()
}

func get(h http.Handler, path string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestModelsPagination(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 30).URL) // 31 models -> 2 pages

	body := get(h, "/models", false).Body.String()
	for _, want := range []string{"31 model(s)", "Page 1 of 2", "model-000:latest", "Next →", "<!doctype html>", "on the models disk"} {
		if !strings.Contains(body, want) {
			t.Errorf("page 1 missing %q", want)
		}
	}
	if strings.Contains(body, "user/custom:v1") {
		t.Error("page 1 should not include the last model")
	}

	frag := get(h, "/models?page=2", true).Body.String()
	if strings.Contains(frag, "<!doctype html>") {
		t.Error("htmx request should get the fragment, not the full page")
	}
	if !strings.Contains(frag, "Page 2 of 2") || !strings.Contains(frag, `href="/models/user/custom:v1"`) {
		t.Errorf("page 2 fragment wrong:\n%s", frag)
	}

	// Out-of-range pages clamp to the last page.
	if !strings.Contains(get(h, "/models?page=99", true).Body.String(), "Page 2 of 2") {
		t.Error("page 99 should clamp to the last page")
	}
}

func TestModelsFilter(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 5).URL)
	body := get(h, "/models?q=QWEN", true).Body.String()
	if !strings.Contains(body, "1 model(s)") || !strings.Contains(body, "user/custom:v1") {
		t.Errorf("family filter should match case-insensitively:\n%s", body)
	}
}

func TestModelsOllamaDown(t *testing.T) {
	srv := fakeOllama(t, 1)
	srv.Close()
	body := get(newTestServer(t, srv.URL), "/models", false).Body.String()
	if !strings.Contains(body, "list models from Ollama") {
		t.Errorf("expected an error panel when Ollama is down:\n%s", body)
	}
}

func TestModelDetail(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	rec := get(h, "/models/user/custom:v1", false)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{"abc123", "2.0 KB", "general.parameter_count", "3212749888", "(list)"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}

	if rec := get(h, "/models/missing:latest", false); rec.Code != http.StatusNotFound {
		t.Errorf("unknown model status = %d, want 404", rec.Code)
	}
}

func TestModelsFilterCapability(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 5).URL)
	body := get(h, "/models?q=Vision", true).Body.String()
	if !strings.Contains(body, "1 model(s)") || !strings.Contains(body, "user/custom:v1") {
		t.Errorf("capability filter should match case-insensitively:\n%s", body)
	}
}

func TestModelsCapabilityChips(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 30).URL)

	page := get(h, "/models?cap=vision", false).Body.String()
	for _, c := range []string{"completion", "tools", "vision"} {
		if !strings.Contains(page, `name="cap" value="`+c+`"`) {
			t.Errorf("missing chip for %q", c)
		}
	}
	if !strings.Contains(page, `value="vision" checked`) {
		t.Error("selected chip should be checked")
	}

	// Every model has completion; only user/custom:v1 has vision too.
	body := get(h, "/models?cap=completion&cap=vision", true).Body.String()
	if !strings.Contains(body, "1 model(s)") || !strings.Contains(body, "user/custom:v1") {
		t.Errorf("chips should AND together:\n%s", body)
	}

	// 30 models have tools -> 2 pages, and paging keeps the filter.
	body = get(h, "/models?q=model&cap=tools", true).Body.String()
	if !strings.Contains(body, `hx-get="/models?cap=tools&amp;page=2&amp;q=model"`) {
		t.Errorf("next link should keep filters:\n%s", body)
	}
}
