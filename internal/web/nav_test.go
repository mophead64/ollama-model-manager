package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var currentLinkRE = regexp.MustCompile(`<a href="([^"]+)"[^>]*aria-current="page"`)

func TestNavMarksCurrentPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	for path, want := range map[string]string{
		"/models":                "/models",
		"/models/user/custom:v1": "/models",
		"/downloads":             "/downloads",
		"/discover":              "/discover",
		"/system":                "/system",
		"/account":               "/account",
	} {
		body := get(h, path, false).Body.String()
		got := currentLinkRE.FindAllStringSubmatch(body, -1)
		if len(got) != 1 || got[0][1] != want {
			t.Errorf("%s: current nav links = %v, want just %s", path, got, want)
		}
	}
}

func TestLogoServedToEveryone(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	rec := do(h, "GET", "/static/logo-64.png", nil, nil) // signed out, as on the login page
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("logo: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	login := do(h, "GET", "/login", nil, nil).Body.String()
	for _, want := range []string{
		`rel="icon" type="image/png" href="` + staticURL("logo-64.png") + `"`,
		`rel="apple-touch-icon" href="` + staticURL("logo-180.png") + `"`,
		`<img src="data:image/png;base64,`,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}

// Embedded files have no modtime, so without explicit caching headers the
// logo and favicon were re-downloaded on every page load and flickered.
func TestStaticAssetsCacheable(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	url := staticURL("logo-64.png")
	if !strings.Contains(url, "?v=") {
		t.Fatalf("staticURL has no version: %s", url)
	}
	rec := do(h, "GET", url, nil, nil)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("versioned URL Cache-Control = %q, want immutable", cc)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	rec = do(h, "GET", "/static/logo-64.png?v=stale", nil, nil)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("stale version Cache-Control = %q, want no-cache", cc)
	}

	req := httptest.NewRequest("GET", "/static/logo-64.png", nil)
	req.Header.Set("If-None-Match", etag)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation with matching ETag: got %d, want 304", w.Code)
	}
}
