package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/library"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

// discoverState is the search on the discover page, as held in its URL.
type discoverState struct {
	Source string // "ollama" (the ollama.com library) or "hf" (Hugging Face)
	Query  string
	Caps   []string // ollama.com only
	Order  string   // "popular"/"newest" for ollama.com; a key of library.HFSorts for Hugging Face
	Fit    bool     // only models that should run on this machine; on unless fit=0
	Page   int      // ollama.com's page, 1-based
	Cursor string   // Hugging Face's position; "" for the first page
}

func parseDiscoverState(q url.Values) discoverState {
	// The form sends fit=0 from a hidden input, plus fit=1 when the box is
	// ticked, so a bare /discover (no fit at all) gets the default: on.
	fit := !q.Has("fit") || slices.Contains(q["fit"], "1")
	st := discoverState{Source: "ollama", Query: strings.TrimSpace(q.Get("q")), Order: "popular", Fit: fit, Page: 1}
	if q.Get("src") == "hf" {
		st.Source, st.Order, st.Cursor = "hf", "downloads", q.Get("cursor")
		if _, ok := library.HFSorts[q.Get("o")]; ok {
			st.Order = q.Get("o")
		}
		return st
	}
	for _, c := range q["c"] {
		if (c == "cloud" || slices.Contains(library.Capabilities, c)) && !slices.Contains(st.Caps, c) {
			st.Caps = append(st.Caps, c)
		}
	}
	if q.Get("o") == "newest" {
		st.Order = "newest"
	}
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 {
		st.Page = p
	}
	return st
}

func (st discoverState) HF() bool { return st.Source == "hf" }

// more reports whether this is a later page, fetched by infinite scroll.
func (st discoverState) more() bool { return st.Page > 1 || st.Cursor != "" }

func (st discoverState) URL() string {
	v := url.Values{}
	if st.HF() {
		v.Set("src", "hf")
	}
	if st.Query != "" {
		v.Set("q", st.Query)
	}
	for _, c := range st.Caps {
		v.Add("c", c)
	}
	if st.Order != "popular" && st.Order != "downloads" {
		v.Set("o", st.Order)
	}
	if !st.Fit {
		v.Set("fit", "0")
	}
	if st.Page > 1 {
		v.Set("page", strconv.Itoa(st.Page))
	}
	if st.Cursor != "" {
		v.Set("cursor", st.Cursor)
	}
	if len(v) == 0 {
		return "/discover"
	}
	return "/discover?" + v.Encode()
}

type sizeChip struct {
	Label string
	Fit   fit
}

// discoverCard is an ollama.com search result with what's known about it here.
type discoverCard struct {
	library.Model
	Sizes     []sizeChip
	Installed bool      // some tag of it is installed
	Active    *activeDL // some tag of it is queued or downloading
}

// runnable reports whether some size of the model should run here.
func (c discoverCard) runnable() bool {
	return slices.ContainsFunc(c.Sizes, func(s sizeChip) bool { return s.Fit.Runs() })
}

// hfCard is a Hugging Face search result with what's known about it here.
type hfCard struct {
	library.HFModel
	Size      *sizeChip // estimated from the parameter count; nil if unknown
	Installed bool
	Active    *activeDL
}

// activeDL is a queued or running download, for a status badge.
type activeDL struct {
	ID      int64
	Status  string  // store.DownloadQueued or store.DownloadDownloading
	Percent float64 // progress when downloading; -1 if unknown
}

