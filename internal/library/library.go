// Package library browses where Ollama can pull models from: the public
// library on ollama.com (its search, and each model's tags) and GGUF repos on
// Hugging Face (see hf.go).
//
// ollama.com has no API for this, so its pages are fetched and their HTML read
// for the few facts shown on them. If the site's markup changes, parsing finds
// nothing rather than failing, and the caller shows that as "no models".
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
var Capabilities = []string{"vision", "tools", "thinking", "embedding"}

// Model is one search result.
type Model struct {
	Name         string // e.g. "qwen3", as pulled (the library namespace is implied)
	Description  string
	Capabilities []string // from Capabilities
	Cloud        bool     // offered as a cloud model
	Sizes        []string // parameter counts as labelled, e.g. "8b", "70b", "e4b", "8x7b"
	Pulls        string   // as shown, e.g. "2.5M"
	Tags         int
	Updated      string // as shown, e.g. "3 weeks ago"
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
	expires time.Time
}

// Client fetches library pages, caching them for a while so browsing doesn't
// hammer ollama.com or Hugging Face.
type Client struct {
	base, hfBase string
	hfToken      string // sent to Hugging Face, if set
	hc           *http.Client

	mu    sync.Mutex
	cache map[string]response
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
	resp, err := c.fetch(ctx, c.base+path, "ollama.com", searchTTL)
	if err != nil {
		return Page{}, err
	}
	return parseSearch(resp.body, max(q.Page, 1)), nil
}

// Tags lists a library model's tags, in the order ollama.com shows them.
func (c *Client) Tags(ctx context.Context, model string) ([]Tag, error) {
	if !namePartRE.MatchString(model) {
		return nil, fmt.Errorf("%q isn't a library model name", model)
	}
	resp, err := c.fetch(ctx, c.base+"/library/"+model+"/tags", "ollama.com", tagsTTL)
	if err != nil {
		return nil, err
	}
	return parseTags(resp.body), nil
}

// ErrNotFound is returned when a page doesn't exist (e.g. no such model).
var ErrNotFound = errors.New("not found")

// fetch GETs url, from the cache if it was fetched within ttl. site names the
// host in errors.
func (c *Client) fetch(ctx context.Context, url, site string, ttl time.Duration) (response, error) {
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
	r := response{body: string(b), next: nextLink(resp.Header.Get("Link")), expires: now.Add(ttl)}

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

	// Search results: each is an <li> holding a link to /library/<name>.
	resultRE   = regexp.MustCompile(`<a href="/library/([^"/:?]+)"`)
	descRE     = regexp.MustCompile(`(?s)<p class="max-w-lg[^"]*">(.*?)</p>`)
	chipRE     = regexp.MustCompile(`<span[^>]*class="[^"]*\brounded-md\b[^"]*"[^>]*>([^<]+)</span>`)
	pullsRE    = regexp.MustCompile(`<span\s*>([^<]+)</span>\s*<span[^>]*>&nbsp;Pulls`)
	tagCountRE = regexp.MustCompile(`<span\s*>([^<]+)</span>\s*<span[^>]*>&nbsp;Tags?`)
	updatedRE  = regexp.MustCompile(`Updated&nbsp;</span>\s*<span\s*>([^<]+)</span>`)
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
		for _, c := range chipRE.FindAllStringSubmatch(chunk, -1) {
			label := strings.ToLower(clean(c[1]))
			switch {
			case label == "cloud":
				m.Cloud = true
			case isCapability(label):
				m.Capabilities = append(m.Capabilities, label)
			case sizeRE.MatchString(label):
				m.Sizes = append(m.Sizes, label)
			}
		}
		if s := pullsRE.FindStringSubmatch(chunk); s != nil {
			m.Pulls = clean(s[1])
		}
		if s := tagCountRE.FindStringSubmatch(chunk); s != nil {
			m.Tags, _ = strconv.Atoi(strings.ReplaceAll(clean(s[1]), ",", ""))
		}
		if s := updatedRE.FindStringSubmatch(chunk); s != nil {
			m.Updated = clean(s[1])
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
			t.Size = parseByteSize(clean(c[1]))
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
