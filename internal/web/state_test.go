package web

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

var seenRE = regexp.MustCompile(`hx-get="/state\?seen=([^"]*)"`)

func TestStateWatcher(t *testing.T) {
	srv := fakeOllama(t, 1)
	h := newTestServer(t, srv.URL)

	// Pages start the watcher with no fingerprint; its first poll takes stock.
	page := get(h, "/models", false).Body.String()
	if m := seenRE.FindStringSubmatch(page); m == nil || m[1] != "" || !strings.Contains(page, `hx-trigger="load"`) {
		t.Fatalf("page should start an unprimed watcher:\n%s", page)
	}
	rec := get(h, "/state", true)
	seen := seenRE.FindStringSubmatch(rec.Body.String())
	if seen == nil || seen[1] == "" || rec.Header().Get("HX-Trigger") != "" {
		t.Fatalf("first poll should return a fingerprint without triggering: %q %s", rec.Header().Get("HX-Trigger"), rec.Body)
	}

	// Nothing changed: no refresh.
	if rec := get(h, "/state?seen="+seen[1], true); rec.Header().Get("HX-Trigger") != "" {
		t.Error("unchanged state shouldn't trigger a refresh")
	}

	// A model used since: the views are told to refresh.
	testStore.MarkModelUsed(t.Context(), "model-000:latest", time.Now())
	rec = get(h, "/state?seen="+seen[1], true)
	next := seenRE.FindStringSubmatch(rec.Body.String())
	if rec.Header().Get("HX-Trigger") != "state-changed" || next == nil || next[1] == seen[1] {
		t.Errorf("changed state should trigger and carry the new fingerprint: %q %s", rec.Header().Get("HX-Trigger"), rec.Body)
	}

	// Ollama unreachable: keep the page's fingerprint rather than refreshing.
	srv.Close()
	rec = get(h, "/state?seen="+next[1], true)
	if rec.Header().Get("HX-Trigger") != "" || !strings.Contains(rec.Body.String(), "seen="+next[1]) {
		t.Errorf("an outage shouldn't refresh the page: %q %s", rec.Header().Get("HX-Trigger"), rec.Body)
	}
}

func TestViewsFollowState(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	list := get(h, "/models?q=custom&sort=size&dir=asc", true).Body.String()
	if !strings.Contains(list, `hx-get="/models?dir=asc&amp;q=custom&amp;sort=size" hx-trigger="state-changed from:body"`) {
		t.Errorf("models table should refresh itself, keeping its filters:\n%s", list)
	}
	detail := get(h, "/models/user/custom:v1", false).Body.String()
	if !strings.Contains(detail, `id="model-memory"`) || !strings.Contains(detail, "state-changed from:body") {
		t.Error("detail page's memory strip should follow state changes")
	}
	frag := do(h, "GET", "/models/user/custom:v1", nil, testSession, "HX-Request", "true", "HX-Target", "model-memory").Body.String()
	if !strings.HasPrefix(strings.TrimSpace(frag), `<div id="model-memory"`) || !strings.Contains(frag, "50%/50% CPU/GPU") {
		t.Errorf("memory strip fragment wrong:\n%s", frag)
	}
}
