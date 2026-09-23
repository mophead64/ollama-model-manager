package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLoadModel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	rec := do(h, "POST", "/models/load", url.Values{"name": {"user/custom:v1"}, "keep_alive": {"30m"}}, testSession,
		"Referer", "http://example.test/models?q=qwen&deleted=old")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("load = %d\n%s", rec.Code, rec.Body)
	}
	// Back where it came from, with the notice replacing any earlier one.
	if got := rec.Header().Get("Location"); got != "/models?loaded=user%2Fcustom%3Av1&q=qwen" {
		t.Errorf("redirect = %q", got)
	}
	if len(generateCalls) != 1 || generateCalls[0] != `{"keep_alive":"30m","model":"user/custom:v1"}` {
		t.Errorf("ollama received %v", generateCalls)
	}

	if body := get(h, "/models?q=qwen&loaded=user/custom:v1", false).Body.String(); !strings.Contains(body, "Loaded <strong>user/custom:v1</strong> into memory") {
		t.Error("models page should show the loaded notice")
	}

	// Only the dialog's keep-alive choices are accepted.
	if rec := do(h, "POST", "/models/load", url.Values{"name": {"x"}, "keep_alive": {"99y"}}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown keep_alive = %d, want 400", rec.Code)
	}

	// Ollama's reason for a failure is passed on.
	rec = do(h, "POST", "/models/load", url.Values{"name": {"fail-me"}}, testSession, "Referer", "http://example.test/system")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Path != "/system" || !strings.Contains(loc.Query().Get("memerror"), "requires more system memory") {
		t.Errorf("failed load redirect = %q", rec.Header().Get("Location"))
	}
}

func TestUnloadModel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	rec := do(h, "POST", "/models/unload", url.Values{"name": {"model-000:latest"}}, testSession, "Referer", "http://example.test/system")
	if got := rec.Header().Get("Location"); got != "/system?unloaded=model-000%3Alatest" {
		t.Errorf("redirect = %q", got)
	}
	if len(generateCalls) != 1 || generateCalls[0] != `{"keep_alive":0,"model":"model-000:latest"}` {
		t.Errorf("ollama received %v", generateCalls)
	}

	// Without a usable Referer it falls back to the models list; a Referer
	// can't send the browser off-site.
	for _, ref := range []string{"", "http://evil.test//evil.test/x", "http://example.test/login"} {
		rec := do(h, "POST", "/models/unload", url.Values{"name": {"x"}}, testSession, "Referer", ref)
		if got := rec.Header().Get("Location"); got != "/models?unloaded=x" {
			t.Errorf("Referer %q: redirect = %q", ref, got)
		}
	}
}

func TestLoadUnloadButtons(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// The fake has user/custom:v1 and model-000:latest loaded.
	list := get(h, "/models?q=custom", true).Body.String()
	if !strings.Contains(list, `action="/models/unload"`) || strings.Contains(list, `data-dialog-open="load-model-modal"`) {
		t.Errorf("a loaded model's row should offer Unload:\n%s", list)
	}

	detail := get(h, "/models/user/custom:v1", false).Body.String()
	for _, want := range []string{`action="/models/unload"`, "50%/50% CPU/GPU", "Unloads", `id="load-model-modal"`, `value="-1"`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail page missing %q", want)
		}
	}

	if system := get(h, "/system", false).Body.String(); strings.Count(system, `action="/models/unload"`) != 2 {
		t.Error("each loaded model on the system page should have an Unload button")
	}
}
