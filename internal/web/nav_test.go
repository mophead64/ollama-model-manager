package web

import (
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
	rec := do(h, "GET", "/static/logo.png", nil, nil) // signed out, as on the login page
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("logo: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	login := do(h, "GET", "/login", nil, nil).Body.String()
	for _, want := range []string{`rel="icon" type="image/png" href="/static/logo.png"`, `<img src="/static/logo.png"`} {
		if !strings.Contains(login, want) {
			t.Errorf("login page missing %q", want)
		}
	}
}
