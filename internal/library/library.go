// Package library browses where Ollama can pull models from: the public
// library on ollama.com (its search, and each model's tags) and GGUF repos on
// Hugging Face (see hf.go).
//
// ollama.com has no API for this, so its pages are fetched and their HTML read
// for the few facts shown on them. If the site's markup changes, parsing finds
// nothing rather than failing. Where there has to be something (a search with
// no text, a model's tags), finding nothing is ErrUnreadable, so it isn't
// mistaken for "no models"; the weekly live test (live_test.go) catches the
// rest.
package library

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/version"
)

const (
	DefaultBase   = "https://ollama.com"
	DefaultHFBase = "https://huggingface.co"
	searchTTL     = 30 * time.Minute
	tagsTTL       = 6 * time.Hour
	maxCached     = 200
	maxPage       = 4 << 20
)

// Capabilities are the filters ollama.com's search offers, besides "cloud".
var Capabilities = []string{"vision", "tools", "thinking", "embedding", "decision"}

// Model is one search result.
type Model struct {
	Name         string // e.g. "qwen3", as pulled (the library namespace is implied)
	Description  string
	Capabilities []string // from Capabilities
	Cloud        bool     // offered as a cloud model
	Sizes        []string // parameter counts as labelled, e.g. "8b", "70b", "e4b", "8x7b"
	Pulls        string   // as shown, e.g. "2.5M"
}

// Page is a page of search results.
type Page struct {
	Models   []Model
	NextPage int // 0 when this is the last page
}

// Tag is one pullable tag of a model.
type Tag struct {
	Name    string // full name, e.g. "qwen3:8b"
	Size    int64  // download size in bytes, as listed (rounded); 0 when not listed (cloud tags)
	SizeMin int64  // when listed as a range ("4.6GB - 7.5GB", as for gemma4), the low end; Size is the high end
	Context string // context window as listed, e.g. "128K"
	Input   string // e.g. "Text, Image"
	Digest  string // short digest, shared by aliases such as "latest"
	Updated string
}

// Cloud reports whether the tag runs on Ollama's cloud rather than locally
// (e.g. "glm-5.3:cloud", "gpt-oss:120b-cloud").
func (t Tag) Cloud() bool {
	_, tag, _ := strings.Cut(t.Name, ":")
	return strings.Contains(tag, "cloud")
}

// Query is a search request.
type Query struct {
	Text  string
	Caps  []string // any of Capabilities, or "cloud"
	Order string   // "popular" (the default) or "newest"
	Page  int      // 1-based
}

// response is a fetched page, as cached.
type response struct {
	body    string
	next    string // the Link header's rel="next" URL, if any
	final   string // the URL it came from, after any redirects
	expires time.Time
}

// Client fetches library pages, caching them for a while so browsing doesn't
// hammer ollama.com or Hugging Face.
type Client struct {
	base, hfBase string
	hfToken      string // sent to Hugging Face, if set
	hc           *http.Client

	mu     sync.Mutex
	cache  map[string]response
	health Health    // see health.go
	who    whoAnswer // HFAccount's last answer
}

// New returns a client for the ollama.com library at base and Hugging Face at
// hfBase (DefaultBase and DefaultHFBase if empty). hfToken, a Hugging Face
// access token, is optional: with it, searches include the private repos it
// can see, and the file lists of gated repos it has access to.
func New(base, hfBase, hfToken string) *Client {
	if base == "" {
		base = DefaultBase
	}
	if hfBase == "" {
		hfBase = DefaultHFBase
	}
	return &Client{
		base:    strings.TrimRight(base, "/"),
		hfBase:  strings.TrimRight(hfBase, "/"),
		hfToken: hfToken,
		hc:      &http.Client{Timeout: 15 * time.Second},
		cache:   map[string]response{},
	}
}

// Search returns a page of models matching q.
func (c *Client) Search(ctx context.Context, q Query) (Page, error) {
	p, err := c.search(ctx, q)
	switch {
	case err != nil:
		c.record(ctx, err)
	case missingDetails(p, strings.TrimSpace(q.Text) == "" && len(q.Caps) == 0 && q.Page <= 1):
		c.record(ctx, errMissingDetails) // the names are still worth showing
	default:
		c.record(ctx, nil)
	}
	return p, err
}

