package library

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

// HFSorts are the orders Hugging Face search offers here, keyed by the value
// used in URLs, with the API's sort field.
var HFSorts = map[string]string{
	"downloads": "downloads",
	"trending":  "trendingScore",
	"likes":     "likes",
	"newest":    "createdAt",
}

const hfPageSize = 20

// HFModel is a Hugging Face repo holding GGUF files, which Ollama can pull
// as hf.co/<repo>:<quant>.
type HFModel struct {
	Repo      string // e.g. "unsloth/Qwen3-8B-GGUF"
	Downloads int
	Likes     int
	Params    int64  // parameter count, from the GGUF metadata; 0 if unknown
	Context   int    // context window in tokens; 0 if unknown
	Arch      string // e.g. "qwen3"
	Pipeline  string // e.g. "text-generation", "image-text-to-text"
	BaseModel string // the model it's a quantisation of, if tagged
	Gated     bool   // needs accepting terms (and a token) before download
	Updated   time.Time
	// Quantisations Ollama can pull, going by the repo's file names (the rules
	// HFFiles uses); -1 if Hugging Face didn't list the files.
	Quants int
}

// HFPage is a page of Hugging Face search results.
type HFPage struct {
	Models     []HFModel
	NextCursor string // pass as HFQuery.Cursor for the next page; "" on the last
	// Repos left out because none of their GGUFs is a model Ollama can pull,
	// e.g. one split into per-layer files for distributed inference.
	Unpullable int
}

// HFQuery is a Hugging Face search.
type HFQuery struct {
	Text   string
	Sort   string // a key of HFSorts; "downloads" if not
	Cursor string
}

// HFFile is one quantisation in a repo: a GGUF file, or several parts of one.
type HFFile struct {
	Quant string // tag to pull it by, e.g. "Q4_K_M", "UD-Q4_K_XL"; "" for a repo's only, unlabelled file
	Size  int64  // bytes, all parts
	Parts int    // files it's split across
	Path  string // first (or only) file, within the repo
}

