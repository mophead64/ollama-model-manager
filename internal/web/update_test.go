package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/version"
)

func fakeGitHub(t *testing.T, tag, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tag, "name": "Spring release", "html_url": "https://github.com/x/y/releases/tag/" + tag,
			"body": body, "published_at": "2026-09-20T10:00:00Z",
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func setVersion(t *testing.T, v string) {
	old := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = old })
}

func TestReleaseNotesRenderedSafely(t *testing.T) {
	setVersion(t, "v2026.09.01")
	u := newUpdateChecker()
	u.url = fakeGitHub(t, "v2026.09.20", "## What's new\n\n- **Faster** [docs](https://example.com)\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))")
	info := u.check(context.Background(), false)
	if !info.Checked || !info.Available || info.Current {
		t.Fatalf("info = %+v", info)
	}
	notes := string(info.Notes)
	for _, want := range []string{"<h2>What's new</h2>", "<strong>Faster</strong>", `<a href="https://example.com" target="_blank" rel="noopener noreferrer">docs</a>`} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	for _, bad := range []string{"<script", `href="javascript:`} {
		if strings.Contains(notes, bad) {
			t.Errorf("notes contain %q:\n%s", bad, notes)
		}
	}
}

func TestSystemAppPanel(t *testing.T) {
	render := func(t *testing.T, running, latest string) string {
		setVersion(t, running)
		s, err := NewServer(nil, nil, nil, nil, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		s.updates.url = fakeGitHub(t, latest, "Some **notes**.")
		rec := httptest.NewRecorder()
		s.handleAppUpdate(rec, httptest.NewRequest("GET", "/system/app", nil))
		return rec.Body.String()
	}

	cur := render(t, "v2026.09.20", "v2026.09.20")
	for _, want := range []string{
		`<span class="mono">v2026.09.20</span>`, `<button type="button" class="badge on" data-dialog-open="app-update-modal"`,
		`data-dialog-scroll="app-update-modal-notes">Release notes</button>`, `data-dialog-scroll="app-update-modal-how">How to update</button>`,
		`<dialog id="app-update-modal"`, "Ollama Model Manager v2026.09.20</h2>", "<h3>Release notes for v2026.09.20</h3>", "<strong>notes</strong>",
		"<h3>How to update Ollama Model Manager</h3>", `value="docker compose pull &amp;&amp; docker compose up -d"`,
	} {
		if !strings.Contains(cur, want) {
			t.Errorf("up to date panel missing %q:\n%s", want, cur)
		}
	}
	if strings.Contains(cur, "is out") || strings.Contains(cur, `hx-trigger="load"`) {
		t.Errorf("up to date panel shouldn't say there's an update, or refresh itself again:\n%s", cur)
	}

	old := render(t, "v2026.09.01", "v2026.09.20")
	for _, want := range []string{
		`data-dialog-open="app-update-modal" title="What's new, and how to update">Update available: v2026.09.20</button>`,
		"Update Ollama Model Manager to v2026.09.20", `you're running <span class="mono">v2026.09.01</span>`,
		"<h3>Release notes for v2026.09.20</h3>", "<strong>notes</strong>",
		"It depends on how you run it", `value="docker pull ghcr.io/mophead64/ollama-model-manager:latest"`,
		`value="xattr -d com.apple.quarantine ollama-model-manager"`, `value="git pull &amp;&amp; ./run-local.sh"`,
	} {
		if !strings.Contains(old, want) {
			t.Errorf("update panel missing %q:\n%s", want, old)
		}
	}
	if strings.Contains(old, "<summary>Release notes") || strings.Contains(old, "system-update-panel") {
		t.Error("the release notes and steps belong in the modal, not inline")
	}
}

func TestSystemPageLayout(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL) // two models, 3 KB between them
	body := get(h, "/system", false).Body.String()
	for _, want := range []string{
		`<div class="value">2</div><div class="label">models installed</div>`,
		`<div class="value">` + nbsp(formatBytes(3072)) + `</div><div class="label">storage used by models (estimate)</div>`,
		`hx-get="/system/ollama" hx-trigger="load"`, `hx-get="/system/app" hx-trigger="load"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("system page missing %q", want)
		}
	}
	// Ollama and this app's versions come last, after the graphs.
	if strings.Index(body, `id="ollama-panel"`) < strings.Index(body, `data-charts=`) || strings.Index(body, `id="app-panel"`) < strings.Index(body, `data-charts=`) {
		t.Error("the version panels should be at the bottom of the page")
	}
}

func TestAccountHasNoReleasePanel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	if body := get(h, "/account", false).Body.String(); strings.Contains(body, "Latest release") {
		t.Error("the release panel is on the System page now, not Settings")
	}
}

func TestVersionStatus(t *testing.T) {
	render := func(t *testing.T, running, latest string) string {
		setVersion(t, running)
		s, err := NewServer(nil, nil, nil, nil, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		s.updates.url = fakeGitHub(t, latest, "")
		rec := httptest.NewRecorder()
		s.handleVersionCheck(rec, httptest.NewRequest("GET", "/version/check", nil))
		return rec.Body.String()
	}
	if cur := render(t, "v2026.09.20", "v2026.09.20"); !strings.Contains(cur, `<span class="badge on" title="Checked `) || !strings.Contains(cur, "✓ Up to date</span>") {
		t.Errorf("up to date status:\n%s", cur)
	}
	if old := render(t, "v2026.09.01", "v2026.09.20"); !strings.Contains(old, "Update available: v2026.09.20") || strings.Contains(old, "Up to date") {
		t.Errorf("update available status:\n%s", old)
	}
	if updateOKTTL > 2*time.Hour {
		t.Errorf("a successful check is cached for %s; it should be re-checked every couple of hours", updateOKTTL)
	}
}
