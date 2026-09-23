package web

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

func TestParseParamSize(t *testing.T) {
	cases := map[string]float64{"9.7B": 9.7e9, "137M": 137e6, "3b": 3e9, "1.5T": 1.5e12, "22K": 22e3, "7": 7}
	for in, want := range cases {
		if got, ok := parseParamSize(in); !ok || got != want {
			t.Errorf("parseParamSize(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "B", "abc"} {
		if _, ok := parseParamSize(bad); ok {
			t.Errorf("parseParamSize(%q) should fail", bad)
		}
	}
}

func names(ms []ollama.Model) string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return strings.Join(out, ",")
}

func TestSortModels(t *testing.T) {
	models := func() []ollama.Model {
		return []ollama.Model{
			{Name: "b", Size: 300, Details: ollama.Details{ParameterSize: "3B", ContextLength: 8192}},
			{Name: "a", Size: 100, Details: ollama.Details{ParameterSize: "137M"}},
			{Name: "c", Size: 200, Details: ollama.Details{ParameterSize: "9.7B", ContextLength: 262144}},
			{Name: "d", Size: 200},
		}
	}
	cases := []struct {
		key  string
		desc bool
		want string
	}{
		{"", false, "a,b,c,d"},
		{"name", true, "d,c,b,a"},
		{"size", false, "a,c,d,b"}, // c and d tie on size; name breaks it
		{"size", true, "b,c,d,a"},
		{"params", true, "c,b,a,d"},  // d has no params: last either way
		{"params", false, "a,b,c,d"}, // 137M < 3B < 9.7B
		{"context", true, "c,b,a,d"},
		{"context", false, "b,c,a,d"},
	}
	for _, c := range cases {
		ms := models()
		sortModels(ms, c.key, c.desc)
		if got := names(ms); got != c.want {
			t.Errorf("sort %q desc=%v = %s, want %s", c.key, c.desc, got, c.want)
		}
	}
}

func TestSortHeaders(t *testing.T) {
	st := parseListState(url.Values{"q": {"llama"}, "cap": {"tools"}})
	h := st.headers()
	if h["name"].Arrow != "▲" || h["name"].URL != "/models?cap=tools&dir=desc&q=llama&sort=name" {
		t.Errorf("default name header = %+v", h["name"])
	}
	if h["size"].Arrow != "" || h["size"].URL != "/models?cap=tools&dir=desc&q=llama&sort=size" {
		t.Errorf("inactive size header should start descending, keeping filters: %+v", h["size"])
	}

	st = parseListState(url.Values{"sort": {"size"}, "dir": {"desc"}, "page": {"3"}})
	h = st.headers()
	if h["size"].Arrow != "▼" || h["size"].URL != "/models?dir=asc&sort=size" {
		t.Errorf("active size header should flip to ascending on page 1: %+v", h["size"])
	}
	if h["name"].URL != "/models" {
		t.Errorf("name header should reset to the default URL, got %q", h["name"].URL)
	}

	if st := parseListState(url.Values{"sort": {"bogus"}, "dir": {"desc"}}); st.Sort != "" || st.Desc {
		t.Errorf("unknown sort key should fall back to name ascending: %+v", st)
	}
}
