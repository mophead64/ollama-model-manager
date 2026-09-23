package library

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
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
}

// HFPage is a page of Hugging Face search results.
type HFPage struct {
	Models     []HFModel
	NextCursor string // pass as HFQuery.Cursor for the next page; "" on the last
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
		"expand[]":  {"gguf", "downloads", "likes", "pipeline_tag", "lastModified", "gated", "tags"},
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
	var next string
	if n, err := url.Parse(resp.next); err == nil {
		next = n.Query().Get("cursor")
	}
	return HFPage{Models: models, NextCursor: next}, nil
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
			Gated: len(r.Gated) > 0 && string(r.Gated) != "false" && string(r.Gated) != "null",
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
