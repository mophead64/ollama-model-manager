package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

func TestComputeStorage(t *testing.T) {
	const gb = 1 << 30
	all := []ollama.Model{
		{Name: "llama3.2:latest", Digest: "d1", Size: 2 * gb},
		{Name: "llama3.2:3b", Digest: "d1", Size: 2 * gb},         // the same model
		{Name: "llama3.2-32k:latest", Digest: "d2", Size: 2 * gb}, // a variant: same weights, new params
		{Name: "qwen3:8b", Digest: "d3", Size: 5 * gb},
	}
	layers := map[string][]layer{
		"d1": {{"weights-a", 2*gb - 100}, {"params-a", 100}},
		"d2": {{"weights-a", 2*gb - 100}, {"params-b", 100}},
		"d3": {{"weights-q", 5 * gb}},
	}
	st := computeStorage(all, func(m ollama.Model) []layer { return layers[m.Digest] })

	if st.Models != 3 || st.Tags != 4 || !st.Exact {
		t.Errorf("models %d, tags %d, exact %v; want 3, 4, true", st.Models, st.Tags, st.Exact)
	}
	if want := int64(2*gb + 100 + 5*gb); st.Bytes != want { // weights-a once
		t.Errorf("bytes = %d, want %d", st.Bytes, want)
	}
	for _, tc := range []struct {
		names []string
		want  int64
	}{
		{[]string{"llama3.2:latest"}, 0},                  // llama3.2:3b keeps it
		{[]string{"llama3.2:latest", "llama3.2:3b"}, 100}, // the variant keeps the weights
		{[]string{"llama3.2-32k:latest"}, 100},            // just its params
		{[]string{"llama3.2:latest", "llama3.2:3b", "llama3.2-32k:latest"}, 2*gb + 100},
		{[]string{"qwen3:8b"}, 5 * gb},
	} {
		if got := st.Freed(tc.names...); got != tc.want {
			t.Errorf("Freed(%v) = %d, want %d", tc.names, got, tc.want)
		}
	}
	if got := st.DeleteEffect("llama3.2:latest"); got != "but that frees no space: llama3.2:3b is the same model and keeps its files" {
		t.Errorf("alias: %q", got)
	}
	if got := st.DeleteEffect("qwen3:8b"); got != "freeing 5.0 GB" {
		t.Errorf("exact: %q", got)
	}

	// Without the manifests, only same-digest tags are known to share.
	st = computeStorage(all, func(ollama.Model) []layer { return nil })
	if st.Models != 3 || st.Exact || st.Bytes != 9*gb {
		t.Errorf("inexact: models %d, exact %v, bytes %d", st.Models, st.Exact, st.Bytes)
	}
	if got := st.DeleteEffect("llama3.2-32k:latest"); got != "freeing up to 2.0 GB" {
		t.Errorf("inexact: %q", got)
	}
}

// writeManifest puts a model's manifest where Ollama keeps it, returning its digest.
func writeManifest(t *testing.T, dir, host, ns, model, tag, body string) string {
	t.Helper()
	p := filepath.Join(dir, "manifests", host, ns, model, tag)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestReadManifestLayers(t *testing.T) {
	dir := t.TempDir()
	body := `{"config":{"digest":"sha256:cfg","size":500},"layers":[{"digest":"sha256:w","size":1000},{"digest":"sha256:t","size":20}]}`
	d := writeManifest(t, dir, "registry.ollama.ai", "library", "llama3.2", "latest", body)
	hf := writeManifest(t, dir, "hf.co", "Owner", "Some-GGUF", "Q4_K_M", body)

	got := readManifestLayers(dir, ollama.Model{Name: "llama3.2:latest", Digest: d})
	if len(got) != 3 || got[0] != (layer{"sha256:w", 1000}) || got[2] != (layer{"sha256:cfg", 500}) {
		t.Errorf("layers = %+v", got)
	}
	if got := readManifestLayers(dir, ollama.Model{Name: "hf.co/Owner/Some-GGUF:Q4_K_M", Digest: hf}); len(got) != 3 {
		t.Errorf("hf.co layers = %+v", got)
	}
	// A manifest that isn't the one Ollama listed (changed since) isn't trusted.
	if got := readManifestLayers(dir, ollama.Model{Name: "llama3.2:latest", Digest: "0000"}); got != nil {
		t.Errorf("digest mismatch: %+v", got)
	}
	if got := readManifestLayers(dir, ollama.Model{Name: "missing:latest", Digest: d}); got != nil {
		t.Errorf("missing: %+v", got)
	}
}

// Two tags of one model count once on the models page.
func TestStorageOnModelsPage(t *testing.T) {
	dir := t.TempDir()
	body := `{"config":{"digest":"sha256:cfg","size":24},"layers":[{"digest":"sha256:w","size":1000}]}`
	d := writeManifest(t, dir, "registry.ollama.ai", "library", "llama3.2", "latest", body)
	writeManifest(t, dir, "registry.ollama.ai", "library", "llama3.2", "3b", body)
	ol := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"llama3.2:latest","size":1024,"digest":"` + d + `"},{"name":"llama3.2:3b","size":1024,"digest":"` + d + `"}]}`))
		case "/api/ps":
			w.Write([]byte(`{"models":[]}`))
		}
	}))
	t.Cleanup(ol.Close)
	h := newTestServer(t, ol.URL, func(c *Config) { c.ModelsDir = dir })

	body = get(h, "/models", false).Body.String()
	for _, want := range []string{
		`<div class="value">1</div><div class="label">models installed <span class="tip-wrap" tabindex="0">(2 tags)`,
		`<div class="value">1.0 KB</div><div class="label">total size on disk</div>`,
		`data-fill-effect="but that frees no space: llama3.2:3b is the same model and keeps its files"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("models page missing %q", want)
		}
	}
}
