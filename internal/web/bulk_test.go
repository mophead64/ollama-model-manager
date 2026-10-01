package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// quantsOllama serves qwen3:8b at two quantisations (and qwen3:latest, the
// same model as qwen3:8b), llama3.2:3b, and a loaded model, recording deletes
// and generate (load/unload) calls.
func quantsOllama(t *testing.T) (srv *httptest.Server, calls func() []string) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[
				{"name":"qwen3:8b","size":5000000000,"digest":"aaa","modified_at":"2026-01-01T00:00:00Z","details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q4_K_M"},"capabilities":["completion"]},
				{"name":"qwen3:latest","size":5000000000,"digest":"aaa","modified_at":"2026-01-01T00:00:00Z","details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q4_K_M"},"capabilities":["completion"]},
				{"name":"qwen3:8b-q8_0","size":8700000000,"digest":"bbb","modified_at":"2026-01-01T00:00:00Z","details":{"family":"qwen3","parameter_size":"8.2B","quantization_level":"Q8_0"},"capabilities":["completion"]},
				{"name":"llama3.2:3b","size":2000000000,"digest":"ccc","modified_at":"2026-01-01T00:00:00Z","details":{"family":"llama","parameter_size":"3.2B","quantization_level":"Q4_K_M"},"capabilities":["completion"]}]}`))
		case "/api/ps":
			w.Write([]byte(`{"models":[{"name":"llama3.2:3b","size":2000000000,"size_vram":2000000000,"expires_at":"2099-01-01T00:00:00Z"}]}`))
		case "/api/delete", "/api/generate":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			got = append(got, r.URL.Path+" "+string(body))
			mu.Unlock()
			if strings.Contains(string(body), "fail-me") {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"disk on fire"}`))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

