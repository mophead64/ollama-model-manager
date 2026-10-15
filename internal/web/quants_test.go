package web

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/library"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

func TestSourceOf(t *testing.T) {
	for name, want := range map[string]modelSource{
		"qwen3:8b":                    {Site: "ollama.com", URL: "https://ollama.com/library/qwen3", QuantsURL: "/discover/quants?name=qwen3%3A8b"},
		"user/custom:v1":              {Site: "ollama.com", URL: "https://ollama.com/user/custom"}, // tags can't be listed
		"hf.co/owner/Small-GGUF:Q8_0": {Site: "Hugging Face", URL: "https://huggingface.co/owner/Small-GGUF", QuantsURL: "/discover/quants?name=hf.co%2Fowner%2FSmall-GGUF%3AQ8_0"},
		"example.com/team/model:1":    {},
	} {
		if got := sourceOf(name); got != want {
			t.Errorf("sourceOf(%q) = %+v, want %+v", name, got, want)
		}
	}
}

func TestOtherQuants(t *testing.T) {
	lib, hf := fakeLibrary(t), fakeHF(t)
	// fakeOllama's /api/show only knows user/custom:v1; model-000's page is needed too.
	fake := fakeOllama(t, 1)
	target, _ := url.Parse(fake.URL)
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			w.Write([]byte(`{"details":{"family":"llama"},"capabilities":["completion"],"model_info":{}}`))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h := newTestServer(t, srv.URL, func(c *Config) { c.LibraryURL, c.HFURL = lib.URL, hf.URL })
	testStore.MarkModelUsed(t.Context(), "model-000:latest", time.Now())

	// On each model menu and the model page: other quants where they can be
	// listed, and a link to the model's page.
	opener := `data-dialog-open="quants-modal"` + "\n" + `  data-fill-name="model-000:latest" data-fill-site="ollama.com" data-fill-url="https://ollama.com/library/model-000"`
	for _, path := range []string{"/", "/models", "/models/model-000:latest"} {
		body := get(h, path, false).Body.String()
		for _, want := range []string{opener, `hx-get="/discover/quants?name=model-000%3Alatest"`, `href="https://ollama.com/library/model-000"`, `id="quants-modal"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
	// A namespaced ollama.com model: a link, but its tags can't be listed.
	list := get(h, "/models", false).Body.String()
	if !strings.Contains(list, `href="https://ollama.com/user/custom"`) || strings.Contains(list, `data-fill-name="user/custom:v1" data-fill-site`) {
		t.Error("user/custom should have a View link and no Other quants")
	}

	// The dialog's list: Discover's, for that model, with the installed one marked.
	body := get(h, "/discover/quants?name=model-000:latest", true).Body.String()
	if !strings.Contains(body, "model-000:8b") || !strings.Contains(body, ">Installed<") || strings.Contains(body, "<!doctype html>") {
		t.Errorf("ollama.com quants wrong:\n%s", body)
	}
	body = get(h, "/discover/quants?name=hf.co/owner/Small-GGUF:Q8_0", true).Body.String()
	if !strings.Contains(body, `value="hf.co/owner/Small-GGUF:Q4_K_M"`) {
		t.Errorf("Hugging Face quants wrong:\n%s", body)
	}
	if body := get(h, "/discover/quants?name=user/custom:v1", true).Body.String(); !strings.Contains(body, "can only be listed for models from the ollama.com library or Hugging Face") {
		t.Errorf("unlistable model: %s", body)
	}
}

func TestQuantOf(t *testing.T) {
	for name, want := range map[string]string{
		"hf.co/unsloth/Qwen3-8B-GGUF:Q4_K_M":       "Q4_K_M",
		"hf.co/unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL":   "UD-Q4_K_XL",
		"gemma3:27b-it-q4_K_M":                     "q4_K_M",
		"llama3.2:3b-instruct-fp16":                "fp16",
		"qwen3:8b-q8_0":                            "q8_0",
		"gemma3:4b-it-qat":                         "qat",
		"qwen3:30b-a3b-iq3_xxs":                    "iq3_xxs",
		"hf.co/x/y-GGUF:Q5_K_P":                    "Q5_K_P",
		"qwen3:8b":                                 "", // ollama.com's default: the page doesn't say
		"qwen3:latest":                             "",
		"hf.co/google/gemma-3-4b-it-qat-q4_0-gguf": "", // the repo's only file, named for nothing
	} {
		if got := quantOf(name); got != want {
			t.Errorf("quantOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDescribeQuant(t *testing.T) {
	for q, want := range map[string]string{
		"Q4_K_M":     "4-bit k-quant, medium: the usual balance of size and quality (Ollama's default)",
		"Q4_K_S":     "4-bit k-quant, small: a good balance of size and quality",
		"Q5_K_M":     "5-bit k-quant, medium: close to the original, for a little more size than 4-bit",
		"Q6_K":       "6-bit k-quant: very close to the original",
		"Q8_0":       "8-bit quant: practically the same as the original, at about twice the size of 4-bit",
		"Q4_0":       "4-bit legacy quant: an older method; a 4-bit k-quant of the same size is usually better",
		"Q3_K_L":     "3-bit k-quant, large: noticeably less accurate; for fitting a bigger model in",
		"Q2_K":       "2-bit k-quant: a big loss in quality; a last resort for fitting a model in",
		"IQ3_XXS":    "3-bit i-quant, extra extra small: noticeably less accurate; for fitting a bigger model in",
		"IQ4_NL":     "4-bit i-quant, non-linear: about as good as a 4-bit k-quant, a little smaller; can be slower on a CPU",
		"Q4_0_4_4":   "4-bit legacy quant: an older method; a 4-bit k-quant of the same size is usually better. Repacked for ARM CPUs, to run faster on them",
		"Q5_K_P":     "5-bit k-quant: close to the original, for a little more size than 4-bit. “_P” is this uploader's own variant of Q5_K, not a standard llama.cpp size",
		"BF16":       "16-bit, unquantised: full quality, but about 4× the size of a 4-bit quant",
		"fp16":       "16-bit, unquantised: full quality, but about 4× the size of a 4-bit quant",
		"MXFP4":      "4-bit floating point (MXFP4): the format some models, such as gpt-oss, are released in",
		"qat":        "Quantisation-aware trained: trained to run at 4 bits, so it loses less than an ordinary 4-bit quant",
		"UD-Q4_K_XL": "4-bit k-quant, extra large: a good balance of size and quality. Unsloth Dynamic: key layers kept more precise, so better than a plain quant this size",
		"":           "",
		"Q9_K":       "",
		"banana":     "",
	} {
		if got := describeQuant(q); got != want {
			t.Errorf("describeQuant(%q) =\n  %q\nwant\n  %q", q, got, want)
		}
	}
}

// "8b" doesn't say its quantisation, but when it's the same download as
// "8b-q4_K_M" it's explained as that.
func TestTagRowsShareQuant(t *testing.T) {
	rows := tagRows([]library.Tag{
		{Name: "m:8b", Digest: "aa"},
		{Name: "m:8b-q4_K_M", Digest: "aa"},
		{Name: "m:8b-q8_0", Digest: "bb"},
		{Name: "m:cloud", Digest: "cc"},
	}, localModels{}, sysinfo.Snapshot{})
	want := []string{describeQuant("q4_K_M"), describeQuant("q4_K_M"), describeQuant("q8_0"), ""}
	for i, r := range rows {
		if r.Quant != want[i] {
			t.Errorf("%s: quant = %q, want %q", r.Name, r.Quant, want[i])
		}
	}
}