func (c *Client) search(ctx context.Context, q Query) (Page, error) {
	v := url.Values{}
	if t := strings.TrimSpace(q.Text); t != "" {
		v.Set("q", t)
	}
	for _, cp := range q.Caps {
		v.Add("c", cp)
	}
	if q.Order == "newest" {
		v.Set("o", "newest")
	}
	if q.Page > 1 {
		v.Set("page", strconv.Itoa(q.Page))
	}
	path := "/search"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	// Later pages are only served to the page's own "load more" requests
	// (htmx); anything else is redirected back to the first page.
	var header http.Header
	if q.Page > 1 {
		header = http.Header{"Hx-Request": {"true"}}
	}
	resp, err := c.fetchWith(ctx, c.base+path, "ollama.com", searchTTL, header)
	if err != nil {
		return Page{}, err
	}
	if q.Page > 1 {
		if u, err := url.Parse(resp.final); err != nil || u.Query().Get("page") != strconv.Itoa(q.Page) {
			return Page{}, fmt.Errorf("%w: asked for page %d of the results, it sent the first", ErrUnreadable, q.Page)
		}
	}
	p := parseSearch(resp.body, max(q.Page, 1))
	// Without search text, the first page lists the library's most popular
	// (or newest) models, so it's never empty unless it couldn't be read.
	// Capability filters narrow it, but every capability has models.
	if len(p.Models) == 0 && strings.TrimSpace(q.Text) == "" && q.Page <= 1 {
		return Page{}, ErrUnreadable
	}
	return p, nil
}

// Tags lists a library model's tags, in the order ollama.com shows them.
func (c *Client) Tags(ctx context.Context, model string) ([]Tag, error) {
	if !namePartRE.MatchString(model) {
		return nil, fmt.Errorf("%q isn't a library model name", model)
	}
	tags, err := c.tags(ctx, model)
	c.record(ctx, err)
	return tags, err
}

func (c *Client) tags(ctx context.Context, model string) ([]Tag, error) {
	resp, err := c.fetch(ctx, c.base+"/library/"+model+"/tags", "ollama.com", tagsTTL)
	if err != nil {
		return nil, err
	}
	tags := parseTags(resp.body)
	if len(tags) == 0 { // every model has at least one
		return nil, ErrUnreadable
	}
	return tags, nil
}

var (
	// ErrNotFound is returned when a page doesn't exist (e.g. no such model).
	ErrNotFound = errors.New("not found")
	// ErrUnreadable is returned when an ollama.com page loaded but nothing
	// could be read from it where there has to be something: most likely
	// the site's layout has changed and the parser needs updating.
	ErrUnreadable = errors.New("couldn't read ollama.com (its layout may have changed)")
)

// fetch GETs url, from the cache if it was fetched within ttl. site names the
// host in errors.
func (c *Client) fetch(ctx context.Context, url, site string, ttl time.Duration) (response, error) {
	return c.fetchWith(ctx, url, site, ttl, nil)
}

