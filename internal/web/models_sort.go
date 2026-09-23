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
	Query string
	Caps  []string
	Sort  string // "name" or one of sortKeys; "" means name ascending
	Desc  bool
	Page  int
}

// sortKeys maps each sortable column to the value it sorts on. ok=false
// means the value isn't known for that model; those always sort last,
// whichever direction is chosen. lastUsed is when each model was last used.
var sortKeys = map[string]func(m ollama.Model, lastUsed map[string]time.Time) (v float64, ok bool){
	"size": func(m ollama.Model, _ map[string]time.Time) (float64, bool) { return float64(m.Size), true },
	"context": func(m ollama.Model, _ map[string]time.Time) (float64, bool) {
		return float64(m.Details.ContextLength), m.Details.ContextLength > 0
	},
	"params": func(m ollama.Model, _ map[string]time.Time) (float64, bool) {
		return parseParamSize(m.Details.ParameterSize)
	},
	"used": func(m ollama.Model, lastUsed map[string]time.Time) (float64, bool) {
		t, ok := lastUsed[m.Name]
		return float64(t.Unix()), ok
	},
}

func parseListState(q url.Values) listState {
	st := listState{
		Query: strings.TrimSpace(q.Get("q")),
		Caps:  q["cap"],
		Page:  1,
		Desc:  q.Get("dir") == "desc",
	}
	if _, ok := sortKeys[q.Get("sort")]; ok || (q.Get("sort") == "name" && st.Desc) {
		st.Sort = q.Get("sort")
	} else {
		st.Desc = false // name ascending, the default, is left out of URLs
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 0 {
		st.Page = p
	}
	return st
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
	if st.Sort != "" {
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
	for _, key := range []string{"name", "size", "context", "params", "used"} {
		active := st.Sort == key || (key == "name" && st.Sort == "")
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
		if key == "name" && !next.Desc {
			next.Sort = ""
		}
		h.URL = next.URL()
		out[key] = h
	}
	return out
}

func sortModels(models []ollama.Model, key string, desc bool, lastUsed map[string]time.Time) {
	byName := func(a, b ollama.Model) int { return cmp.Compare(a.Name, b.Name) }
	val, numeric := sortKeys[key]
	slices.SortStableFunc(models, func(a, b ollama.Model) int {
		if !numeric {
			if desc {
				return byName(b, a)
			}
			return byName(a, b)
		}
		av, aok := val(a, lastUsed)
		bv, bok := val(b, lastUsed)
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
