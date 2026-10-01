package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

// The Settings page's Ollama settings panel. Ollama reads its settings from
// environment variables when it starts and has no API to report them, so the
// panel shows what can be seen from here (whether Ollama answers on the
// network, the context loaded models got, the models folder), suggests values
// for this machine's hardware, and gives copyable steps for each way of
// running Ollama, as the System page does for updating it.

// ollamaSetting is one of Ollama's environment variables, as the panel shows it.
type ollamaSetting struct {
	Env      string // e.g. "OLLAMA_FLASH_ATTENTION"
	Label    string
	Default  string // Ollama's default
	Suggest  string // the value suggested for this machine; "" to leave it as it is
	Why      string
	Detected string // what can be seen of it from here, if anything
}

// exposure is whether Ollama can be reached from other devices.
type exposure struct {
	// "local": it only answers on this machine. "network": it also answers on
	// one of this machine's network addresses (URLs). "remote": this app reaches
	// it over the network, so it listens beyond localhost. "container": this app
	// runs in Docker and reaches the host through host.docker.internal, from
	// where there's no telling.
	Level string
	URLs  []string
}

// copyBlock is a multi-line snippet with a Copy button.
type copyBlock struct {
	ID, Text string
	Rows     int
}

func newCopyBlock(id string, lines []string) copyBlock {
	return copyBlock{ID: id, Text: strings.Join(lines, "\n"), Rows: len(lines)}
}