// handleDiscover searches the ollama.com library or Hugging Face. htmx
// requests get just the results (after a filter change) or just the next
// page's cards (infinite scroll).
func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	st := parseDiscoverState(r.URL.Query())
	snap := s.sys.Latest()

	var caps []capOption
	for _, c := range append(slices.Clone(library.Capabilities), "cloud") {
		caps = append(caps, capOption{Name: c, Selected: slices.Contains(st.Caps, c)})
	}
	hw := hardwareSummary(snap)
	data := map[string]any{
		"State":    st,
		"Caps":     caps,
		"HFSorts":  []string{"downloads", "trending", "likes", "newest"},
		"Hardware": hw,
	}
	// Until the hardware's been read nothing is known to fit, so the filter
	// would hide everything; show all instead.
	filter := st.Fit && hw != ""
	local := s.localModels(r)

	var err error
	if st.HF() {
		err = s.discoverHF(r, st, filter, local, snap, data)
	} else {
		err = s.discoverOllama(r, st, filter, local, snap, data)
	}
	if err != nil {
		s.log.Error("model search failed", "source", st.Source, "error", err)
		data["Error"] = err.Error()
	}

	switch {
	case r.Header.Get("HX-Request") != "true":
		s.render(w, r, "discover.html", data)
	case st.more():
		s.render(w, r, "discover_cards", data)
	default:
		s.render(w, r, "discover_results", data)
	}
}

func (s *Server) discoverOllama(r *http.Request, st discoverState, filter bool, local localModels, snap sysinfo.Snapshot, data map[string]any) error {
	page, err := s.lib.Search(r.Context(), library.Query{Text: st.Query, Caps: st.Caps, Order: st.Order, Page: st.Page})
	if err != nil {
		return err
	}
	cards := make([]discoverCard, 0, len(page.Models))
	for _, m := range page.Models {
		c := discoverCard{Model: m, Installed: local.installed[m.Name], Active: local.active[m.Name]}
		for _, label := range m.Sizes {
			c.Sizes = append(c.Sizes, sizeChip{Label: label, Fit: sizeFit(label, snap)})
		}
		// Cloud models don't run here, but someone filtering for them wants them.
		wantCloud := c.Cloud && slices.Contains(st.Caps, "cloud")
		if filter && !c.runnable() && !wantCloud {
			continue
		}
		cards = append(cards, c)
	}
	data["Cards"] = cards
	data["Hidden"] = len(page.Models) - len(cards)
	if page.NextPage > 0 {
		next := st
		next.Page = page.NextPage
		data["NextURL"] = next.URL()
	}
	return nil
}

func (s *Server) discoverHF(r *http.Request, st discoverState, filter bool, local localModels, snap sysinfo.Snapshot, data map[string]any) error {
	page, err := s.lib.HFSearch(r.Context(), library.HFQuery{Text: st.Query, Sort: st.Order, Cursor: st.Cursor})
	if err != nil {
		return err
	}
	cards := make([]hfCard, 0, len(page.Models))
	for _, m := range page.Models {
		key := hfKey(m.Repo)
		c := hfCard{HFModel: m, Installed: local.installed[key], Active: local.active[key]}
		if m.Params > 0 {
			f := estimateFit(int64(float64(m.Params)*bytesPerParam), snap)
			if f.Level != "unknown" {
				f.Detail = "Estimated for a 4-bit quantisation. " + f.Detail
			}
			c.Size = &sizeChip{Label: formatParams(m.Params), Fit: f}
		}
		if filter && (c.Size == nil || !c.Size.Fit.Runs()) {
			continue
		}
		cards = append(cards, c)
	}
	data["HFCards"] = cards
	data["Hidden"] = len(page.Models) - len(cards)
	if page.NextCursor != "" {
		next := st
		next.Cursor = page.NextCursor
		data["NextURL"] = next.URL()
	}
	return nil
}

// tagRow is a downloadable tag (or Hugging Face file) with its state here.
type tagRow struct {
	library.Tag
	Label     string // what the row shows: the full name for ollama.com, the quant for Hugging Face
	Fit       fit
	Installed bool
	Active    *activeDL
	AliasOf   string // an earlier tag with the same digest, e.g. latest -> 8b
}

// handleDiscoverTags lists a library model's tags, for expanding its card.
func (s *Server) handleDiscoverTags(w http.ResponseWriter, r *http.Request) {
	model := r.URL.Query().Get("model")
	tags, err := s.lib.Tags(r.Context(), model)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			err = errMsg(model + " wasn't found on ollama.com.")
		}
		s.render(w, r, "error_fragment.html", map[string]any{"Error": err.Error()})
		return
	}
	rows := tagRows(tags, s.localModels(r), s.sys.Latest())
	s.render(w, r, "discover_tags", map[string]any{"Model": model, "Tags": rows})
}

