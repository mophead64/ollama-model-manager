package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSystemPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	body := get(h, "/system", false).Body.String()
	for _, want := range []string{
		"<!doctype html>", "version 0.12.3", `data-charts="/system/history"`, `data-metric="vram"`,
		"Collecting the first sample", // the test sampler never runs
		"Loaded in memory", "2 model(s) in use", "50%/50% CPU/GPU", "100% GPU", "8,192", "never (kept loaded)",
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

func TestModelsPageLoad(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	body := get(h, "/models", false).Body.String()
	for _, want := range []string{`id="load-tile"`, `hx-get="/system/load"`, `id="running-panel"`, "2 model(s) in use"} {
		if !strings.Contains(body, want) {
			t.Errorf("models page missing %q", want)
		}
	}
	if tile := get(h, "/system/load", true).Body.String(); !strings.Contains(tile, "VRAM") {
		t.Errorf("load tile fragment wrong:\n%s", tile)
	}
	if panel := get(h, "/system/running", true).Body.String(); !strings.Contains(panel, "user/custom:v1") {
		t.Errorf("running panel fragment wrong:\n%s", panel)
	}
}
