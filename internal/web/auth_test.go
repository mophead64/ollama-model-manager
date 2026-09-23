package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// authServer is a server with one user (admin / test-password) and no session.
func authServer(t *testing.T) http.Handler {
	t.Helper()
	h := newTestServer(t, fakeOllama(t, 1).URL)
	return h
}

func do(h http.Handler, method, path string, form url.Values, cookie *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

func login(t *testing.T, h http.Handler, user, pw string) *http.Cookie {
	t.Helper()
	rec := do(h, "POST", "/login", url.Values{"username": {user}, "password": {pw}}, nil)
	c := sessionFrom(rec)
	if rec.Code != http.StatusSeeOther || c == nil {
		t.Fatalf("login(%q) = %d, cookie %v:\n%s", user, rec.Code, c, rec.Body)
	}
	return c
}

func TestUnauthenticatedRedirects(t *testing.T) {
	h := authServer(t)

	rec := do(h, "GET", "/models?q=llama", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next=%2Fmodels%3Fq%3Dllama" {
		t.Errorf("page: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = do(h, "GET", "/models", nil, nil, "HX-Request", "true")
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") == "" {
		t.Errorf("htmx: %d, HX-Redirect %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if rec := do(h, "GET", "/static/htmx.min.js", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("static assets should be public, got %d", rec.Code)
	}
	if rec := do(h, "GET", "/login", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("login page should be public, got %d", rec.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	h := authServer(t)

	rec := do(h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"nope"}}, nil)
	if rec.Code != http.StatusUnauthorized || sessionFrom(rec) != nil || !strings.Contains(rec.Body.String(), "Incorrect username or password") {
		t.Errorf("bad password: %d", rec.Code)
	}

	rec = do(h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"test-password"}, "next": {"/models?q=x"}}, nil)
	if rec.Header().Get("Location") != "/models?q=x" {
		t.Errorf("should return to next, got %q", rec.Header().Get("Location"))
	}
	c := sessionFrom(rec)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie wrong: %+v", c)
	}

	page := do(h, "GET", "/models", nil, c)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `href="/account"`) {
		t.Errorf("signed-in page should show the account link, got %d", page.Code)
	}

	rec = do(h, "POST", "/logout", url.Values{}, c)
	if rec.Header().Get("Location") != "/login" {
		t.Errorf("logout redirect = %q", rec.Header().Get("Location"))
	}
	if rec := do(h, "GET", "/models", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("session should be dead after logout, got %d", rec.Code)
	}
}

func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"/models?q=1":       "/models?q=1",
		"":                  "/",
		"https://evil.test": "/",
		"//evil.test":       "/",
		"/\\evil.test":      "/",
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := authServer(t)
	bad := url.Values{"username": {"admin"}, "password": {"wrong-guess"}}
	for i := 0; i < loginMaxFails; i++ {
		do(h, "POST", "/login", bad, nil)
	}
	rec := do(h, "POST", "/login", url.Values{"username": {"admin"}, "password": {"test-password"}}, nil)
	if rec.Code != http.StatusTooManyRequests || sessionFrom(rec) != nil {
		t.Errorf("after %d failures even the right password should be refused, got %d", loginMaxFails, rec.Code)
	}
}

func TestChangeCredentials(t *testing.T) {
	h := authServer(t)
	c := login(t, h, "admin", "test-password")
	other := login(t, h, "admin", "test-password") // a second browser

	rec := do(h, "POST", "/account/password", url.Values{
		"current_password": {"wrong"}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"},
	}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Current password is incorrect") {
		t.Errorf("wrong current password: %d", rec.Code)
	}
	rec = do(h, "POST", "/account/password", url.Values{
		"current_password": {"test-password"}, "new_password": {"new-password-1"}, "confirm_password": {"different-1"},
	}, c)
	if !strings.Contains(rec.Body.String(), "don&#39;t match") {
		t.Errorf("mismatch should be reported:\n%s", rec.Body)
	}
	rec = do(h, "POST", "/account/password", url.Values{
		"current_password": {"test-password"}, "new_password": {"new-password-1"}, "confirm_password": {"new-password-1"},
	}, c)
	if rec.Header().Get("Location") != "/account?done=password" {
		t.Fatalf("password change failed: %d\n%s", rec.Code, rec.Body)
	}
	if do(h, "GET", "/account", nil, c).Code != http.StatusOK {
		t.Error("the session that changed the password should stay signed in")
	}
	if do(h, "GET", "/account", nil, other).Code != http.StatusSeeOther {
		t.Error("other sessions should be signed out")
	}

	rec = do(h, "POST", "/account/username", url.Values{"username": {"josh"}, "current_password": {"new-password-1"}}, c)
	if rec.Header().Get("Location") != "/account?done=username" {
		t.Fatalf("username change failed: %d\n%s", rec.Code, rec.Body)
	}
	login(t, h, "josh", "new-password-1")
}

func TestCrossOriginPostRejected(t *testing.T) {
	h := authServer(t)
	c := login(t, h, "admin", "test-password")
	rec := do(h, "POST", "/account/password", url.Values{
		"current_password": {"test-password"}, "new_password": {"hijacked-pw"}, "confirm_password": {"hijacked-pw"},
	}, c, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site POST should be forbidden, got %d", rec.Code)
	}
}

func TestAccountDialogs(t *testing.T) {
	h := authServer(t)
	c := login(t, h, "admin", "test-password")

	page := do(h, "GET", "/account", nil, c).Body.String()
	for _, want := range []string{`data-dialog-open="change-username-modal"`, `data-dialog-open="change-password-modal"`, `/static/dialog.js`} {
		if !strings.Contains(page, want) {
			t.Errorf("account page missing %q", want)
		}
	}
	if strings.Contains(page, "data-dialog-autoopen") {
		t.Error("no dialog should auto-open without an error")
	}

	// A failed change re-renders with only that form's dialog set to reopen.
	rec := do(h, "POST", "/account/username", url.Values{"username": {"x"}, "current_password": {"wrong"}}, c)
	body := rec.Body.String()
	if !strings.Contains(body, `id="change-username-modal" aria-labelledby="change-username-title" data-dialog-autoopen`) {
		t.Errorf("username dialog should auto-open after a failed change:\n%s", body)
	}
	if strings.Count(body, "data-dialog-autoopen") != 1 {
		t.Error("only the failing form's dialog should auto-open")
	}
}