func TestDuplicateQuants(t *testing.T) {
	srv, _ := quantsOllama(t)
	h := newTestServer(t, srv.URL)
	testStore.MarkModelUsed(t.Context(), "qwen3:latest", time.Now().Add(-2*time.Hour)) // under its other tag
	testStore.AddModelUsage(t.Context(), "qwen3:latest", time.Now().Format(time.DateOnly), 600, 4)

	body := get(h, "/models", false).Body.String()
	for _, want := range []string{
		`<details class="panel dupes" id="dupes">`, "1 model is installed in more than one quantisation.",
		"<code>qwen3</code> <span class=\"muted\">8.2B</span>",
		// The same model under two tags is one entry, with its use under either.
		`also qwen3:latest`, `<span title="`, "2 hours ago", `<td class="tnum">4</td>`, "≈ 10 min",
		`data-fill-name="qwen3:8b-q8_0"`, "Q8_0",
		// Deleting it deletes both its tags, freeing the space shown.
		`data-delete-names="[&#34;qwen3:8b&#34;,&#34;qwen3:latest&#34;]"`,
		`href="/models/testing?model=qwen3%3A8b&amp;model=qwen3%3A8b-q8_0&amp;name=Compare&#43;qwen3&#43;8.2B&#43;quantisations"`,
		// And flagged in the table.
		`title="Also installed as qwen3:8b-q8_0 (Q8_0)">+1 quant</a>`,
		`title="The same model as qwen3:latest (same digest), so stored once">= qwen3:latest</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("models page missing %q", want)
		}
	}
	if strings.Contains(body, "<code>llama3.2</code>") {
		t.Error("llama3.2 has one quantisation, so isn't a duplicate")
	}

	// The link fills in a test comparing them.
	tests := get(h, "/models/testing?model=qwen3%3A8b&model=qwen3%3A8b-q8_0&name=Compare+qwen3+8.2B+quantisations", false).Body.String()
	for _, want := range []string{`value="Compare qwen3 8.2B quantisations"`, `value="qwen3:8b" checked`, `value="qwen3:8b-q8_0" checked`} {
		if !strings.Contains(tests, want) {
			t.Errorf("test form missing %q", want)
		}
	}
}

func TestUnusedModels(t *testing.T) {
	srv, _ := quantsOllama(t)
	h := newTestServer(t, srv.URL)
	// Tracked since well before the models were downloaded (1 Jan 2026);
	// only llama3.2 has been used since.
	testStore.MarkModelUsed(t.Context(), "llama3.2:3b", time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	testStore.UsageTrackedSince(t.Context()) // noted from that first use
	testStore.MarkModelUsed(t.Context(), "llama3.2:3b", time.Now().Add(-time.Hour))

	dash := get(h, "/", false).Body.String()
	// qwen3:8b and qwen3:latest are one model: 2 models, 13.7 GB.
	for _, want := range []string{"2 models unused for 60+ days, " + formatBytes(13700000000), `href="/models?unused=60"`} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}

	list := get(h, "/models?unused=60", true).Body.String()
	for _, want := range []string{"3 model(s) not used in 60+ days", "would free <strong>" + formatBytes(13700000000) + "</strong>",
		`value="qwen3:8b"`, `value="qwen3:latest"`, `value="qwen3:8b-q8_0"`} {
		if !strings.Contains(list, want) {
			t.Errorf("unused list missing %q", want)
		}
	}
	if strings.Contains(list, `value="llama3.2:3b"`) {
		t.Error("llama3.2 was used an hour ago")
	}
	if page := get(h, "/models", false).Body.String(); !strings.Contains(page, `<option value="60">not in 60+ days</option>`) {
		t.Error("the filter should offer 60 days")
	}
	if !strings.Contains(get(h, "/models?unused=60&sort=name&dir=asc", true).Body.String(), `href="/models?dir=desc&amp;sort=name&amp;unused=60"`) {
		t.Error("sorting should keep the unused filter")
	}

	// Nothing's unused when tracking has only just begun.
	h = newTestServer(t, srv.URL)
	if dash := get(h, "/", false).Body.String(); strings.Contains(dash, "unused for") {
		t.Error("a fresh install knows of nothing unused")
	}
}

func TestBulkDelete(t *testing.T) {
	srv, calls := quantsOllama(t)
	h := newTestServer(t, srv.URL)

	// The dialog lists them and what deleting them frees.
	confirm := get(h, "/models/bulk/confirm?name=qwen3:8b&name=qwen3:latest&name=gone:1b&return=/models?unused%3D60", true).Body.String()
	for _, want := range []string{"Delete 2 models?", `<input type="hidden" name="name" value="qwen3:8b">`, `<input type="hidden" name="name" value="qwen3:latest">`,
		"freeing up to " + formatBytes(5000000000), "left out: <code>gone:1b</code>", `name="return" value="/models?unused=60"`,
		`Also add them to the blacklist`, `id="bl-reason-bulk"`} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirm dialog missing %q:\n%s", want, confirm)
		}
	}

	rec := do(h, "POST", "/models/bulk/delete", url.Values{
		"name": {"qwen3:8b", "qwen3:latest"}, "blacklist": {"on"}, "reason": {"Too slow"}, "return": {"/models?unused=60"},
	}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/models?blacklisted=1&deleted=2+models&unused=60" {
		t.Fatalf("bulk delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := calls(); len(got) != 2 || got[0] != `/api/delete {"model":"qwen3:8b"}` || got[1] != `/api/delete {"model":"qwen3:latest"}` {
		t.Errorf("ollama received %v", got)
	}
	bl, _ := testStore.Blacklist(t.Context())
	if len(bl) != 2 || bl[0].Reason != "Too slow" || bl[0].Quantization != "Q4_K_M" {
		t.Errorf("blacklist = %+v", bl)
	}
	if body := get(h, "/models?blacklisted=1&deleted=2+models", false).Body.String(); !strings.Contains(body, "Deleted <strong>2 models</strong> and added them to the") {
		t.Error("notice should say both were deleted and blacklisted")
	}

	// Failures are reported alongside what worked.
	rec = do(h, "POST", "/models/bulk/delete", url.Values{"name": {"llama3.2:3b", "fail-me:1b"}}, testSession)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if q := loc.Query(); q.Get("deleted") != "llama3.2:3b" || q.Get("memerror") != "Couldn't delete fail-me:1b: disk on fire" {
		t.Errorf("partial failure redirect = %s", loc)
	}

	if rec := do(h, "POST", "/models/bulk/delete", url.Values{}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("nothing selected = %d, want 400", rec.Code)
	}
	h = newTestServer(t, srv.URL, func(c *Config) { c.AllowDelete = false })
	if rec := do(h, "POST", "/models/bulk/delete", url.Values{"name": {"qwen3:8b"}}, testSession); rec.Code != http.StatusForbidden {
		t.Errorf("deletes disabled = %d, want 403", rec.Code)
	}
}

func TestBulkUnload(t *testing.T) {
	srv, calls := quantsOllama(t)
	h := newTestServer(t, srv.URL)

	// Only llama3.2 is loaded: the others are left alone (unloading them would load them).
	rec := do(h, "POST", "/models/bulk/unload", url.Values{"name": {"qwen3:8b", "llama3.2:3b"}}, testSession, "Referer", "http://example.com/models?unused=30")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/models?unloaded=llama3.2%3A3b&unused=30" {
		t.Errorf("bulk unload = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got := calls(); len(got) != 1 || !strings.Contains(got[0], `"model":"llama3.2:3b"`) || !strings.Contains(got[0], `"keep_alive":0`) {
		t.Errorf("ollama received %v", got)
	}

	rec = do(h, "POST", "/models/bulk/unload", url.Values{"name": {"qwen3:8b"}}, testSession)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "memerror=None+of+the+selected+models+were+loaded.") {
		t.Errorf("none loaded = %q", loc)
	}
}

func TestModelsPageSelection(t *testing.T) {
	srv, _ := quantsOllama(t)
	h := newTestServer(t, srv.URL)
	body := get(h, "/models", false).Body.String()
	for _, want := range []string{
		`data-matching="[&#34;`, `<input type="checkbox" data-bulk-page`, `<input type="checkbox" class="bulk-check" value="qwen3:8b"`,
		`id="bulk-bar" hidden`, `action="/models/bulk/unload"`, `data-bulk-delete`, `id="bulk-delete-modal"`, "/static/models.js",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("models page missing %q", want)
		}
	}
}
