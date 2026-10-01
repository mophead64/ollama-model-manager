package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestSystemPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	body := get(h, "/system", false).Body.String()
	for _, want := range []string{
		"<!doctype html>", `<span class="mono">0.12.3</span>`, `data-charts="/system/history"`, `data-metric="vram"`,
		"Collecting the first sample", // the test sampler never runs
		"Loaded in memory", "2 model(s) in use", "50%/50% CPU/GPU", "100% GPU", "8,192", nbsp("never (kept loaded)"),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("system page missing %q", want)
		}
	}

	frag := get(h, "/system", true).Body.String()
	if strings.Contains(frag, "<!doctype html>") || !strings.Contains(frag, `id="system-live"`) {
		t.Errorf("htmx request should get the live fragment:\n%s", frag)
	}
}

func TestSystemHistory(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	rec := get(h, "/system/history", false)
	var out struct {
		IntervalMS int64             `json:"interval_ms"`
		WindowMS   int64             `json:"window_ms"`
		Samples    []json.RawMessage `json:"samples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	if out.IntervalMS != 1000 || out.WindowMS != 60000 || out.Samples == nil {
		t.Errorf("history = %+v, want 1s interval, 1m window, empty samples", out)
	}
}

func TestDashboardLoad(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	body := get(h, "/", false).Body.String()
	for _, want := range []string{`id="load-tile"`, `hx-get="/system/load"`, `id="running-panel"`, "2 model(s) in use", "on the models disk"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// The models page has the same overview above its list, less system load.
	list := get(h, "/models", false).Body.String()
	for _, want := range []string{`id="running-panel"`, "2 model(s) in use", "on the models disk"} {
		if !strings.Contains(list, want) {
			t.Errorf("models page missing %q", want)
		}
	}
	if strings.Contains(list, `id="load-tile"`) {
		t.Error("models page shouldn't have the system load tile")
	}
	if tile := get(h, "/system/load", true).Body.String(); !strings.Contains(tile, "VRAM") {
		t.Errorf("load tile fragment wrong:\n%s", tile)
	}
	if panel := get(h, "/system/running", true).Body.String(); !strings.Contains(panel, "user/custom:v1") {
		t.Errorf("running panel fragment wrong:\n%s", panel)
	}
}

func TestDashboardShowsEmptyRunningPanel(t *testing.T) {
	fake := fakeOllama(t, 1)
	target, _ := url.Parse(fake.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	idle := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ps" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(idle.Close)
	h := newTestServer(t, idle.URL)

	const empty = "No models are loaded right now"
	if body := get(h, "/", false).Body.String(); !strings.Contains(body, empty) || !strings.Contains(body, `hx-get="/system/running?show=empty"`) {
		t.Errorf("dashboard should show the empty panel and keep it when polled:\n%s", body)
	}
	if body := get(h, "/system/running?show=empty", true).Body.String(); !strings.Contains(body, empty) {
		t.Error("polled panel should stay visible with show=empty")
	}
	if body := get(h, "/models", false).Body.String(); strings.Contains(body, empty) {
		t.Error("models page should still hide the panel when nothing's loaded")
	}
}