// handleDiscoverHFFiles lists a Hugging Face repo's quantisations, for
// expanding its card. ctx is the model's context window, from the card.
func (s *Server) handleDiscoverHFFiles(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	files, err := s.lib.HFFiles(r.Context(), repo)
	if err != nil {
		if errors.Is(err, library.ErrNotFound) {
			err = errMsg(repo + " wasn't found on Hugging Face.")
		}
		s.render(w, r, "error_fragment.html", map[string]any{"Error": err.Error()})
		return
	}
	ctxLen, _ := strconv.Atoi(r.URL.Query().Get("ctx"))
	tags := make([]library.Tag, len(files))
	labels := make([]string, len(files))
	for i, f := range files {
		name, label := "hf.co/"+repo, "default"
		if f.Quant != "" {
			name, label = name+":"+f.Quant, f.Quant
		}
		tags[i] = library.Tag{Name: name, Size: f.Size, Context: formatTokens(ctxLen)}
		if f.Parts > 1 {
			tags[i].Input = fmt.Sprintf("split into %d files", f.Parts)
		}
		labels[i] = label
	}
	rows := tagRows(tags, s.localModels(r), s.sys.Latest())
	for i := range rows {
		rows[i].Label = labels[i]
	}
	s.render(w, r, "discover_tags", map[string]any{"Model": repo, "Tags": rows, "HF": true})
}

// tagRows pairs tags with their fit here and whether they're installed or
// downloading. Tags sharing a digest are the same download, so each is
// labelled with the first such tag, preferring a descriptive one ("8b") over
// "latest", and counts as installed if any of them is.
func tagRows(tags []library.Tag, local localModels, snap sysinfo.Snapshot) []tagRow {
	canon := map[string]string{}
	haveDigest := map[string]bool{}
	for _, latest := range []bool{false, true} {
		for _, t := range tags {
			if t.Digest == "" || strings.HasSuffix(t.Name, ":latest") != latest {
				continue
			}
			if _, seen := canon[t.Digest]; !seen {
				canon[t.Digest] = t.Name
			}
			if local.installed[tagKey(t.Name)] {
				haveDigest[t.Digest] = true
			}
		}
	}
	rows := make([]tagRow, len(tags))
	for i, t := range tags {
		k := tagKey(t.Name)
		rows[i] = tagRow{Tag: t, Label: t.Name, Fit: tagFit(t, snap), Installed: local.installed[k] || haveDigest[t.Digest], Active: local.active[k]}
		if c := canon[t.Digest]; c != t.Name {
			rows[i].AliasOf = c
		}
	}
	return rows
}

// localModels is what's installed and what's downloading, keyed by model
// (e.g. "qwen3", "hf.co/owner/repo") and by tag ("qwen3:8b",
// "hf.co/owner/repo:q4_k_m"; Hugging Face names are case-insensitive).
type localModels struct {
	installed map[string]bool
	active    map[string]*activeDL
}

func (s *Server) localModels(r *http.Request) localModels {
	l := localModels{installed: map[string]bool{}, active: map[string]*activeDL{}}
	if all, err := s.ol.List(r.Context()); err == nil {
		for _, m := range all {
			if model, tag, ok := modelKeys(m.Name); ok {
				l.installed[model], l.installed[tag] = true, true
			}
		}
	}
	if dls, err := s.st.ActiveDownloads(r.Context()); err == nil {
		l.addActive(dls, s.dl.Live())
	}
	return l
}

// addActive records queued and running downloads, with live's progress.
func (l localModels) addActive(dls []store.Download, live *downloads.Live) {
	for _, d := range dls {
		model, tag, ok := modelKeys(d.Model)
		if !ok {
			continue
		}
		a := &activeDL{ID: d.ID, Status: d.Status, Percent: -1}
		if live != nil && live.ID == d.ID && live.Total > 0 {
			a.Percent = live.Percent()
		}
		l.active[tag] = a
		// For the model's card, a running download beats a queued one.
		if cur := l.active[model]; cur == nil || cur.Status != store.DownloadDownloading {
			l.active[model] = a
		}
	}
}

