package web

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"net/http"
	"slices"
	"strconv"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// Pages keep up with changes made elsewhere (a model loaded by a chat client,
// unloaded when idle, used...) through a small watcher in the footer. It polls
// /state with a fingerprint of the model state it last saw; when the current
// fingerprint differs, the response carries an HX-Trigger header, which fires
// a "state-changed" event that the page's views listen for to re-fetch
// themselves. Nothing is re-rendered while nothing changes.

// stateFingerprint summarises what the views depend on: which models are
// loaded, how they're split between CPU and GPU, and when each model was
// last used.
func (s *Server) stateFingerprint(ctx context.Context) (string, error) {
	running, err := s.ol.Running(ctx)
	if err != nil {
		return "", err
	}
	h := fnv.New64a()
	slices.SortFunc(running, func(a, b ollama.RunningModel) int { return cmp.Compare(a.Name, b.Name) })
	for _, m := range running {
		fmt.Fprintf(h, "run %s %d;", m.Name, m.SizeVRAM)
	}
	used, err := s.st.ModelsLastUsed(ctx)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Fprintf(h, "used %s %d;", n, used[n].Unix())
	}
	return strconv.FormatUint(h.Sum64(), 36), nil
}

// handleState answers the footer's watcher. The first poll from a page has
// no fingerprint and only establishes one. If Ollama can't be reached the
// page's fingerprint is kept, so a blip doesn't refresh everything.
func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	seen := r.URL.Query().Get("seen")
	now, err := s.stateFingerprint(r.Context())
	if err != nil {
		now = seen
	}
	if seen != "" && now != seen {
		w.Header().Set("HX-Trigger", "state-changed")
	}
	s.render(w, r, "state_watch", map[string]any{"Seen": now})
}
