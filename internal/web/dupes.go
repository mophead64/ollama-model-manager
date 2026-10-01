package web

import (
	"cmp"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// A model installed in more than one quantisation (qwen3:8b at Q4_K_M and
// qwen3:8b-q8_0) is usually a comparison that's been made, or meant to be;
// the Models page lists them side by side with their use, so the one that's
// wanted is easy to keep and the rest easy to delete.

// quantEntry is one installed quantisation of a model.
type quantEntry struct {
	ollama.Model
	Also     []string // other tags of the same model (same digest)
	LastUsed *time.Time
	Usage    store.ModelTotal // under any of its tags
	Frees    int64            // deleting it alone
}

// quantGroup is a model installed in several quantisations.
type quantGroup struct {
	Name    string // the model without a tag, e.g. "qwen3" or "hf.co/owner/Qwen3-8B-GGUF"
	Params  string // e.g. "8.2B"
	Quants  []quantEntry
	TestURL string // a test with them all ticked
}

// quantGroupKey is what makes two tags the same model at different
// quantisations: the same repo (ignoring the tag) and parameter count.
func quantGroupKey(m ollama.Model) (name, key string, ok bool) {
	ref, err := ollama.ParseName(m.Name)
	if err != nil || m.Details.ParameterSize == "" {
		return "", "", false
	}
	name = strings.TrimSuffix(ref.String(), ":"+ref.Tag)
	return name, strings.ToLower(name + "|" + m.Details.ParameterSize), true
}

// duplicateQuants finds the models installed in more than one quantisation.
// Tags of the same model (same digest) are one entry.
func duplicateQuants(all []ollama.Model, u usageFacts, totals map[string]store.ModelTotal, st *libraryStorage) []quantGroup {
	groups := map[string]*quantGroup{}
	var order []string
	seen := map[string]bool{} // model keys already in a group
	for _, m := range all {
		name, key, ok := quantGroupKey(m)
		if !ok || seen[modelKey(m)] {
			continue
		}
		seen[modelKey(m)] = true
		g := groups[key]
		if g == nil {
			g = &quantGroup{Name: name, Params: m.Details.ParameterSize}
			groups[key] = g
			order = append(order, key)
		}
		e := quantEntry{Model: m, Also: st.SameAs(m.Name), Frees: st.Freed(append([]string{m.Name}, st.SameAs(m.Name)...)...)}
		for _, tag := range append([]string{m.Name}, e.Also...) {
			if t, ok := u.LastUsed[tag]; ok && (e.LastUsed == nil || t.After(*e.LastUsed)) {
				e.LastUsed = &t
			}
			e.Usage.ActiveSeconds += totals[tag].ActiveSeconds
			e.Usage.Loads += totals[tag].Loads
		}
		g.Quants = append(g.Quants, e)
	}

	var out []quantGroup
	for _, key := range order {
		g := groups[key]
		levels := map[string]bool{}
		for _, q := range g.Quants {
			levels[strings.ToUpper(q.Details.QuantizationLevel)] = true
		}
		if len(g.Quants) < 2 || len(levels) < 2 {
			continue // one quantisation, perhaps with variants of it
		}
		slices.SortFunc(g.Quants, func(a, b quantEntry) int { return cmp.Compare(a.Size, b.Size) })
		v := url.Values{"name": {"Compare " + g.Name + " " + g.Params + " quantisations"}}
		for _, q := range g.Quants {
			if canChat(q.Capabilities) {
				v.Add("model", q.Name)
			}
		}
		if len(v["model"]) >= 2 && len(v["model"]) <= testMaxModels {
			g.TestURL = "/models/testing?" + v.Encode()
		}
		out = append(out, *g)
	}
	slices.SortFunc(out, func(a, b quantGroup) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// otherQuants maps each tag in a group to the group's other quantisations,
// for the models table's "+1 quant" badges.
func otherQuants(groups []quantGroup) map[string][]string {
	out := map[string][]string{}
	for _, g := range groups {
		for _, q := range g.Quants {
			for _, o := range g.Quants {
				if o.Name != q.Name {
					label := o.Name
					if o.Details.QuantizationLevel != "" {
						label += " (" + o.Details.QuantizationLevel + ")"
					}
					for _, tag := range append([]string{q.Name}, q.Also...) {
						out[tag] = append(out[tag], label)
					}
				}
			}
		}
	}
	return out
}
