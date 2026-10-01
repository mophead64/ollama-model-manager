package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/disk"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

const modelsPageSize = 25

// handleModels lists the models Ollama has locally. Ollama has no server-side
// paging or filtering, so the whole list is fetched and paged here; even large
// libraries are a few hundred entries at most.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	st := parseListState(r.URL.Query())

	data := map[string]any{
		"Query":         st.Query,
		"Caps":          st.Caps,
		"Unused":        st.Unused,
		"UnusedChoices": unusedChoices,
		"Dir":           st.Dir(),
		"Page":          st.Page,
		"TotalPages":    1,
		"OllamaURL":     s.ol.BaseURL(),
		"Deleted":       r.URL.Query().Get("deleted"),
		"Blacklisted":   r.URL.Query().Get("blacklisted") == "1",
	}

	if !st.isDefaultSort() {
		data["Sort"] = st.Sort // kept by the filter form; the default is left out
	}

	all, err := s.ol.List(r.Context())
	if err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
	} else {
		facts := s.usageFacts(r)
		lastUsed := facts.LastUsed
		storage := s.storage(all)
		data["Storage"] = storage
		models := filterModels(all, st.Query, st.Caps)
		if st.Unused > 0 {
			now := time.Now()
			models = unusedFor(models, st.Unused, facts, storage, now)
			data["UnusedSummary"] = summarizeUnused(models, st.Unused, facts, storage, now)
		}
		totals, err := s.st.ModelTotals(r.Context())
		if err != nil {
			s.log.Warn("read model usage totals failed", "error", err) // only costs the column
		}
		loads := make(map[string]int, len(totals))
		for name, t := range totals {
			loads[name] = t.Loads
		}
		sortModels(models, st.Sort, st.Desc, modelUsage{LastUsed: lastUsed, Loads: loads})
		data["LastUsed"] = lastUsed
		data["Loads"] = loads
		// Every model the filter matches, on any page, for "select all".
		matching := make([]string, len(models))
		for i, m := range models {
			matching[i] = m.Name
		}
		data["Matching"] = matching
		dupes := duplicateQuants(all, facts, totals, storage)
		data["Dupes"] = dupes
		data["OtherQuants"] = otherQuants(dupes)

		totalPages := max(1, (len(models)+modelsPageSize-1)/modelsPageSize)
		st.Page = min(st.Page, totalPages)
		start := (st.Page - 1) * modelsPageSize
		end := min(start+modelsPageSize, len(models))

		data["Models"] = models[start:end]
		data["Total"] = len(models)
		data["Page"] = st.Page
		data["TotalPages"] = totalPages
		data["AllCaps"] = capabilityOptions(all, st.Caps)
		data["Headers"] = st.headers()
		data["ListURL"] = st.URL()
		running := s.addOverview(r, all, data)
		loaded := make(map[string]bool, len(running))
		for _, m := range running {
			loaded[m.Name] = true
		}
		data["LoadedNames"] = loaded // rows offer Unload instead of Load
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

// addModelsOverview adds the overview to a models tab that doesn't list the
// models itself. If Ollama can't be reached, Error hides it.
func (s *Server) addModelsOverview(r *http.Request, data map[string]any) {
	all, err := s.ol.List(r.Context())
	if err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
		return
	}
	s.addOverview(r, all, data)
}

