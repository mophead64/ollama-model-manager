package web

import (
	"regexp"
	"testing"
)

var currentLinkRE = regexp.MustCompile(`<a href="([^"]+)"[^>]*aria-current="page"`)

func TestNavMarksCurrentPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	for path, want := range map[string]string{
		"/models":                "/models",
		"/models/user/custom:v1": "/models",
		"/downloads":             "/downloads",
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