// modelKeys are localModels' keys for a model name, for names from the
// ollama.com library and Hugging Face.
func modelKeys(name string) (model, tag string, ok bool) {
	ref, err := ollama.ParseName(name)
	if err != nil {
		return "", "", false
	}
	switch {
	case ref.Host == "registry.ollama.ai" && ref.Namespace == "library":
		return ref.Model, ref.Model + ":" + ref.Tag, true
	case ref.Host == "hf.co":
		model = hfKey(ref.Namespace + "/" + ref.Model)
		return model, model + ":" + strings.ToLower(ref.Tag), true
	}
	return "", "", false
}

func hfKey(repo string) string { return "hf.co/" + strings.ToLower(repo) }

// tagKey is localModels' tag key for a pullable name.
func tagKey(name string) string {
	_, tag, _ := modelKeys(name)
	return tag
}

// hardwareSummary describes what fit estimates are made against, e.g.
// "64.0 GB memory · NVIDIA GeForce RTX 4090, 24.0 GB VRAM".
func hardwareSummary(snap sysinfo.Snapshot) string {
	if snap.Time.IsZero() || snap.MemTotal == 0 {
		return ""
	}
	parts := []string{formatBytes(int64(snap.MemTotal)) + " memory"}
	for _, g := range snap.GPUs {
		p := strings.TrimSpace(g.Name)
		if p == "" {
			p = strings.TrimSpace(g.Vendor + " GPU")
		}
		switch {
		case g.Unified:
			p += ", sharing system memory"
		case g.HasMem():
			p += ", " + formatBytes(int64(g.MemTotal)) + " VRAM"
		}
		parts = append(parts, p)
	}
	if len(snap.GPUs) == 0 {
		parts = append(parts, "no GPU detected")
	}
	return strings.Join(parts, " · ")
}

// formatParams renders a parameter count as model names do: "27.3B", "270M".
func formatParams(n int64) string {
	switch {
	case n >= 1e9:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e9), ".0") + "B"
	case n >= 1e6:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return strconv.FormatInt(n, 10)
}

// formatTokens renders a context length as ollama.com does: "256K", "1M".
func formatTokens(n int) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%dM", n>>20)
	case n >= 1024:
		return fmt.Sprintf("%dK", n/1024)
	}
	return strconv.Itoa(n)
}

// formatCompact renders large counts briefly: 12450427 -> "12.5M".
func formatCompact(n int) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

// queueFromDiscover handles a Download button on the discover page, which
// posts with htmx and swaps the answer into the button's table cell: the
// download's status once queued, or the button again with an error, or with
// a confirmation dialog if the model looks too big for this machine.
func (s *Server) queueFromDiscover(w http.ResponseWriter, r *http.Request) {
	input := r.FormValue("model")
	data := map[string]any{"Name": input, "Fragment": true} // Fragment: refresh the nav badge
	c, err := s.dl.Check(r.Context(), input)
	if err == nil && r.FormValue("confirm") == "" {
		if concerns := s.resourceConcerns(c); len(concerns) > 0 {
			data["Confirm"] = map[string]any{"Name": c.Name, "Size": c.Size, "Concerns": concerns}
			s.render(w, r, "discover_dl_action", data)
			return
		}
	}
	var id int64
	if err == nil {
		id, err = s.dl.EnqueueChecked(r.Context(), c, currentUser(r).Username)
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, store.ErrAlreadyQueued) {
			msg = c.Name + " is already queued or downloading."
		}
		data["Error"] = msg
		s.render(w, r, "discover_dl_action", data)
		return
	}
	s.log.Info("download queued", "model", c.Name, "by", currentUser(r).Username)
	data["Queued"] = &activeDL{ID: id, Status: store.DownloadQueued, Percent: -1}
	data["Warning"] = c.Warning
	s.render(w, r, "discover_dl_action", data)
}
