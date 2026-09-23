package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/disk"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

const modelsPageSize = 25

// handleModels lists the models Ollama has locally. Ollama has no server-side
// paging or filtering, so the whole list is fetched and paged here; even large
// libraries are a few hundred entries at most.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	st := parseListState(r.URL.Query())

	data := map[string]any{
		"Query":      st.Query,
		"Caps":       st.Caps,
		"Sort":       st.Sort,
		"Dir":        st.Dir(),
		"Page":       st.Page,
		"TotalPages": 1,
		"OllamaURL":  s.ol.BaseURL(),
		"Deleted":    r.URL.Query().Get("deleted"),
	}

	all, err := s.ol.List(r.Context())
	if err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
	} else {
		models := filterModels(all, st.Query, st.Caps)
		sortModels(models, st.Sort, st.Desc)

		var totalBytes int64
		for _, m := range all {
			totalBytes += m.Size
		}
		totalPages := max(1, (len(models)+modelsPageSize-1)/modelsPageSize)
		st.Page = min(st.Page, totalPages)
		start := (st.Page - 1) * modelsPageSize
		end := min(start+modelsPageSize, len(models))

		data["Models"] = models[start:end]
		data["Total"] = len(models)
		data["Count"] = len(all)
		data["TotalBytes"] = totalBytes
		data["Page"] = st.Page
		data["TotalPages"] = totalPages
		data["AllCaps"] = capabilityOptions(all, st.Caps)
		data["Headers"] = st.headers()
		data["ListURL"] = st.URL()
		data["Disk"] = s.diskUsage()
		if st.Page > 1 {
			prev := st
			prev.Page--
			data["PrevURL"] = prev.URL()
		}
		if st.Page < totalPages {
			next := st
			next.Page++
			data["NextURL"] = next.URL()
		}
	}

	if r.Header.Get("HX-Request") == "true" {
		s.render(w, r, "models_results.html", data)
		return
	}
	s.render(w, r, "models.html", data)
}

// diskUsage reports free space where Ollama keeps its models, or nil when that
// directory isn't visible to this process (e.g. not mounted into the container).
func (s *Server) diskUsage() *disk.Usage {
	if s.cfg.ModelsDir == "" {
		return nil
	}
	u, err := disk.Stat(s.cfg.ModelsDir)
	if err != nil {
		s.log.Warn("disk usage unavailable", "dir", s.cfg.ModelsDir, "error", err)
		return nil
	}
	return &u
}

// filterModels keeps models whose name, family or a capability contains query
// (case-insensitively) and that have every capability in caps.
func filterModels(models []ollama.Model, query string, caps []string) []ollama.Model {
	query = strings.ToLower(query)
	var out []ollama.Model
	for _, m := range models {
		if query != "" &&
			!strings.Contains(strings.ToLower(m.Name), query) &&
			!strings.Contains(strings.ToLower(m.Details.Family), query) &&
			!slices.ContainsFunc(m.Capabilities, func(c string) bool {
				return strings.Contains(strings.ToLower(c), query)
			}) {
			continue
		}
		if !hasAll(m.Capabilities, caps) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func hasAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

type capOption struct {
	Name     string
	Selected bool
}

// capabilityOptions lists every capability any model reports, for the filter
// chips. A selected capability no model has is kept so it can be unticked.
func capabilityOptions(models []ollama.Model, selected []string) []capOption {
	seen := map[string]bool{}
	for _, m := range models {
		for _, c := range m.Capabilities {
			seen[c] = true
		}
	}
	for _, c := range selected {
		seen[c] = true
	}
	out := make([]capOption, 0, len(seen))
	for c := range seen {
		out = append(out, capOption{Name: c, Selected: slices.Contains(selected, c)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// handleDeleteModel removes a model from Ollama, then goes back to the list
// (with the filters/sort/page it was deleted from, if it came from there).
func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowDelete {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "Deleting models is disabled on this server (ALLOW_MODEL_DELETE=false)."})
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}

	err := s.ol.Delete(r.Context(), name)
	var se *ollama.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		err = nil // already gone; the outcome the user wanted
	}
	if err != nil {
		s.log.Error("delete model failed", "model", name, "error", err)
		w.WriteHeader(http.StatusBadGateway)
		s.render(w, r, "model_detail.html", map[string]any{
			"Name":  name,
			"Error": fmt.Sprintf("Couldn't delete %s: %v", name, err),
		})
		return
	}
	s.log.Info("model deleted", "model", name, "by", currentUser(r).Username)

	back := parseListState(nil)
	if ret, err := url.Parse(r.FormValue("return")); err == nil && ret.Path == "/models" {
		back = parseListState(ret.Query())
	}
	target, _ := url.Parse(back.URL())
	q := target.Query()
	q.Set("deleted", name)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) handleModelDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}

	data := map[string]any{"Name": name}

	info, err := s.ol.Show(r.Context(), name)
	var se *ollama.StatusError
	switch {
	case errors.As(err, &se) && se.StatusCode == http.StatusNotFound:
		w.WriteHeader(http.StatusNotFound)
		data["Error"] = fmt.Sprintf("Model %q isn't on this Ollama server.", name)
	case err != nil:
		s.log.Error("show model failed", "model", name, "error", err)
		data["Error"] = err.Error()
	default:
		data["Info"] = info
		data["Meta"] = flattenModelInfo(info.ModelInfo)
		// /api/show doesn't report size or digest; pick them up from the list.
		if all, err := s.ol.List(r.Context()); err == nil {
			for _, m := range all {
				if m.Name == name || m.Model == name {
					data["Model"] = m
					break
				}
			}
		}
	}
	s.render(w, r, "model_detail.html", data)
}

type metaEntry struct {
	Key   string
	Value string
}

// flattenModelInfo turns Ollama's raw GGUF metadata into sorted display rows.
// Arrays are summarised rather than printed: tokenizer vocabularies run to
// hundreds of thousands of entries (and Ollama sends them empty unless asked
// for verbose output anyway).
func flattenModelInfo(mi map[string]any) []metaEntry {
	out := make([]metaEntry, 0, len(mi))
	for k, v := range mi {
		out = append(out, metaEntry{Key: k, Value: metaValue(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func metaValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case []any:
		if len(x) == 0 {
			return "(list)"
		}
		if len(x) <= 16 {
			parts := make([]string, len(x))
			for i, e := range x {
				parts[i] = metaValue(e)
			}
			return strings.Join(parts, ", ")
		}
		return fmt.Sprintf("(%d items)", len(x))
	case float64:
		// JSON numbers decode as float64; print integers without an exponent.
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