// fetchWith is fetch, sending header with the request too.
func (c *Client) fetchWith(ctx context.Context, url, site string, ttl time.Duration, header http.Header) (response, error) {
	now := time.Now()
	c.mu.Lock()
	if e, ok := c.cache[url]; ok && now.Before(e.expires) {
		c.mu.Unlock()
		return e, nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return response{}, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", "ollama-model-manager/"+version.Version)
	if c.hfToken != "" && strings.HasPrefix(url, c.hfBase+"/") {
		req.Header.Set("Authorization", "Bearer "+c.hfToken)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("couldn't reach %s: %w", site, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return response{}, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return response{}, fmt.Errorf("%s returned %s", site, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
	if err != nil {
		return response{}, fmt.Errorf("reading %s: %w", site, err)
	}
	r := response{body: string(b), next: nextLink(resp.Header.Get("Link")), final: resp.Request.URL.String(), expires: now.Add(ttl)}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCached {
		for k, e := range c.cache {
			if now.After(e.expires) {
				delete(c.cache, k)
			}
		}
		if len(c.cache) >= maxCached {
			clear(c.cache)
		}
	}
	c.cache[url] = r
	return r, nil
}

var linkNextRE = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="next"`)

// nextLink is the rel="next" URL in a Link header, or "".
func nextLink(h string) string {
	if m := linkNextRE.FindStringSubmatch(h); m != nil {
		return m[1]
	}
	return ""
}

var (
	namePartRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

	// Search results: each is an <li> holding a link to /library/<name>,
	// with the description, then a row of chips saying whether it's a cloud
	// model, its sizes and its capabilities. The pull count sits on the
	// right, titled "N downloads". ollama.com has shown the chips in two
	// styles (in October 2026 it switched between them within days), so
	// both are read:
	//   - a rounded label each: "cloud", "27b", "vision";
	//   - "Cloud" with a tooltip saying it runs on Ollama's cloud, the sizes
	//     together ("27b · 35b"), and an icon and label per capability.
	resultRE = regexp.MustCompile(`<a href="/library/([^"/:?]+)"`)
	descRE   = regexp.MustCompile(`(?s)<p class="[^"]*\bmax-w-[^"]*"[^>]*>(.*?)</p>`)
	chipREs  = []*regexp.Regexp{
		regexp.MustCompile(`<span[^>]*class="[^"]*\brounded-md\b[^"]*"[^>]*>([^<]+)</span>`),
		regexp.MustCompile(`<span\s+class="font-medium text-black">([^<]+)</span>`),
		regexp.MustCompile(`(?s)<span\s+class="inline-flex items-center gap-1\.5">(?:<svg[^>]*>.*?</svg>)?([^<]+)</span>`),
	}
	cloudRE    = regexp.MustCompile(`role="tooltip"[^>]*>Runs on Ollama.s cloud<`)
	pullsRE    = regexp.MustCompile(`(?s)title="[^"]*\bdownloads"[^>]*>(?:<svg[^>]*>.*?</svg>)?\s*<span\s*>([^<]+)</span>`)
	nextPageRE = regexp.MustCompile(`hx-get="/search\?page=(\d+)"`)
	sizeRE     = regexp.MustCompile(`^(?:e?\d+(?:\.\d+)?|\d+x\d+(?:\.\d+)?)[mbt]$`)

	// Tags page: each tag's desktop row starts with this div, then has the
	// name link, size, context and input columns, and the digest.
	tagRowStart = `class="hidden md:flex`
	tagNameRE   = regexp.MustCompile(`<a href="/library/[^"]+" class="group-hover:underline">([^<]+)</a>`)
	tagColsRE   = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>\s*<p[^>]*>(.*?)</p>\s*<div[^>]*>(.*?)</div>`)
	digestRE    = regexp.MustCompile(`<span class="font-mono[^"]*">([0-9a-f]+)</span>&nbsp;·&nbsp;([^<\n]+)`)
	tagStripRE  = regexp.MustCompile(`<[^>]*>`)
	byteSizeRE  = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGT]?B)$`)
)

func parseSearch(body string, page int) Page {
	var p Page
	locs := resultRE.FindAllStringSubmatchIndex(body, -1)
	for i, loc := range locs {
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunk := body[loc[0]:end]
		if j := strings.Index(chunk, "</li>"); j >= 0 {
			chunk = chunk[:j]
		}
		m := Model{Name: body[loc[2]:loc[3]]}
		if d := descRE.FindStringSubmatch(chunk); d != nil {
			m.Description = clean(d[1])
		}
		m.Cloud = cloudRE.MatchString(chunk)
		for _, label := range chipLabels(chunk) {
			switch {
			case label == "cloud":
				m.Cloud = true
			case isCapability(label):
				if !slices.Contains(m.Capabilities, label) {
					m.Capabilities = append(m.Capabilities, label)
				}
			case sizeRE.MatchString(label):
				if !slices.Contains(m.Sizes, label) {
					m.Sizes = append(m.Sizes, label)
				}
			}
		}
		if s := pullsRE.FindStringSubmatch(chunk); s != nil {
			m.Pulls = clean(s[1])
		}
		p.Models = append(p.Models, m)
	}
	for _, s := range nextPageRE.FindAllStringSubmatch(body, -1) {
		if n, _ := strconv.Atoi(s[1]); n > page {
			p.NextPage = n
		}
	}
	return p
}

// chipLabels is the text of a result's chips, in either style (chipREs),
// lower-cased, in the order they're on the page. Anything else a pattern
// catches (a tooltip, a context length) isn't a capability or size, so it's
// ignored.
func chipLabels(chunk string) []string {
	type found struct {
		at    int
		label string
	}
	var all []found
	for _, re := range chipREs {
		for _, m := range re.FindAllStringSubmatchIndex(chunk, -1) {
			all = append(all, found{m[0], strings.ToLower(clean(chunk[m[2]:m[3]]))})
		}
	}
	slices.SortFunc(all, func(a, b found) int { return a.at - b.at })
	labels := make([]string, len(all))
	for i, f := range all {
		labels[i] = f.label
	}
	return labels
}

func parseTags(body string) []Tag {
	var tags []Tag
	rows := strings.Split(body, tagRowStart)
	for _, row := range rows[1:] {
		n := tagNameRE.FindStringSubmatchIndex(row)
		if n == nil {
			continue
		}
		t := Tag{Name: clean(row[n[2]:n[3]])}
		rest := row[n[1]:]
		if c := tagColsRE.FindStringSubmatch(rest); c != nil {
			t.SizeMin, t.Size = parseSizeRange(clean(c[1]))
			t.Context = clean(c[2])
			t.Input = clean(c[3])
		}
		if d := digestRE.FindStringSubmatch(rest); d != nil {
			t.Digest, t.Updated = d[1], clean(d[2])
		}
		tags = append(tags, t)
	}
	return tags
}

// clean strips markup and entities and collapses whitespace.
func clean(s string) string {
	s = html.UnescapeString(tagStripRE.ReplaceAllString(s, ""))
	return strings.Join(strings.Fields(s), " ")
}

func isCapability(s string) bool {
	for _, c := range Capabilities {
		if c == s {
			return true
		}
	}
	return false
}

// parseByteSize reads sizes as ollama.com lists them ("18GB", "815MB"), which
// are decimal units. Unparseable input (e.g. "-") gives 0.
func parseByteSize(s string) int64 {
	m := byteSizeRE.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12}[m[2]]
	return int64(f * mult)
}

// parseSizeRange reads a tag's size: one ("18GB"), or a range ("4.6GB -
// 7.5GB") as some models list, giving its low end and high end. For a
// single size, low is 0.
func parseSizeRange(s string) (low, high int64) {
	if a, b, ok := strings.Cut(strings.ReplaceAll(s, "–", "-"), " - "); ok {
		if low, high = parseByteSize(strings.TrimSpace(a)), parseByteSize(strings.TrimSpace(b)); low > 0 && high > 0 {
			return low, high
		}
		return 0, 0
	}
	return 0, parseByteSize(s)
}

// ParamCount turns a size label such as "8b", "270m", "8x7b" or "e4b" into a
// parameter count. "e" sizes (Gemma's "effective" parameters) understate the
// real count, so estimates built on them are low. Returns 0 if unparseable.
func ParamCount(label string) float64 {
	s := strings.ToLower(strings.TrimPrefix(strings.ToLower(label), "e"))
	if len(s) < 2 {
		return 0
	}
	mult := map[byte]float64{'m': 1e6, 'b': 1e9, 't': 1e12}[s[len(s)-1]]
	if mult == 0 {
		return 0
	}
	num := s[:len(s)-1]
	experts := 1.0
	if a, b, ok := strings.Cut(num, "x"); ok {
		e, err := strconv.ParseFloat(a, 64)
		if err != nil {
			return 0
		}
		experts, num = e, b
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	return experts * f * mult
}
