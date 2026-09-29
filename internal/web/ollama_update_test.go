package web

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOllamaUpdate(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL) // Ollama 0.12.3

	// Nothing known yet: no dot, and the page says it hasn't checked.
	if body := get(h, "/models", false).Body.String(); strings.Contains(body, `<span class="nav-dot`) {
		t.Error("no dot before a check")
	}

	// A newer release.
	testServer.ollamaUp.releases.url = fakeGitHub(t, "v0.12.5", "")
	testServer.refreshOllamaUpdate(context.Background(), true)
	if body := get(h, "/models", false).Body.String(); !strings.Contains(body, `<span class="nav-dot warn" role="img" aria-label="Ollama update available" title="Ollama v0.12.5 is available (running 0.12.3)">`) {
		t.Error("the nav's System link should have an orange dot")
	}
	page := get(h, "/system", false).Body.String()
	for _, want := range []string{
		`<span class="mono">0.12.3</span>`, "Update available: v0.12.5", "Ollama <strong>v0.12.5</strong> is out",
		`hx-get="/system/ollama" hx-trigger="load"`, "check now",
		`value="docker compose pull ollama &amp;&amp; docker compose up -d ollama"`, `value="docker pull ollama/ollama:latest"`,
		`value="curl -fsSL https://ollama.com/install.sh | sh"`,
		"<strong>New models need it.</strong>", "Hugging Face", "Speed, memory use and GPU support",
		"Back up your service settings first.", `value="sudo cp /etc/systemd/system/ollama.service ~/ollama.service.bak"`,
		"sudo systemctl edit ollama", "brew upgrade ollama",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("system page missing %q", want)
		}
	}
	if strings.Contains(page, `<details class="ollama-how">`) {
		t.Error("with an update out, the steps should be shown, not folded away")
	}

	// Up to date: a green tick, no dot, the steps folded away.
	testServer.ollamaUp.releases.url = fakeGitHub(t, "v0.12.3", "")
	testServer.ollamaUp.releases.expiry = time.Time{} // as if the cache had run out ("check now" is rate limited)
	frag := get(h, "/system/ollama?force=1", true).Body.String()
	for _, want := range []string{`<span class="badge on">✓ Up to date</span>`, `<details class="ollama-how"><summary>How to update Ollama</summary>`, "Checked"} {
		if !strings.Contains(frag, want) {
			t.Errorf("up to date panel missing %q", want)
		}
	}
	if strings.Contains(frag, `hx-trigger="load"`) {
		t.Error("the refreshed panel shouldn't refresh itself again")
	}
	if body := get(h, "/models", false).Body.String(); strings.Contains(body, `<span class="nav-dot`) {
		t.Error("no dot once up to date")
	}

	// Updated since the last check: opening the System page asks Ollama, and the dot clears.
	testServer.ollamaUp.mu.Lock()
	testServer.ollamaUp.last = ollamaUpdate{Running: "0.12.1", Latest: "v0.12.3", Available: true}
	testServer.ollamaUp.mu.Unlock()
	if body := get(h, "/models", false).Body.String(); !strings.Contains(body, `<span class="nav-dot`) {
		t.Fatal("dot expected while the last check saw an older Ollama")
	}
	get(h, "/system", false) // Ollama now says 0.12.3
	if body := get(h, "/models", false).Body.String(); strings.Contains(body, `<span class="nav-dot`) {
		t.Error("opening the System page should clear the dot once Ollama's been updated")
	}
	if ollamaCheckEvery < 6*time.Hour {
		t.Errorf("background checks every %s; that's more often than needed", ollamaCheckEvery)
	}
}

func TestOllamaUpdateCompare(t *testing.T) {
	for _, c := range []struct {
		running, latest    string
		available, current bool
	}{
		{"0.12.3", "v0.12.5", true, false},
		{"0.12.3", "v0.12.3", false, true},
		{"0.13.0", "v0.12.5", false, false}, // a pre-release, ahead of the latest
		{"0.12.3-rc1", "v0.12.5", false, false},
		{"", "v0.12.5", false, false}, // Ollama unreachable
		{"0.12.3", "", false, false},  // GitHub unreachable
	} {
		u := ollamaUpdate{Running: c.running, Latest: c.latest}
		u.compare()
		if u.Available != c.available || u.Current != c.current {
			t.Errorf("%q vs %q: available %v current %v, want %v %v", c.running, c.latest, u.Available, u.Current, c.available, c.current)
		}
	}
}
