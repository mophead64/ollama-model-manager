package web

import (
	"context"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/version"
)

const (
	latestOllamaURL = "https://api.github.com/repos/ollama/ollama/releases/latest"
	// How often the background check runs: Ollama's releases aren't that
	// frequent. The System page also asks Ollama's version whenever it's
	// opened, so the nav's dot clears as soon as someone looks after updating.
	ollamaCheckEvery = 6 * time.Hour
)

// ollamaUpdate is whether the Ollama server has a newer release to update to.
type ollamaUpdate struct {
	Running   string        // Ollama's version, e.g. "0.12.3"; "" if it couldn't be asked
	Latest    string        // the latest release's tag, e.g. "v0.12.5"; "" if GitHub couldn't be asked
	URL       string        // its release notes
	Notes     template.HTML // them, rendered from Markdown
	Published time.Time
	Available bool // Latest is newer than Running
	Current   bool // Running is the latest release
	CheckedAt time.Time
}

// Checked reports whether both versions are known, so the comparison means something.
func (u ollamaUpdate) Checked() bool { return u.Running != "" && u.Latest != "" }

// compare sets Available and Current from the two versions.
func (u *ollamaUpdate) compare() {
	u.Available, u.Current = false, false
	if u.Checked() {
		u.Available = version.Newer(u.Latest, u.Running)
		u.Current = !u.Available && strings.TrimPrefix(u.Latest, "v") == strings.TrimPrefix(u.Running, "v")
	}
}

// withRunning is the last check with Ollama's version asked now (it's local,
// so quick), for the System page, and kept, so an Ollama that's just been
// updated clears the nav's dot. GitHub's answer is left to the background.
func (s *Server) withRunning(ctx context.Context) ollamaUpdate {
	v, err := s.ol.Version(ctx)
	s.ollamaUp.mu.Lock()
	defer s.ollamaUp.mu.Unlock()
	if err == nil {
		s.ollamaUp.last.Running = strings.TrimSpace(v)
		s.ollamaUp.last.compare()
	}
	return s.ollamaUp.last
}

// ollamaUpdates keeps track of ollamaUpdate in the background (RunOllamaUpdateChecks),
// so the nav can show it on every page without waiting on GitHub.
type ollamaUpdates struct {
	releases *updateChecker

	mu   sync.Mutex
	last ollamaUpdate
}

func newOllamaUpdates() *ollamaUpdates {
	u := newUpdateChecker()
	u.url, u.current, u.ttl = latestOllamaURL, nil, ollamaCheckEvery
	return &ollamaUpdates{releases: u}
}

// latest is the last check's result, without checking.
func (o *ollamaUpdates) latest() ollamaUpdate {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.last
}

// refresh asks Ollama for its version, and GitHub for the latest release
// (from the cache unless force), and records how they compare.
func (s *Server) refreshOllamaUpdate(ctx context.Context, force bool) ollamaUpdate {
	u := ollamaUpdate{CheckedAt: time.Now()}
	if v, err := s.ol.Version(ctx); err == nil {
		u.Running = strings.TrimSpace(v)
	}
	if rel := s.ollamaUp.releases.check(ctx, force); rel.Checked {
		u.Latest, u.URL, u.Notes, u.Published = rel.Latest, rel.URL, rel.Notes, rel.Published
	}
	u.compare()
	s.ollamaUp.mu.Lock()
	s.ollamaUp.last = u
	s.ollamaUp.mu.Unlock()
	return u
}

// RunOllamaUpdateChecks keeps the Ollama update check fresh until ctx is
// cancelled, checking every ollamaCheckEvery.
func (s *Server) RunOllamaUpdateChecks(ctx context.Context) {
	tick := time.NewTicker(ollamaCheckEvery)
	defer tick.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		s.refreshOllamaUpdate(cctx, false)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// handleOllamaUpdate is the System page's Ollama panel, re-checked on demand
// (?force=1, its "check now").
func (s *Server) handleOllamaUpdate(w http.ResponseWriter, r *http.Request) {
	u := s.refreshOllamaUpdate(r.Context(), r.URL.Query().Get("force") != "")
	s.render(w, r, "ollama_panel", map[string]any{"OllamaURL": s.ol.BaseURL(), "OllamaUpdate": u, "Fragment": true})
}
