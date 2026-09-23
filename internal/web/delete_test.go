package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

func TestDeleteModel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 30).URL)

	list := get(h, "/models?sort=size&dir=asc&page=2", true).Body.String()
	if !strings.Contains(list, `data-fill-name="user/custom:v1"`) ||
		!strings.Contains(list, `data-fill-return="/models?dir=asc&amp;page=2&amp;sort=size"`) {
		t.Errorf("rows should carry the model and the list state to return to:\n%s", list)
	}

	rec := do(h, "POST", "/models/delete", url.Values{
		"name": {"user/custom:v1"}, "return": {"/models?dir=asc&page=2&sort=size"},
	}, testSession)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d\n%s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != "/models?deleted=user%2Fcustom%3Av1&dir=asc&page=2&sort=size" {
		t.Errorf("redirect = %q", got)
	}
	if len(deletedModels) != 1 || deletedModels[0] != `{"model":"user/custom:v1"}` {
		t.Errorf("ollama received %v", deletedModels)
	}

	// A return URL pointing elsewhere is ignored.
	rec = do(h, "POST", "/models/delete", url.Values{"name": {"x"}, "return": {"https://evil.test/models"}}, testSession)
	if got := rec.Header().Get("Location"); got != "/models?deleted=x" {
		t.Errorf("foreign return URL should be dropped, got %q", got)
	}

	rec = do(h, "POST", "/models/delete", url.Values{"name": {"fail-me"}}, testSession)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "disk on fire") {
		t.Errorf("ollama failure should be shown, got %d", rec.Code)
	}
}

func TestDeleteDisabled(t *testing.T) {
	fake := fakeOllama(t, 1)
	st := newTestStore(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, _ := NewServer(ollama.New(fake.URL), st, downloads.New(st, ollama.New(fake.URL), log), sysinfo.New(time.Second, time.Minute, log), Config{AllowDelete: false}, log)
	h := s.Routes()
	u, _ := st.CreateUser(t.Context(), "admin", "test-password")
	token, _ := st.CreateSession(t.Context(), u.ID)
	c := &http.Cookie{Name: sessionCookie, Value: token}

	rec := do(h, "POST", "/models/delete", url.Values{"name": {"user/custom:v1"}}, c)
	if rec.Code != http.StatusForbidden || len(deletedModels) != 0 {
		t.Errorf("delete should be refused when disabled: %d, ollama got %v", rec.Code, deletedModels)
	}
	for _, path := range []string{"/models", "/models/user/custom:v1"} {
		if body := do(h, "GET", path, nil, c).Body.String(); strings.Contains(body, "delete-model-modal") {
			t.Errorf("%s shouldn't offer delete when disabled", path)
		}
	}
}