// handleOllamaSettings fills in the Settings page's Ollama settings panel,
// loaded after the page since it asks Ollama and checks the network.
func (s *Server) handleOllamaSettings(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Error(w, "only the admin can see Ollama's settings", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	snap := s.sys.Latest()

	var loaded []string
	if running, err := s.ol.Running(ctx); err == nil {
		for _, m := range running {
			if m.ContextLength > 0 {
				loaded = append(loaded, fmt.Sprintf("%s: %s", m.Name, formatCount(m.ContextLength)))
			}
		}
	}
	exp := s.ollamaExposure(ctx)
	var free string
	if d := s.diskUsage(); d != nil {
		free = fmt.Sprintf("%s free of %s", formatBytes(int64(d.Free)), formatBytes(int64(d.Total)))
	}
	settings := suggestOllamaSettings(snap, exp, loaded, s.cfg.ModelsDir, free)

	var env [][2]string
	for _, st := range settings {
		if st.Suggest != "" && st.Env != "OLLAMA_HOST" && st.Env != "OLLAMA_MODELS" {
			env = append(env, [2]string{st.Env, st.Suggest})
		}
	}
	data := map[string]any{
		"OllamaURL": s.ol.BaseURL(),
		"Exposure":  exp,
		"Settings":  settings,
		"Hardware":  hardwareSummary(snap),
		"EnvLines":  env,
	}
	if v, err := s.ol.Version(ctx); err == nil {
		data["OllamaVersion"] = v
	}
	if len(env) > 0 {
		systemd := []string{"[Service]"}
		compose := []string{"    environment:"}
		var run, launchctl, setx []string
		for _, e := range env {
			systemd = append(systemd, fmt.Sprintf(`Environment="%s=%s"`, e[0], e[1]))
			compose = append(compose, fmt.Sprintf(`      %s: "%s"`, e[0], e[1]))
			run = append(run, fmt.Sprintf("-e %s=%s", e[0], e[1]))
			launchctl = append(launchctl, fmt.Sprintf("launchctl setenv %s %s", e[0], e[1]))
			setx = append(setx, fmt.Sprintf("setx %s %s", e[0], e[1]))
		}
		data["Systemd"] = newCopyBlock("os-systemd", systemd)
		data["Compose"] = newCopyBlock("os-compose", compose)
		data["DockerRun"] = strings.Join(run, " ")
		data["Launchctl"] = strings.Join(launchctl, " && ")
		data["Setx"] = newCopyBlock("os-setx", setx)
	}
	s.render(w, r, "ollama_settings", data)
}

// suggestOllamaSettings describes each setting, with values suggested for the
// hardware in snap and what can be seen from here.
func suggestOllamaSettings(snap sysinfo.Snapshot, exp exposure, loadedCtx []string, modelsDir, diskFree string) []ollamaSetting {
	known := !snap.Time.IsZero() && snap.MemTotal > 0 // hardware read yet
	gpuMem := gpuMemory(snap)
	gb := float64(gpuMem) / (1 << 30)

	ctxSuggest, ctxWhy := "", "How much text a model can take in at once. More lets it read longer documents and conversations, but the memory it takes grows with it."
	switch {
	case !known:
	case gpuMem == 0:
		ctxWhy += " Ollama's default suits a machine without a GPU: long contexts are slow on the CPU."
	case gb < 10:
		ctxSuggest = "8192"
	case gb < 20:
		ctxSuggest = "16384"
	case gb < 40:
		ctxSuggest = "32768"
	default:
		ctxSuggest = "65536"
	}
	if ctxSuggest != "" {
		ctxWhy = fmt.Sprintf("How much text a model can take in at once. More lets it read longer documents and conversations, but the memory it takes grows with it, and a model that no longer fits spills onto the CPU. A starting point for %s of GPU memory, with the KV cache setting below; a model's own settings can still ask for more or less.", formatBytes(int64(gpuMem)))
	}
	ctxDetected := "Load a model to see the context Ollama gives it."
	if len(loadedCtx) > 0 {
		ctxDetected = "Loaded now: " + strings.Join(loadedCtx, ", ")
	}

	parallel, parallelWhy := "", "How many requests a model works on at once. Each one reserves another context's worth of memory."
	if gpuMem > 0 && gb >= 24 {
		parallelWhy += " With this much GPU memory there's room for a few, for several people or apps at once."
	}
	if gpuMem > 0 && gb < 24 {
		parallel = "1"
		parallelWhy = "How many requests a model works on at once. Each one reserves another context's worth of memory, so on this much GPU memory 1 leaves the most room for context. Others wait their turn."
	}

	host := ollamaSetting{
		Env: "OLLAMA_HOST", Label: "Listen address", Default: "127.0.0.1:11434",
		Why: "Where Ollama listens. 0.0.0.0 lets any device that can reach this machine use it, and Ollama has no password: they can run, download and delete models. Keep it on 127.0.0.1 unless other devices need it, and then limit who can reach the port.",
	}
	switch exp.Level {
	case "local":
		host.Detected = "Only answers on this machine."
	case "network":
		host.Detected = "Also answers on the network, at " + strings.Join(exp.URLs, ", ") + "."
		host.Suggest = "127.0.0.1:11434"
		host.Why += " If other devices do need it, leave it and restrict the port with a firewall instead."
	case "remote":
		host.Detected = "This app reaches it over the network, so it listens beyond localhost. It has to, for this app to reach it."
	case "container":
		host.Detected = "This app runs in Docker and reaches Ollama through host.docker.internal, so it can't tell whether other devices can."
	}

	models := ollamaSetting{
		Env: "OLLAMA_MODELS", Label: "Models folder", Default: "~/.ollama/models (Linux service: /usr/share/ollama/.ollama/models)",
		Why: "Where downloaded models are kept. Point it at a bigger disk if space runs short, then move the folder's contents there (or download them again).",
	}
	if modelsDir != "" {
		models.Detected = modelsDir
		if diskFree != "" {
			models.Detected += " (" + diskFree + ")"
		}
	}

	return []ollamaSetting{
		{Env: "OLLAMA_CONTEXT_LENGTH", Label: "Context length", Default: "4096 (newer versions may choose more on big GPUs)",
			Suggest: ctxSuggest, Why: ctxWhy, Detected: ctxDetected},
		{Env: "OLLAMA_FLASH_ATTENTION", Label: "Flash attention", Default: "off (newer versions turn it on for models that support it)",
			Suggest: map[bool]string{true: "1"}[gpuMem > 0],
			Why:     "A faster way of working through the context that takes less memory at long contexts. A quantised KV cache (below) needs it."},
		{Env: "OLLAMA_KV_CACHE_TYPE", Label: "KV cache type", Default: "f16",
			Suggest: map[bool]string{true: "q8_0"}[gpuMem > 0],
			Why:     "How the context is stored in memory. q8_0 halves the memory it takes, with little effect on quality, so a longer context fits; q4_0 saves more but costs quality. Needs flash attention."},
		{Env: "OLLAMA_NUM_PARALLEL", Label: "Parallel requests", Default: "1 (older versions: up to 4)",
			Suggest: parallel, Why: parallelWhy},
		{Env: "OLLAMA_KEEP_ALIVE", Label: "Keep alive", Default: "5m",
			Why: "How long a model stays loaded after its last request. Longer means fewer slow first replies while you're using it, but the memory stays taken meanwhile. It depends on how you use it, so no suggestion; -1 keeps models loaded."},
		{Env: "OLLAMA_MAX_LOADED_MODELS", Label: "Models loaded at once", Default: "3 per GPU",
			Why: "How many models can be in memory together, when they fit. Lower it if models keep crowding each other onto the CPU."},
		host,
		models,
	}
}

// gpuMemory is the memory models can use on the GPU: VRAM, or the share of
// unified memory macOS lets the GPU have. 0 without a GPU.
func gpuMemory(snap sysinfo.Snapshot) uint64 {
	if slices.ContainsFunc(snap.GPUs, func(g sysinfo.GPU) bool { return g.Unified }) {
		return uint64(float64(snap.MemTotal) * unifiedGPUShare)
	}
	if _, vram, ok := snap.VRAM(); ok {
		return vram
	}
	return 0
}

// ollamaExposure works out whether Ollama can be reached from other devices.
// When this app reaches it on localhost, it tries this machine's network
// addresses on the same port.
func (s *Server) ollamaExposure(ctx context.Context) exposure {
	u, err := url.Parse(s.ol.BaseURL())
	if err != nil || u.Hostname() == "" {
		return exposure{}
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "11434"
	}
	if strings.EqualFold(host, "host.docker.internal") {
		return exposure{Level: "container"}
	}
	if !isLoopback(ctx, host) {
		return exposure{Level: "remote"}
	}

	addrs := s.lanAddrs()
	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		urls   []string
		client = &http.Client{Timeout: time.Second}
	)
	for _, a := range addrs {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			base := "http://" + net.JoinHostPort(a, port)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/version", nil)
			if err != nil {
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				mu.Lock()
				urls = append(urls, base)
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	if len(urls) == 0 {
		return exposure{Level: "local"}
	}
	slices.Sort(urls)
	return exposure{Level: "network", URLs: urls}
}

// isLoopback reports whether host is this machine: "localhost", or an address
// or name that resolves only to loopback addresses.
func isLoopback(ctx context.Context, host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil || len(ips) == 0 {
		return strings.EqualFold(host, "localhost")
	}
	for _, ip := range ips {
		if !ip.IP.IsLoopback() {
			return false
		}
	}
	return true
}

// localNetworkAddrs are this machine's addresses other devices could reach it
// on: up interfaces' IPv4 and global IPv6 addresses, not loopback or
// link-local. At most 8, so the check stays quick on busy Docker hosts.
func localNetworkAddrs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || !ipn.IP.IsGlobalUnicast() {
				continue
			}
			out = append(out, ipn.IP.String())
			if len(out) == 8 {
				return out
			}
		}
	}
	return out
}
