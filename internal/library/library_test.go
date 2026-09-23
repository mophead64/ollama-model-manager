package library

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// The testdata pages are trimmed copies of real ollama.com pages.

func TestParseSearch(t *testing.T) {
	b, err := os.ReadFile("testdata/search.html")
	if err != nil {
		t.Fatal(err)
	}
	p := parseSearch(string(b), 1)
	want := []Model{
		{Name: "glm-5.3", Description: "Z.ai's flagship model and the most capable open-weights model for coding, with major gains on long-horizon agentic tasks.",
			Capabilities: []string{"tools", "thinking"}, Cloud: true, Pulls: "58.1K", Tags: 1, Updated: "3 weeks ago"},
		{Name: "qwen3.8", Description: "Qwen3.8 delivers substantial gains across coding, professional work, research, and long-horizon agentic tasks.",
			Capabilities: []string{"vision", "tools", "thinking"}, Sizes: []string{"27b"}, Pulls: "2.5M", Tags: 12, Updated: "1 month ago"},
		{Name: "qwen3.5", Description: "Qwen 3.5 is a family of open-source multimodal models that delivers exceptional utility and performance.",
			Capabilities: []string{"vision", "tools", "thinking"}, Cloud: true, Sizes: []string{"0.8b", "2b", "4b", "9b", "27b", "35b", "122b"},
			Pulls: "20.8M", Tags: 64, Updated: "3 weeks ago"},
	}
	if !reflect.DeepEqual(p.Models, want) {
		t.Errorf("models =\n%+v\nwant\n%+v", p.Models, want)
	}
	if p.NextPage != 2 {
		t.Errorf("next page = %d, want 2", p.NextPage)
	}
	if got := parseSearch(string(b), 2).NextPage; got != 0 {
		t.Errorf("on page 2, next page = %d, want 0 (the link is to page 2 itself)", got)
	}
}

func TestParseTags(t *testing.T) {
	b, err := os.ReadFile("testdata/tags.html")
	if err != nil {
		t.Fatal(err)
	}
	got := parseTags(string(b))
	want := []Tag{
		{Name: "qwen3.8:latest", Size: 18e9, Context: "256K", Input: "Text, Image", Digest: "22130167c4c2", Updated: "1 month ago"},
		{Name: "qwen3.8:27b", Size: 18e9, Context: "256K", Input: "Text, Image", Digest: "22130167c4c2", Updated: "1 month ago"},
		{Name: "qwen3.8:27b-q4_K_M", Size: 18e9, Context: "256K", Input: "Text, Image", Digest: "25b843619e94", Updated: "1 month ago"},
		{Name: "glm-5.3:cloud", Context: "1M", Input: "Text", Digest: "13055f1621ef", Updated: "3 weeks ago"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tags =\n%+v\nwant\n%+v", got, want)
	}
	if got[0].Cloud() || !got[3].Cloud() {
		t.Errorf("Cloud() wrong: %v %v", got[0].Cloud(), got[3].Cloud())
	}
}

func TestParamCount(t *testing.T) {
	for in, want := range map[string]float64{
		"8b": 8e9, "0.8b": 0.8e9, "270m": 270e6, "8x7b": 56e9, "e4b": 4e9, "1t": 1e12,
		"": 0, "b": 0, "xyz": 0, "8q": 0,
	} {
		if got := ParamCount(in); got != want {
			t.Errorf("ParamCount(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClientSearchQueryAndCache(t *testing.T) {
	page, _ := os.ReadFile("testdata/search.html")
	var hits atomic.Int32
	var lastURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		lastURL = r.URL.String()
		if r.URL.Path == "/library/nope/tags" {
			http.NotFound(w, r)
			return
		}
		w.Write(page)
	}))
	defer srv.Close()
	c := New(srv.URL, "", "")

	q := Query{Text: " qwen ", Caps: []string{"vision", "tools"}, Order: "newest", Page: 2}
	p, err := c.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Models) != 3 {
		t.Errorf("got %d models, want 3", len(p.Models))
	}
	if want := "/search?c=vision&c=tools&o=newest&page=2&q=qwen"; lastURL != want {
		t.Errorf("requested %s, want %s", lastURL, want)
	}
	if _, err := c.Search(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("fetched %d times, want 1 (second from cache)", n)
	}

	if _, err := c.Tags(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing model: err = %v, want ErrNotFound", err)
	}
	if _, err := c.Tags(context.Background(), "../etc"); err == nil {
		t.Error("bad model name accepted")
	}
}