// lastUsed is when each model was last seen in use (see package usage). A
// failure only costs the column, so it's logged rather than shown.
func (s *Server) lastUsed(r *http.Request) map[string]time.Time {
	m, err := s.st.ModelsLastUsed(r.Context())
	if err != nil {
		s.log.Warn("read model usage failed", "error", err)
	}
	return m
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

// blacklistReasonMax caps a blacklist reason, in characters.
const blacklistReasonMax = 2000

// handleDeleteModel removes a model from Ollama, then goes back to the list
// (with the filters/sort/page it was deleted from, if it came from there) or
// the dashboard. With blacklist=on it also adds the model to the blacklist,
// with the form's reason and a copy of its details, which go with it.
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
	blacklist := r.FormValue("blacklist") == "on"
	reason, err := blacklistReason(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	var entry store.BlacklistEntry
	if blacklist {
		all, err := s.ol.List(r.Context())
		if err != nil {
			s.log.Warn("list models for blacklist entry failed", "model", name, "error", err)
		}
		entry = s.blacklistEntry(r, all, name, reason) // before its details are gone
	}

	if err := s.deleteModel(r.Context(), name); err != nil {
		s.log.Error("delete model failed", "model", name, "error", err)
		w.WriteHeader(http.StatusBadGateway)
		s.render(w, r, "model_detail.html", map[string]any{
			"Name":  name,
			"Error": fmt.Sprintf("Couldn't delete %s: %v", name, err),
		})
		return
	}
	s.log.Info("model deleted", "model", name, "by", currentUser(r).Username)
	if blacklist {
		if err := s.st.BlacklistModel(r.Context(), entry); err != nil {
			s.log.Error("blacklist model failed", "model", name, "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			s.render(w, r, "model_detail.html", map[string]any{
				"Name":  name,
				"Error": fmt.Sprintf("Deleted %s, but couldn't add it to the blacklist: %v", name, err),
			})
			return
		}
		s.log.Info("model blacklisted", "model", name, "by", entry.By)
	}

	s.afterDelete(w, r, name, blacklist, "")
}