var (
	hfRepoRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// "-00001-of-00003" before .gguf marks a part of a split file.
	hfSplitRE = regexp.MustCompile(`-\d{5}-of-(\d{5})$`)
	// The quantisation at the end of a file name, e.g. "…-UD-Q4_K_XL".
	hfQuantRE = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:I?Q\d(?:_[A-Z0-9]+)*|BF16|F16|F32|MXFP4(?:_MOE)?|TQ\d_\d))$`)
)

// HasHFToken reports whether requests to Hugging Face carry an access token.
func (c *Client) HasHFToken() bool { return c.hfToken != "" }

// whoAnswer is a cached HFWhoAmI answer.
type whoAnswer struct {
	name string
	err  error
	at   time.Time
}

// HFAccount is HFWhoAmI, remembered for a while (a failure for less), for
// showing on pages without asking Hugging Face each time.
func (c *Client) HFAccount(ctx context.Context) (string, error) {
	c.mu.Lock()
	w := c.who
	c.mu.Unlock()
	keep := 30 * time.Minute
	if w.err != nil {
		keep = 2 * time.Minute
	}
	if !w.at.IsZero() && time.Since(w.at) < keep {
		return w.name, w.err
	}
	name, err := c.HFWhoAmI(ctx)
	if ctx.Err() != nil {
		return "", err // gave up waiting: not an answer to remember
	}
	c.mu.Lock()
	c.who = whoAnswer{name, err, time.Now()}
	c.mu.Unlock()
	return name, err
}

// HFWhoAmI returns the Hugging Face account the access token belongs to.
func (c *Client) HFWhoAmI(ctx context.Context) (string, error) {
	if c.hfToken == "" {
		return "", errors.New("no Hugging Face token is set")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.hfBase+"/api/whoami-v2", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.hfToken)
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("couldn't reach Hugging Face: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("Hugging Face rejected the token: it may have been revoked or mistyped")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Hugging Face returned %s", resp.Status)
	}
	var who struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&who); err != nil || who.Name == "" {
		return "", errors.New("unexpected answer from Hugging Face")
	}
	return who.Name, nil
}

// ValidHFRepo reports whether repo looks like "owner/name".
func ValidHFRepo(repo string) bool { return hfRepoRE.MatchString(repo) }

// HFSearch lists GGUF repos on Hugging Face.
func (c *Client) HFSearch(ctx context.Context, q HFQuery) (HFPage, error) {
	sort, ok := HFSorts[q.Sort]
	if !ok {
		sort = HFSorts["downloads"]
	}
	v := url.Values{
		"filter":    {"gguf"},
		"sort":      {sort},
		"direction": {"-1"},
		"limit":     {fmt.Sprint(hfPageSize)},
		// siblings (the file names) show which repos have a GGUF Ollama can
		// pull, without asking for each one's file list.
		"expand[]": {"gguf", "downloads", "likes", "pipeline_tag", "lastModified", "gated", "tags", "siblings"},
	}
	if t := strings.TrimSpace(q.Text); t != "" {
		v.Set("search", t)
	}
	if q.Cursor != "" {
		v.Set("cursor", q.Cursor)
	}
	u := c.hfBase + "/api/models?" + v.Encode()
	resp, err := c.fetch(ctx, u, "Hugging Face", searchTTL)
	if err != nil {
		return HFPage{}, err
	}
	models, err := parseHFModels(resp.body)
	if err != nil {
		return HFPage{}, err
	}
	page := HFPage{Models: models[:0]}
	for _, m := range models {
		if m.Quants == 0 {
			page.Unpullable++
			continue
		}
		page.Models = append(page.Models, m)
	}
	if n, err := url.Parse(resp.next); err == nil {
		page.NextCursor = n.Query().Get("cursor")
	}
	return page, nil
}

// HFFiles lists the quantisations in a repo, smallest first.
func (c *Client) HFFiles(ctx context.Context, repo string) ([]HFFile, error) {
	if !ValidHFRepo(repo) {
		return nil, fmt.Errorf("%q isn't a Hugging Face repo name", repo)
	}
	resp, err := c.fetch(ctx, c.hfBase+"/api/models/"+repo+"/tree/main?recursive=true", "Hugging Face", tagsTTL)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Type, Path string
		Size       int64
	}
	if err := json.Unmarshal([]byte(resp.body), &entries); err != nil {
		return nil, fmt.Errorf("reading Hugging Face's file list: %w", err)
	}
	var paths []string
	sizes := map[string]int64{}
	for _, e := range entries {
		if e.Type == "file" {
			paths = append(paths, e.Path)
			sizes[e.Path] = e.Size
		}
	}
	return groupGGUFs(paths, sizes), nil
}

func parseHFModels(body string) ([]HFModel, error) {
	var raw []struct {
		ID           string
		Downloads    int
		Likes        int
		PipelineTag  string          `json:"pipeline_tag"`
		LastModified time.Time       `json:"lastModified"`
		Gated        json.RawMessage // false, or "auto"/"manual"
		Tags         []string
		Siblings     *[]struct{ Rfilename string } // nil if not listed
		GGUF         struct {
			Total         int64
			Architecture  string
			ContextLength int `json:"context_length"`
		}
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, fmt.Errorf("reading Hugging Face's search results: %w", err)
	}
	models := make([]HFModel, 0, len(raw))
	for _, r := range raw {
		m := HFModel{
			Repo: r.ID, Downloads: r.Downloads, Likes: r.Likes, Pipeline: r.PipelineTag, Updated: r.LastModified,
			Params: r.GGUF.Total, Context: r.GGUF.ContextLength, Arch: r.GGUF.Architecture,
			Gated:  len(r.Gated) > 0 && string(r.Gated) != "false" && string(r.Gated) != "null",
			Quants: -1,
		}
		if r.Siblings != nil {
			names := make([]string, len(*r.Siblings))
			for i, f := range *r.Siblings {
				names[i] = f.Rfilename
			}
			m.Quants = len(groupGGUFs(names, nil))
		}
		// The metadata describes one file in the repo, which is sometimes a
		// small helper (e.g. a draft model) rather than the model itself. A
		// size in the repo's name ("…-27B-GGUF") is then the better guess.
		if n := paramsFromName(r.ID); n > 0 && m.Params < n/2 {
			m.Params = n
		}
		for _, t := range r.Tags {
			if b, ok := strings.CutPrefix(t, "base_model:quantized:"); ok {
				m.BaseModel = b
				break
			}
		}
		models = append(models, m)
	}
	return models, nil
}

// A parameter count in a repo name: "27B" in "Qwen3.8-27B-GGUF", but not the
// active-parameter "A3B" of "30B-A3B", nor the "7B" of "8x7B".
var nameParamsRE = regexp.MustCompile(`(?i)(?:^|[^a-z0-9.])(\d+(?:\.\d+)?)([bm])(?:$|[^a-z0-9])`)

// paramsFromName is the first parameter count in a repo name, or 0.
func paramsFromName(repo string) int64 {
	_, name, _ := strings.Cut(repo, "/")
	m := nameParamsRE.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	return int64(ParamCount(m[1] + m[2]))
}

// groupGGUFs picks out the model files among a repo's paths: GGUFs, less the
// vision projectors, importance matrices and draft models that accompany them,
// with split files' parts combined.
func groupGGUFs(paths []string, sizes map[string]int64) []HFFile {
	byQuant := map[string]*HFFile{}
	var order []string
	var unlabelled []string
	for _, p := range paths {
		base, ok := strings.CutSuffix(path.Base(p), ".gguf")
		lower := strings.ToLower(base)
		if !ok || strings.HasPrefix(lower, "mmproj") || strings.Contains(lower, "imatrix") || strings.HasPrefix(lower, "mtp-") {
			continue
		}
		name := hfSplitRE.ReplaceAllString(base, "")
		m := hfQuantRE.FindStringSubmatch(name)
		if m == nil {
			unlabelled = append(unlabelled, p)
			continue
		}
		key := strings.ToUpper(m[1])
		f, seen := byQuant[key]
		if !seen {
			f = &HFFile{Quant: m[1], Path: p}
			byQuant[key] = f
			order = append(order, key)
		}
		f.Size += sizes[p]
		f.Parts++
		if p < f.Path {
			f.Path = p
		}
	}
	files := make([]HFFile, 0, len(order)+1)
	for _, k := range order {
		files = append(files, *byQuant[k])
	}
	// A repo with a single file needs no tag: Ollama pulls it by default.
	if len(files) == 0 && len(unlabelled) == 1 {
		files = append(files, HFFile{Size: sizes[unlabelled[0]], Parts: 1, Path: unlabelled[0]})
	}
	slices.SortStableFunc(files, func(a, b HFFile) int { return cmp.Compare(a.Size, b.Size) })
	return files
}
