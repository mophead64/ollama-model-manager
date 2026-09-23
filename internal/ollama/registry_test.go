package ollama

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		"llama3.2":                                 "llama3.2:latest",
		"qwen3:8b":                                 "qwen3:8b",
		"  ollama pull qwen3:8b ":                  "qwen3:8b",
		"ollama run gemma3":                        "gemma3:latest",
		"https://ollama.com/library/qwen3":         "qwen3:latest",
		"https://ollama.com/gnokit/improve-prompt": "gnokit/improve-prompt:latest",
		"library/llama3.2:3b":                      "llama3.2:3b",
		"hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M": "hf.co/bartowski/Llama-3.2-1B-Instruct-GGUF:Q4_K_M",
		"https://huggingface.co/bartowski/X-GGUF":           "hf.co/bartowski/X-GGUF:latest",
		"localhost:5000/team/model:v1":                      "localhost:5000/team/model:v1",
	}
	for in, want := range cases {
		r, err := NormalizeName(in)
		if err != nil {
			t.Errorf("NormalizeName(%q) error: %v", in, err)
			continue
		}
		if got := r.String(); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "two words", "a/b/c/d", "model:", "-bad", "mod@el", "qwen3:8b;rm"} {
		if _, err := NormalizeName(bad); err == nil {
			t.Errorf("NormalizeName(%q) should fail", bad)
		}
	}
	r, _ := ParseName("hf.co/bartowski/X-GGUF:Q4_K_M")
	if r.Host != "hf.co" || r.Namespace != "bartowski" || r.Model != "X-GGUF" || r.Tag != "Q4_K_M" {
		t.Errorf("ParseName parts = %+v", r)
	}
}

// hfTransport fakes huggingface.co's model API and hf.co's registry.
type hfTransport struct{}

func (hfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp := func(code int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	switch r.URL.Host + r.URL.Path {
	case "huggingface.co/api/models/meta-llama/Gated":
		return resp(200, `{"gated":"manual"}`)
	case "huggingface.co/api/models/bartowski/Public-GGUF":
		return resp(200, `{"gated":false}`)
	case "hf.co/v2/bartowski/Public-GGUF/manifests/Q4_K_M":
		return resp(200, `{"config":{"size":551},"layers":[{"size":807694464},{"size":1481},{"size":65}]}`)
	case "hf.co/v2/bartowski/Public-GGUF/manifests/Q9_X":
		return resp(400, "") // what Hugging Face actually sends for an unknown quant
	}
	return resp(401, "")
}

func TestCheckRegistryHF(t *testing.T) {
	c := &http.Client{Transport: hfTransport{}}
	cases := map[string]error{
		"hf.co/meta-llama/Gated:latest":      ErrGated,
		"hf.co/bartowski/Public-GGUF:Q4_K_M": nil,
		"hf.co/bartowski/Public-GGUF:Q9_X":   ErrModelNotFound,
		"hf.co/someone/Nope-GGUF:Q4_K_M":     ErrPrivateOrMissing,
	}
	for name, want := range cases {
		r, _ := ParseName(name)
		if _, got := CheckRegistry(context.Background(), c, r); !errors.Is(got, want) && got != want {
			t.Errorf("CheckRegistry(%s) = %v, want %v", name, got, want)
		}
	}

	r, _ := ParseName("hf.co/bartowski/Public-GGUF:Q4_K_M")
	if size, _ := CheckRegistry(context.Background(), c, r); size != 807696561 {
		t.Errorf("size = %d, want the manifest's layers + config (807696561)", size)
	}
}

func TestManifestSize(t *testing.T) {
	if got := manifestSize(strings.NewReader("not json")); got != 0 {
		t.Errorf("unreadable manifest size = %d, want 0", got)
	}
}
