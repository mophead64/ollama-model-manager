package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

func TestSuggestOllamaSettings(t *testing.T) {
	const gib = 1 << 30
	nvidia := func(gpus ...uint64) sysinfo.Snapshot {
		s := sysinfo.Snapshot{Time: time.Now(), MemTotal: 64 * gib}
		for _, m := range gpus {
			s.GPUs = append(s.GPUs, sysinfo.GPU{Name: "RTX", MemTotal: m})
		}
		return s
	}
	mac := sysinfo.Snapshot{Time: time.Now(), MemTotal: 24 * gib, GPUs: []sysinfo.GPU{{Name: "Apple M4", Unified: true}}}
	cpuOnly := sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 * gib}

	for _, c := range []struct {
		name                     string
		snap                     sysinfo.Snapshot
		ctx, flash, kv, parallel string
	}{
		{"two 12 GB GPUs", nvidia(12*gib, 12*gib), "32768", "1", "q8_0", ""},
		{"one 12 GB GPU", nvidia(12 * gib), "16384", "1", "q8_0", "1"},
		{"8 GB GPU", nvidia(8 * gib), "8192", "1", "q8_0", "1"},
		{"48 GB of GPUs", nvidia(24*gib, 24*gib), "65536", "1", "q8_0", ""},
		{"24 GB Mac (18 GB for the GPU)", mac, "16384", "1", "q8_0", "1"},
		{"no GPU", cpuOnly, "", "", "", ""},
		{"hardware not read yet", sysinfo.Snapshot{}, "", "", "", ""},
	} {
		got := map[string]string{}
		for _, s := range suggestOllamaSettings(c.snap, exposure{Level: "local"}, nil, "", "") {
			got[s.Env] = s.Suggest
		}
		want := map[string]string{
			"OLLAMA_CONTEXT_LENGTH": c.ctx, "OLLAMA_FLASH_ATTENTION": c.flash, "OLLAMA_KV_CACHE_TYPE": c.kv,
			"OLLAMA_NUM_PARALLEL": c.parallel, "OLLAMA_KEEP_ALIVE": "", "OLLAMA_MAX_LOADED_MODELS": "", "OLLAMA_HOST": "",
		}
		for env, w := range want {
			if got[env] != w {
				t.Errorf("%s: %s = %q, want %q", c.name, env, got[env], w)
			}
		}
	}
}

func TestSuggestOllamaSettingsDetected(t *testing.T) {
	snap := sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 << 30}
	exposed := exposure{Level: "network", URLs: []string{"http://192.168.1.10:11434"}}
	byEnv := map[string]ollamaSetting{}
	for _, s := range suggestOllamaSettings(snap, exposed, []string{"qwen3:8b: 4,096"}, "/models", "68 GB free of 460 GB") {
		byEnv[s.Env] = s
	}
	if h := byEnv["OLLAMA_HOST"]; h.Suggest != "127.0.0.1:11434" || !strings.Contains(h.Detected, "http://192.168.1.10:11434") {
		t.Errorf("exposed host = %+v", h)
	}
	if c := byEnv["OLLAMA_CONTEXT_LENGTH"]; c.Detected != "Loaded now: qwen3:8b: 4,096" {
		t.Errorf("context detected = %q", c.Detected)
	}
	if m := byEnv["OLLAMA_MODELS"]; m.Detected != "/models (68 GB free of 460 GB)" {
		t.Errorf("models detected = %q", m.Detected)
	}
}

func TestOllamaExposure(t *testing.T) {
	fake := fakeOllama(t, 1) // listens on 127.0.0.1
	s := &Server{ol: ollama.New(fake.URL)}
	ctx := context.Background()

	s.lanAddrs = func() []string { return nil }
	if got := s.ollamaExposure(ctx); got.Level != "local" {
		t.Errorf("nothing else answering: %+v", got)
	}
	// Pretend 127.0.0.1 is one of this machine's network addresses: Ollama
	// answering there is Ollama answering on the network.
	s.lanAddrs = func() []string { return []string{"127.0.0.1"} }
	if got := s.ollamaExposure(ctx); got.Level != "network" || len(got.URLs) != 1 || got.URLs[0] != fake.URL {
		t.Errorf("answering on a network address: %+v", got)
	}

	for url, want := range map[string]string{
		"http://192.0.2.10:11434":           "remote",
		"http://host.docker.internal:11434": "container",
	} {
		s := &Server{ol: ollama.New(url), lanAddrs: func() []string { t.Error("shouldn't probe"); return nil }}
		if got := s.ollamaExposure(ctx); got.Level != want {
			t.Errorf("%s: level %q, want %q", url, got.Level, want)
		}
	}
}

func TestOllamaSettingsPanel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	testServer.lanAddrs = func() []string { return nil }
	testSampler.SetLatest(sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 << 30, GPUs: []sysinfo.GPU{{Name: "RTX 3060", MemTotal: 12 << 30}}})

	if page := get(h, "/account", false).Body.String(); !strings.Contains(page, `hx-get="/account/ollama"`) {
		t.Error("the Settings page should load the Ollama settings panel")
	}
	panel := get(h, "/account/ollama", true).Body.String()
	for _, want := range []string{
		"this is Ollama 0.12.3", "RTX 3060, 12.0 GB VRAM",
		"OLLAMA_CONTEXT_LENGTH", `<code class="os-suggest">16384</code>`, "OLLAMA_FLASH_ATTENTION", "Only answers on this machine.",
		// Loaded models (the fake reports user/custom:v1 with an 8K context).
		"Loaded now: user/custom:v1: 8,192",
		// The steps carry the suggested values, but not the listen address.
		`Environment=&#34;OLLAMA_KV_CACHE_TYPE=q8_0&#34;`, `OLLAMA_NUM_PARALLEL: &#34;1&#34;`,
		`value="launchctl setenv OLLAMA_CONTEXT_LENGTH 16384 &amp;&amp; launchctl setenv OLLAMA_FLASH_ATTENTION 1`,
		"setx OLLAMA_FLASH_ATTENTION 1", `value="sudo systemctl edit ollama"`,
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel missing %q", want)
		}
	}
	if strings.Contains(panel, "Ollama is open to your network") || strings.Contains(panel, "Environment=&#34;OLLAMA_HOST") {
		t.Error("nothing's exposed here, and the steps should never change the listen address")
	}
}

func TestOllamaSettingsBeforeHardwareIsRead(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL) // the sampler hasn't taken a sample
	testServer.lanAddrs = func() []string { return nil }
	panel := get(h, "/account/ollama", true).Body.String()
	if !strings.Contains(panel, "will suggest values once this machine's hardware has been read") {
		t.Error("the panel should say the hardware hasn't been read yet")
	}
	if strings.Contains(panel, "without a GPU") || strings.Contains(panel, "os-suggest") {
		t.Error("unread hardware isn't the same as no GPU: nothing should be suggested")
	}
	if !strings.Contains(panel, `<code title="Context length">OLLAMA_CONTEXT_LENGTH</code>`) {
		t.Error("each setting is shown by its variable, named in a tooltip")
	}
}
