package web

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSourceOf(t *testing.T) {
	for name, want := range map[string]modelSource{
		"qwen3:8b":                    {Site: "ollama.com", URL: "https://ollama.com/library/qwen3", QuantsURL: "/discover/quants?name=qwen3%3A8b"},
		"user/custom:v1":              {Site: "ollama.com", URL: "https://ollama.com/user/custom"}, // tags can't be listed
		"hf.co/owner/Small-GGUF:Q8_0": {Site: "Hugging Face", URL: "https://huggingface.co/owner/Small-GGUF", QuantsURL: "/discover/quants?name=hf.co%2Fowner%2FSmall-GGUF%3AQ8_0"},
		"example.com/team/model:1":    {},
	} {
		if got := sourceOf(name); got != want {
			t.Errorf("sourceOf(%q) = %+v, want %+v", name, got, want)
		}
	}
}

func TestOtherQuants(t *testing.T) {
	lib, hf := fakeLibrary(t), fakeHF(t)
	// fakeOllama's /api/show only knows user/custom:v1; model-000's page is needed too.
	fake := fakeOllama(t, 1)
	target, _ := url.Parse(fake.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			w.Write([]byte(`{"details":{"family":"llama"},"capabilities":["completion"],"model_info":{}}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h := newTestServer(t, srv.URL, func(c *Config) { c.LibraryURL, c.HFURL = lib.URL, hf.URL })
	testStore.MarkModelUsed(t.Context(), "model-000:latest", time.Now())

	// On each model menu and the model page: other quants where they can be
	// listed, and a link to the model's page.
	opener := `data-dialog-open="quants-modal"` + "\n" + `  data-fill-name="model-000:latest" data-fill-site="ollama.com" data-fill-url="https://ollama.com/library/model-000"`
	for _, path := range []string{"/", "/models", "/models/model-000:latest"} {
		body := get(h, path, false).Body.String()
		for _, want := range []string{opener, `hx-get="/discover/quants?name=model-000%3Alatest"`, `href="https://ollama.com/library/model-000"`, `id="quants-modal"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
	// A namespaced ollama.com model: a link, but its tags can't be listed.
	list := get(h, "/models", false).Body.String()
	if !strings.Contains(list, `href="https://ollama.com/user/custom"`) || strings.Contains(list, `data-fill-name="user/custom:v1" data-fill-site`) {
		t.Error("user/custom should have a View link and no Other quants")
	}

	// The dialog's list: Discover's, for that model, with the installed one marked.
	body := get(h, "/discover/quants?name=model-000:latest", true).Body.String()
	if !strings.Contains(body, "model-000:8b") || !strings.Contains(body, ">Installed<") || strings.Contains(body, "<!doctype html>") {
		t.Errorf("ollama.com quants wrong:\n%s", body)
	}
	body = get(h, "/discover/quants?name=hf.co/owner/Small-GGUF:Q8_0", true).Body.String()
	if !strings.Contains(body, `value="hf.co/owner/Small-GGUF:Q4_K_M"`) {
		t.Errorf("Hugging Face quants wrong:\n%s", body)
	}
	if body := get(h, "/discover/quants?name=user/custom:v1", true).Body.String(); !strings.Contains(body, "can only be listed for models from the ollama.com library or Hugging Face") {
		t.Errorf("unlistable model: %s", body)
	}
}