// deleteModel removes a model from Ollama. One that's already gone counts
// as deleted: that's the outcome wanted.
func (s *Server) deleteModel(ctx context.Context, name string) error {
	err := s.ol.Delete(ctx, name)
	var se *ollama.StatusError
	if errors.As(err, &se) && se.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

// afterDelete goes back to the list (with the filters/sort/page it was
// deleted from, if it came from there) or the dashboard, with a notice of
// what was deleted, and of what couldn't be (failed).
func (s *Server) afterDelete(w http.ResponseWriter, r *http.Request, deleted string, blacklisted bool, failed string) {
	back := parseListState(nil).URL()
	if ret, err := url.Parse(r.FormValue("return")); err == nil {
		switch ret.Path {
		case "/models":
			back = parseListState(ret.Query()).URL()
		case "/": // the dashboard
			back = "/"
		}
	}
	target, _ := url.Parse(back)
	q := target.Query()
	if deleted != "" {
		q.Set("deleted", deleted)
	}
	if blacklisted {
		q.Set("blacklisted", "1")
	}
	if failed != "" {
		q.Set("memerror", failed)
	}
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) handleModelDetail(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	if r.Header.Get("HX-Target") == "model-memory" {
		data := map[string]any{"Name": name}
		s.addMemoryState(r, name, data)
		s.render(w, r, "model_memory", data)
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
		s.addMemoryState(r, name, data)
		data["CanChat"] = canChat(info.Capabilities)
		data["Usage"] = s.usageHistory(r, name)
		// /api/show doesn't report size or digest; pick them up from the list.
		if all, err := s.ol.List(r.Context()); err == nil {
			for _, m := range all {
				if m.Name == name || m.Model == name {
					data["Model"] = m
					st := s.storage(all)
					data["DeleteEffect"] = st.DeleteEffect(m.Name)
					data["SameAs"] = st.SameAs(m.Name)
					break
				}
			}
		}
	}
	s.render(w, r, "model_detail.html", data)
}

// addMemoryState adds whether a model is loaded ("Loaded") and when it was
// last used ("LastUsedAt") to a detail page's data.
func (s *Server) addMemoryState(r *http.Request, name string, data map[string]any) {
	if running, err := s.ol.Running(r.Context()); err == nil {
		for _, m := range running {
			if m.Name == name || m.Model == name {
				data["Loaded"] = m
				break
			}
		}
	}
	if t, ok := s.lastUsed(r)[name]; ok {
		data["LastUsedAt"] = t
	}
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

// blacklistReason is the form's reason for blacklisting, trimmed.
func blacklistReason(r *http.Request) (string, error) {
	reason := strings.TrimSpace(r.FormValue("reason"))
	if len([]rune(reason)) > blacklistReasonMax {
		return "", errMsg(fmt.Sprintf("the reason can be at most %d characters", blacklistReasonMax))
	}
	return reason, nil
}

// blacklistEntry describes the model for its blacklist entry, from all (the
// models Ollama lists). If it isn't there, the entry just has its name.
func (s *Server) blacklistEntry(r *http.Request, all []ollama.Model, name, reason string) store.BlacklistEntry {
	e := store.BlacklistEntry{Model: name, Reason: reason, By: currentUser(r).Username, At: time.Now()}
	for _, m := range all {
		if m.Name == name || m.Model == name {
			e.Family, e.ParameterSize, e.Quantization = m.Details.Family, m.Details.ParameterSize, m.Details.QuantizationLevel
			e.Size, e.Digest = m.Size, m.Digest
			break
		}
	}
	return e
}

// handleBlacklist is the models section's Blacklist tab.
func (s *Server) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data := map[string]any{"Removed": q.Get("removed"), "Updated": q.Get("updated")}
	s.addModelsOverview(r, data)
	entries, err := s.st.Blacklist(r.Context())
	if err != nil {
		s.log.Error("read blacklist failed", "error", err)
		data["BlacklistErr"] = err.Error()
	}
	data["Entries"] = entries
	s.render(w, r, "blacklist.html", data)
}

// handleUnblacklist takes a model off the blacklist.
func (s *Server) handleUnblacklist(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	if err := s.st.UnblacklistModel(r.Context(), name); err != nil {
		s.log.Error("unblacklist model failed", "model", name, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": fmt.Sprintf("Couldn't remove %s from the blacklist: %v", name, err)})
		return
	}
	s.log.Info("model unblacklisted", "model", name, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/blacklist?removed="+url.QueryEscape(name), http.StatusSeeOther)
}

// handleBlacklistReason changes why a model is blacklisted (the Edit dialog).
func (s *Server) handleBlacklistReason(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	reason, err := blacklistReason(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	ok, err := s.st.UpdateBlacklistReason(r.Context(), name, reason)
	if err != nil {
		s.log.Error("update blacklist reason failed", "model", name, "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": fmt.Sprintf("Couldn't update %s's reason: %v", name, err)})
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": fmt.Sprintf("%s isn't on the blacklist.", name)})
		return
	}
	s.log.Info("blacklist reason updated", "model", name, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/blacklist?updated="+url.QueryEscape(name), http.StatusSeeOther)
}

// usageDays is how far back a model's page charts its use.
const usageDays = 30

// usageDay is one day of a model's use, for its page's chart.
type usageDay struct {
	Date          time.Time
	ActiveSeconds int
	Loads         int
	Height        float64 // the bar's height, as a percentage of the busiest day
}

// usageHistory is a model's use over the last usageDays days (every day,
// oldest first, so the chart has a gap where it wasn't used) and in all.
type usageHistory struct {
	Days        []usageDay
	DaysUsed    int // in those days
	Active      int // seconds, in those days
	Loads       int // in those days
	TotalActive int // seconds, ever
	TotalLoads  int
	Since       time.Time // the first day any use was recorded; zero if none
}

func (s *Server) usageHistory(r *http.Request, name string) *usageHistory {
	today := time.Now()
	start := today.AddDate(0, 0, -(usageDays - 1))
	rows, err := s.st.ModelDailyUsage(r.Context(), name, start.Format(time.DateOnly))
	if err != nil {
		s.log.Warn("read model usage history failed", "model", name, "error", err)
		return nil
	}
	h := &usageHistory{}
	byDay := map[string]store.DailyUsage{}
	for _, d := range rows {
		byDay[d.Day] = d
	}
	busiest := 0
	for i := range usageDays {
		date := start.AddDate(0, 0, i)
		d := byDay[date.Format(time.DateOnly)]
		h.Days = append(h.Days, usageDay{Date: date, ActiveSeconds: d.ActiveSeconds, Loads: d.Loads})
		busiest = max(busiest, d.ActiveSeconds)
		if d.ActiveSeconds > 0 || d.Loads > 0 {
			h.DaysUsed++
		}
		h.Active += d.ActiveSeconds
		h.Loads += d.Loads
	}
	for i := range h.Days {
		if busiest > 0 {
			h.Days[i].Height = 100 * float64(h.Days[i].ActiveSeconds) / float64(busiest)
		}
	}
	var since string
	h.TotalActive, h.TotalLoads, since, err = s.st.ModelUsageTotals(r.Context(), name)
	if err != nil {
		s.log.Warn("read model usage totals failed", "model", name, "error", err)
	}
	if t, err := time.ParseInLocation(time.DateOnly, since, time.Local); err == nil {
		h.Since = t
	}
	return h
}
