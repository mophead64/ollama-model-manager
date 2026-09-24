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

func TestReleasePanel(t *testing.T) {
	render := func(t *testing.T, running, latest string) string {
		setVersion(t, running)
		s, err := NewServer(nil, nil, nil, nil, Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		s.updates.url = fakeGitHub(t, latest, "Some **notes**.")
		rec := httptest.NewRecorder()
		s.handleRelease(rec, httptest.NewRequest("GET", "/version/release", nil))
		return rec.Body.String()
	}

	cur := render(t, "v2026.09.20", "v2026.09.20")
	if !strings.Contains(cur, `<span class="badge on">This release</span>`) || !strings.Contains(cur, "<strong>notes</strong>") {
		t.Errorf("current release panel:\n%s", cur)
	}
	if strings.Contains(cur, "how to update") {
		t.Error("current release shouldn't point at updating")
	}

	old := render(t, "v2026.09.01", "v2026.09.20")
	for _, want := range []string{"New release", "You're running <span class=\"mono\">v2026.09.01</span>. The notes below say how to update."} {
		if !strings.Contains(old, want) {
			t.Errorf("update panel missing %q", want)
		}
	}
}

func TestAccountHasReleasePanel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	if body := get(h, "/account", false).Body.String(); !strings.Contains(body, `hx-get="/version/release"`) {
		t.Error("account page has no release panel")
	}
}
