package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

// fakeOllama serves n models named model-000:latest... plus one namespaced
// model, and a /api/show that knows only "user/custom:v1".
// deletedModels records the /api/delete bodies fakeOllama received.
var deletedModels []string

// generateCalls records the /api/generate (load/unload) bodies fakeOllama received.
var generateCalls []string

func fakeOllama(t *testing.T, n int) *httptest.Server {
	t.Helper()
	deletedModels = nil
	generateCalls = nil
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			var parts []string
			for i := 0; i < n; i++ {
				parts = append(parts, fmt.Sprintf(`{"name":"model-%03d:latest","size":1024,"details":{"family":"llama"},"capabilities":["completion","tools"]}`, i))
			}
			parts = append(parts, `{"name":"user/custom:v1","size":2048,"digest":"abc123","details":{"family":"qwen"},"capabilities":["completion","vision"]}`)
			fmt.Fprintf(w, `{"models":[%s]}`, strings.Join(parts, ","))
		case "/api/ps":
			// One model split across CPU and GPU, one kept loaded indefinitely.
			w.Write([]byte(`{"models":[` +
				`{"name":"user/custom:v1","size":4000,"size_vram":2000,"context_length":8192,"expires_at":"2099-01-01T00:00:00Z"},` +
				`{"name":"model-000:latest","size":1000,"size_vram":1000,"expires_at":"2318-01-01T00:00:00Z"}]}`))
		case "/api/generate":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "fail-me") {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"model requires more system memory (12 GiB) than is available (4 GiB)"}`))
				return
			}
			generateCalls = append(generateCalls, string(body))
			w.Write([]byte(`{"done":true}`))
		case "/api/me": // signed out of ollama.com: offers a link carrying Ollama's key
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","signin_url":"https://ollama.com/connect?name=test\u0026key=c3NoLWVkMjU1MTkgQUFBQXRlc3RrZXk"}`))
		case "/api/version":
			w.Write([]byte(`{"version":"0.12.3"}`))
		case "/api/pull":
			for _, line := range []string{
				`{"status":"pulling manifest"}`,
				`{"status":"pulling abc","digest":"sha256:abc","total":2048,"completed":2048}`,
				`{"status":"success"}`,
			} {
				fmt.Fprintln(w, line)
			}
		case "/api/delete":
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), "fail-me") {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"disk on fire"}`))
				return
			}
			deletedModels = append(deletedModels, string(body))
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

// testSession is the cookie get() sends, so tests exercise pages as a
// signed-in user. Set by newTestServer.
var testSession *http.Cookie

// testManager, testStore and testSampler are the download manager, store and
// hardware sampler behind the last newTestServer; the manager isn't running
// unless a test starts it, and the sampler never runs (set its snapshot).
var (
	testManager *downloads.Manager
	testStore   *store.Store
	testSampler *sysinfo.Sampler
)

// fakeRegistry answers model manifest checks: 404 for "missing*", a 1 PiB
// manifest for "huge*", 200 otherwise.
type fakeRegistry struct{}

func (fakeRegistry) RoundTrip(r *http.Request) (*http.Response, error) {
	code, body := http.StatusOK, ""
	if strings.Contains(r.URL.Path, "/missing") {
		code = http.StatusNotFound
	}
	if strings.Contains(r.URL.Path, "/huge") {
		body = `{"config":{"size":0},"layers":[{"size":1125899906842624}]}` // 1 PiB: bigger than any test disk
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// newTestServer serves the app against a fake Ollama at base. opts can adjust
// its config; the ollama.com library is unreachable unless one sets it.
func newTestServer(t *testing.T, base string, opts ...func(*Config)) http.Handler {
	t.Helper()
	st := newTestStore(t)
	u, err := st.CreateUser(context.Background(), "admin", "test-password")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := st.CreateSession(context.Background(), u.ID)
	testSession = &http.Cookie{Name: sessionCookie, Value: token}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	testManager = downloads.New(st, ollama.New(base), log)
	testManager.SetRegistryClient(&http.Client{Transport: fakeRegistry{}})
	testStore = st
	cfg := Config{ModelsDir: t.TempDir(), AllowDelete: true, LibraryURL: "http://127.0.0.1:1"}
	for _, o := range opts {
		o(&cfg)
	}
	testSampler = sysinfo.New(time.Second, time.Minute, log)
	s, err := NewServer(ollama.New(base), st, testManager, testSampler, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return s.Routes()
}

func get(h http.Handler, path string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(testSession)
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
	// (The full page also lists it in the "loaded in memory" panel.)
	if strings.Contains(get(h, "/models", true).Body.String(), "user/custom:v1") {
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
