package web

import (
	"net/http"
	"slices"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// unusedChoices are the Models page's "not used for" filter options, in days.
var unusedChoices = []int{30, 60, 90}

// dashboardUnusedDays is how long unused a model has to be for the
// dashboard's "reclaim space" note.
const dashboardUnusedDays = 60

// usageFacts is what's known about models' use, for telling which have gone unused.
type usageFacts struct {
	LastUsed map[string]time.Time
	Tracked  time.Time // when tracking began; nothing's known from before
}

func (s *Server) usageFacts(r *http.Request) usageFacts {
	f := usageFacts{LastUsed: s.lastUsed(r)}
	var err error
	if f.Tracked, err = s.st.UsageTrackedSince(r.Context()); err != nil {
		s.log.Warn("read when usage tracking began failed", "error", err)
		f.Tracked = time.Now() // so nothing's called unused on no evidence
	}
	return f
}

// idleSince is when a model was last known to be wanted: its last use (under
// any of its tags, since they're the same model), when it was downloaded, or
// when usage tracking began, whichever is latest.
func idleSince(m ollama.Model, u usageFacts, st *libraryStorage) time.Time {
	t := u.Tracked
	if m.ModifiedAt.After(t) {
		t = m.ModifiedAt
	}
	tags := []string{m.Name}
	if st != nil {
		tags = append(tags, st.SameAs(m.Name)...)
	}
	for _, name := range tags {
		if used, ok := u.LastUsed[name]; ok && used.After(t) {
			t = used
		}
	}
	return t
}

// unusedFor keeps the models that haven't been used for at least days.
func unusedFor(models []ollama.Model, days int, u usageFacts, st *libraryStorage, now time.Time) []ollama.Model {
	cutoff := now.AddDate(0, 0, -days)
	var out []ollama.Model
	for _, m := range models {
		if idleSince(m, u, st).Before(cutoff) {
			out = append(out, m)
		}
	}
	return out
}

// unusedSummary describes the models unused for Days: how many (tags of one
// model counting once) and the space deleting them all would free.
type unusedSummary struct {
	Days   int
	Models int
	Bytes  int64
	Since  time.Time // when usage tracking began
}

func summarizeUnused(all []ollama.Model, days int, u usageFacts, st *libraryStorage, now time.Time) unusedSummary {
	sum := unusedSummary{Days: days}
	unused := unusedFor(all, days, u, st, now)
	names := make([]string, len(unused))
	keys := map[string]bool{}
	for i, m := range unused {
		names[i] = m.Name
		keys[modelKey(m)] = true
	}
	sum.Models = len(keys)
	sum.Bytes = st.Freed(names...)
	sum.Since = u.Tracked
	return sum
}

// validUnused is days if it's one of unusedChoices, else 0 (no filter).
func validUnused(days int) int {
	if slices.Contains(unusedChoices, days) {
		return days
	}
	return 0
}
