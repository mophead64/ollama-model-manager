package downloads

import (
	"strings"
	"testing"
)

func titles(hs []Hint) string {
	var t []string
	for _, h := range hs {
		t = append(t, h.Title)
	}
	return strings.Join(t, " | ")
}

func TestSuggest(t *testing.T) {
	cases := []struct {
		model, err string
		warnings   []string
		want       string
	}{
		{"hf.co/liodon-ai/DeepHat-V1-7B-imatrix-GGUF:Q8_0",
			`Head "https://us.aws.cdn.hf.co/xet-bridge-us/6a51?x=1": blocked redirect to a different host`, nil, "Update Ollama"},
		{"qwen9:latest", "pull model manifest: 412: The model you are attempting to pull requires a newer version of Ollama.", nil, "Update Ollama"},
		{"hf.co/meta-llama/Llama-3.2-1B:latest", "", []string{"Gated Hugging Face model: access has to be granted before Ollama can pull it"}, "Gated or private Hugging Face model"},
		{"hf.co/org/private-GGUF:Q4_K_M", "pull model manifest: 401: Unauthorized", nil, "Gated or private Hugging Face model"},
		{"gemma3:typo", "pull model manifest: file does not exist", nil, "Model or tag not found"},
		{"llama3.2:latest", "write /root/.ollama/models/blobs/sha256-x-partial: no space left on device", nil, "Out of disk space"},
		{"llama3.2:latest", "no progress from Ollama for 10m0s", nil, "Network problem"},
		{"llama3.2:latest", `contact ollama at http://localhost:11434: dial tcp: connection refused`, nil, "Couldn't reach Ollama"},
		{"llama3.2:latest", "something odd", nil, "Not sure what went wrong"},
	}
	for _, c := range cases {
		hs := Suggest(c.model, c.err, c.warnings, "0.34.2")
		if len(hs) == 0 || hs[0].Title != c.want {
			t.Errorf("Suggest(%q, %q) = [%s], want %q first", c.model, c.err, titles(hs), c.want)
		}
	}

	if hs := Suggest("x:latest", "", nil, ""); len(hs) != 0 {
		t.Errorf("no error and no warnings should give no hints, got [%s]", titles(hs))
	}
	up := Suggest("hf.co/a/b:Q8_0", "blocked redirect to a different host", nil, "0.34.2")[0]
	if !strings.Contains(up.Text, "v0.34.2") || up.Links[0].URL != "https://ollama.com/download" {
		t.Errorf("update hint = %+v", up)
	}
	gated := Suggest("hf.co/meta-llama/Llama-3.2-1B:latest", "401 unauthorized", nil, "")[0]
	if !strings.Contains(strings.Join(gated.Commands, "\n"), "hf auth login") || gated.Links[0].URL != "https://huggingface.co/meta-llama/Llama-3.2-1B" {
		t.Errorf("gated hint = %+v", gated)
	}
	nf := Suggest("gemma3:typo", "file does not exist", nil, "")[0]
	if nf.Links[0].URL != "https://ollama.com/library/gemma3/tags" {
		t.Errorf("not-found hint links = %+v", nf.Links)
	}
}