func TestParseHFModels(t *testing.T) {
	b, err := os.ReadFile("testdata/hf_search.json")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := parseHFModels(string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 {
		t.Fatalf("got %d models, want 3", len(ms))
	}
	m := ms[1]
	if m.Repo != "unsloth/Qwen3.8-27B-GGUF" || m.Params != 27320697856 || m.Context != 262144 || m.Arch != "qwen35" ||
		m.BaseModel != "Qwen/Qwen3.8-27B" || m.Downloads == 0 || m.Updated.IsZero() || m.Gated {
		t.Errorf("model = %+v", m)
	}
	if !ms[2].Gated {
		t.Error(`gated "manual" not read as gated`)
	}
}

func TestGroupGGUFs(t *testing.T) {
	b, err := os.ReadFile("testdata/hf_tree.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Type, Path string
		Size       int64
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatal(err)
	}
	var paths []string
	sizes := map[string]int64{}
	for _, e := range entries {
		if e.Type == "file" {
			paths = append(paths, e.Path)
			sizes[e.Path] = e.Size
		}
	}
	got := groupGGUFs(paths, sizes)
	want := []HFFile{
		{Quant: "UD-IQ1_S", Size: 6192222208, Parts: 1, Path: "Qwen3.8-27B-UD-IQ1_S.gguf"},
		{Quant: "UD-Q4_K_M", Size: 16464440224, Parts: 1, Path: "Qwen3.8-27B-UD-Q4_K_M.gguf"},
		{Quant: "Q8_0", Size: 29047086048, Parts: 1, Path: "Qwen3.8-27B-Q8_0.gguf"},
		{Quant: "BF16", Size: 49986159616 + 4671576000, Parts: 2, Path: "BF16/Qwen3.8-27B-BF16-00001-of-00002.gguf"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", got, want)
	}

	// A lone file with no quantisation in its name is pulled untagged.
	if got := groupGGUFs([]string{"model.gguf", "mmproj.gguf"}, map[string]int64{"model.gguf": 5}); len(got) != 1 || got[0].Quant != "" {
		t.Errorf("lone file: %+v", got)
	}
}

func TestHFSearchPaging(t *testing.T) {
	page, _ := os.ReadFile("testdata/hf_search.json")
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("cursor"))
		if r.URL.Query().Get("cursor") == "" {
			w.Header().Set("Link", `<https://huggingface.co/api/models?filter=gguf&cursor=abc123>; rel="next"`)
		}
		w.Write(page)
	}))
	defer srv.Close()
	c := New("", srv.URL, "")

	p, err := c.HFSearch(context.Background(), HFQuery{Text: "qwen", Sort: "trending"})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextCursor != "abc123" || len(p.Models) != 3 {
		t.Fatalf("page 1: next = %q, %d models", p.NextCursor, len(p.Models))
	}
	p, err = c.HFSearch(context.Background(), HFQuery{Text: "qwen", Sort: "trending", Cursor: p.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextCursor != "" || !reflect.DeepEqual(got, []string{"", "abc123"}) {
		t.Errorf("page 2: next = %q; cursors sent %q", p.NextCursor, got)
	}
	if _, err := c.HFFiles(context.Background(), "../../etc"); err == nil {
		t.Error("bad repo name accepted")
	}
}

func TestParamsFromName(t *testing.T) {
	for repo, want := range map[string]int64{
		"unsloth/Qwen3.8-27B-GGUF":                  27e9,
		"unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF": 30e9,
		"org/gemma-3-270m-it-GGUF":                  270e6,
		"org/Mixtral-8x7B-GGUF":                     0,
		"org/Llama-3.1-8b":                          8e9,
		"org/no-size-here":                          0,
		"org/v1.5-GGUF":                             0,
	} {
		if got := paramsFromName(repo); got != want {
			t.Errorf("paramsFromName(%q) = %d, want %d", repo, got, want)
		}
	}
}

func TestHFToken(t *testing.T) {
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = append(auth, r.URL.Path+" "+r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/api/whoami-v2" && r.Header.Get("Authorization") == "Bearer hf_good":
			w.Write([]byte(`{"name":"josh","type":"user"}`))
		case r.URL.Path == "/api/whoami-v2":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()

	c := New("", srv.URL, "hf_good")
	if name, err := c.HFWhoAmI(context.Background()); name != "josh" || err != nil {
		t.Errorf("whoami = %q, %v", name, err)
	}
	if _, err := c.HFSearch(context.Background(), HFQuery{}); err != nil {
		t.Fatal(err)
	}
	if auth[1] != "/api/models Bearer hf_good" {
		t.Errorf("search sent %q", auth[1])
	}
	if _, err := New("", srv.URL, "hf_bad").HFWhoAmI(context.Background()); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("bad token: %v", err)
	}
	if _, err := New("", srv.URL, "").HFWhoAmI(context.Background()); err == nil {
		t.Error("no token: want an error")
	}
}
