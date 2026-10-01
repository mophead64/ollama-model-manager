package web

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// listState is everything that shapes the models table: filters, sort and
// page. Links it builds carry all of it, so sorting keeps the filters and
// paging keeps both.
type listState struct {
	Query  string
	Caps   []string
	Unused int    // only models not used for this many days (one of unusedChoices); 0 for all
	Sort   string // "name" or one of sortKeys
	Desc   bool
	Page   int
}

// The default order, which is left out of URLs: most recently used first.
const (
	defaultSort = "used"
	defaultDesc = true
)

// modelUsage is what's known about models' use here, for the Last used and
// Loads columns and sorting by them.
type modelUsage struct {
	LastUsed map[string]time.Time
	Loads    map[string]int // times seen loaded; absent if never tracked
}

// sortKeys maps each sortable column to the value it sorts on. ok=false
// means the value isn't known for that model; those always sort last,
// whichever direction is chosen.
var sortKeys = map[string]func(m ollama.Model, u modelUsage) (v float64, ok bool){
	"size": func(m ollama.Model, _ modelUsage) (float64, bool) { return float64(m.Size), true },
	"context": func(m ollama.Model, _ modelUsage) (float64, bool) {
		return float64(m.Details.ContextLength), m.Details.ContextLength > 0
	},
	"params": func(m ollama.Model, _ modelUsage) (float64, bool) {
		return parseParamSize(m.Details.ParameterSize)
	},
	"used": func(m ollama.Model, u modelUsage) (float64, bool) {
		t, ok := u.LastUsed[m.Name]
		return float64(t.Unix()), ok
	},
	"loads": func(m ollama.Model, u modelUsage) (float64, bool) {
		n, ok := u.Loads[m.Name]
		return float64(n), ok
	},
}

func parseListState(q url.Values) listState {
	st := listState{
		Query: strings.TrimSpace(q.Get("q")),
		Caps:  q["cap"],
		Page:  1,
		Sort:  defaultSort,
		Desc:  defaultDesc,
	}
	if key := q.Get("sort"); key == "name" || sortKeys[key] != nil {
		st.Sort, st.Desc = key, q.Get("dir") == "desc"
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		st.Page = p
	}
	st.Unused, _ = strconv.Atoi(q.Get("unused"))
	st.Unused = validUnused(st.Unused)
	return st
}

// isDefaultSort reports whether the list is in the default order.
func (st listState) isDefaultSort() bool {
	return st.Sort == defaultSort && st.Desc == defaultDesc
}

func (st listState) Dir() string {
	if st.Desc {
		return "desc"
	}
	return "asc"
}

// URL links to the models list with this state.
func (st listState) URL() string {
	v := url.Values{}
	if st.Query != "" {
		v.Set("q", st.Query)
	}
	for _, c := range st.Caps {
		v.Add("cap", c)
	}
	if st.Unused > 0 {
		v.Set("unused", strconv.Itoa(st.Unused))
	}
	if !st.isDefaultSort() {
		v.Set("sort", st.Sort)
		v.Set("dir", st.Dir())
	}
	if st.Page > 1 {
		v.Set("page", strconv.Itoa(st.Page))
	}
	if len(v) == 0 {
		return "/models"
	}
	return "/models?" + v.Encode()
}

type sortHeader struct {
	URL   string
	Arrow string // "▲"/"▼" on the active column, "" otherwise
	Aria  string // aria-sort value
}

// headers returns a link per sortable column ("name" plus sortKeys). Clicking
// the active column flips its direction; clicking another starts it
// descending (biggest or most recent first), or ascending for name. Either
// way it goes back to page 1.
func (st listState) headers() map[string]sortHeader {
	out := map[string]sortHeader{}
	for _, key := range []string{"name", "size", "context", "params", "used", "loads"} {
		active := st.Sort == key
		next := st
		next.Page = 1
		next.Sort = key
		h := sortHeader{Aria: "none"}
		if active {
			next.Desc = !st.Desc
			h.Arrow, h.Aria = "▲", "ascending"
			if st.Desc {
				h.Arrow, h.Aria = "▼", "descending"
			}
		} else {
			next.Desc = key != "name"
		}
		h.URL = next.URL()
		out[key] = h
	}
	return out
}

func sortModels(models []ollama.Model, key string, desc bool, u modelUsage) {
	byName := func(a, b ollama.Model) int { return cmp.Compare(a.Name, b.Name) }
	val, numeric := sortKeys[key]
	slices.SortStableFunc(models, func(a, b ollama.Model) int {
		if !numeric {
			if desc {
				return byName(b, a)
			}
			return byName(a, b)
		}
		av, aok := val(a, u)
		bv, bok := val(b, u)
		switch {
		case aok != bok: // unknown values last
			if aok {
				return -1
			}
			return 1
		case av != bv:
			if desc {
				return cmp.Compare(bv, av)
			}
			return cmp.Compare(av, bv)
		default:
			return byName(a, b)
		}
	})
}

// parseParamSize turns Ollama's parameter_size ("9.7B", "137M", "1.5T") into
// a count, so "137M" sorts below "3B".
func parseParamSize(s string) (float64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if s == "" {
		return 0, false
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'K':
		mult = 1e3
	case 'M':
		mult = 1e6
	case 'B':
		mult = 1e9
	case 'T':
		mult = 1e12
	}
	if mult != 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return n * mult, true
}
