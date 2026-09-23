package web

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/library"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
)

const gb = 1_000_000_000

func TestEstimateFit(t *testing.T) {
	now := time.Now()
	discrete := sysinfo.Snapshot{Time: now, MemTotal: 32 * gb, GPUs: []sysinfo.GPU{{Name: "RTX", MemTotal: 12 * gb}}}
	apple := sysinfo.Snapshot{Time: now, MemTotal: 32 * gb, GPUs: []sysinfo.GPU{{Name: "M3", MemTotal: 32 * gb, Unified: true}}}
	cpu := sysinfo.Snapshot{Time: now, MemTotal: 16 * gb}

	for _, tc := range []struct {
		name string
		size int64
		snap sysinfo.Snapshot
		want string
	}{
		{"in VRAM", 5 * gb, discrete, "gpu"},
		{"spills to RAM", 20 * gb, discrete, "partial"},
		{"bigger than both", 40 * gb, discrete, "no"},
		{"apple, in GPU share", 18 * gb, apple, "gpu"},
		{"apple, over GPU share", 24 * gb, apple, "partial"},
		{"apple, too big", 30 * gb, apple, "no"},
		{"no GPU", 8 * gb, cpu, "cpu"},
		{"no GPU, too big", 15 * gb, cpu, "no"},
		{"size unknown", 0, discrete, "unknown"},
		{"hardware unread", 5 * gb, sysinfo.Snapshot{}, "unknown"},
	} {
		if got := estimateFit(tc.size, tc.snap).Level; got != tc.want {
			t.Errorf("%s: level = %s, want %s", tc.name, got, tc.want)
		}
	}

	if f := tagFit(library.Tag{Name: "m:8b-mlx", Size: gb}, discrete); f.Level != "no" {
		t.Errorf("MLX tag on a PC: level = %s, want no", f.Level)
	}
	if f := tagFit(library.Tag{Name: "m:8b-mlx", Size: gb}, apple); f.Level != "gpu" {
		t.Errorf("MLX tag on a Mac: level = %s, want gpu", f.Level)
	}
	if f := tagFit(library.Tag{Name: "m:cloud"}, discrete); f.Level != "cloud" {
		t.Errorf("cloud tag: level = %s, want cloud", f.Level)
	}
	// 70b at ~4 bits is ~42 GB: more than 12 GB VRAM + 32 GB RAM once loaded.
	if f := sizeFit("70b", discrete); f.Level != "no" {
		t.Errorf("70b: level = %s, want no", f.Level)
	}
}

// A search result in ollama.com's markup (see internal/library/testdata).
func libraryResult(name, desc string, chips ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<li class="flex items-baseline"><a href="/library/%s" class="group w-full"><p class="max-w-lg break-words">%s</p>`, name, desc)
	for _, c := range chips {
		fmt.Fprintf(&b, `<span class="inline-flex my-1 items-center rounded-md px-2">%s</span>`, c)
	}
	b.WriteString(`<span >1.2M</span><span class="hidden sm:flex">&nbsp;Pulls</span></a></li>`)
	return b.String()
}

func fakeLibrary(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search" && r.URL.Query().Get("page") == "":
			fmt.Fprint(w, `<ul>`+
				libraryResult("model-000", "Installed already.", "tools", "8b")+
				libraryResult("huge", "Won't fit anywhere.", "4000b")+
				libraryResult("cloudy", "Cloud only.", "cloud")+
				`<li hx-get="/search?page=2" hx-trigger="revealed"></li></ul>`)
		case r.URL.Path == "/search":
			fmt.Fprint(w, `<ul>`+libraryResult("second-page", "Page two.", "1b")+`</ul>`)
		case r.URL.Path == "/library/model-000/tags":
			fmt.Fprint(w, `<div class="hidden md:flex flex-col"><a href="/library/model-000:latest" class="group-hover:underline">model-000:latest</a>
				<p class="col-span-2">5GB</p><p class="col-span-2">128K</p><div class="col-span-2">Text</div>
				<span class="font-mono text-[11px]">aaa111</span>&nbsp;·&nbsp;2 weeks ago</div>
				<div class="hidden md:flex flex-col"><a href="/library/model-000:8b" class="group-hover:underline">model-000:8b</a>
				<p class="col-span-2">5GB</p><p class="col-span-2">128K</p><div class="col-span-2">Text</div>
				<span class="font-mono text-[11px]">aaa111</span>&nbsp;·&nbsp;2 weeks ago</div>
				<div class="hidden md:flex flex-col"><a href="/library/model-000:cloud" class="group-hover:underline">model-000:cloud</a>
				<p class="col-span-2"><span class="block h-1 w-4 rounded-full"></span></p><p class="col-span-2">1M</p><div class="col-span-2">Text</div>
				<span class="font-mono text-[11px]">ccc333</span>&nbsp;·&nbsp;2 weeks ago</div>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverPage(t *testing.T) {
	lib := fakeLibrary(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL = lib.URL })

	// Before the hardware's been read, the fit filter can't judge anything,
	// so it shows everything rather than nothing.
	if body := get(h, "/discover", false).Body.String(); !strings.Contains(body, "huge") {
		t.Error("with hardware unknown, the fit filter hid a model")
	}
	testSampler.SetLatest(sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 * gb, GPUs: []sysinfo.GPU{{Name: "RTX", MemTotal: 12 * gb}}})

	// By default, only models that would run here.
	body := get(h, "/discover", false).Body.String()
	for _, want := range []string{"<!doctype html>", "model-000", "Installed already.", "Installed",
		`hx-get="/discover?page=2"`, "1.2M pulls"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	for _, hidden := range []string{"huge", "cloudy"} {
		if strings.Contains(body, hidden) {
			t.Errorf("default page shows %q, which won't run here", hidden)
		}
	}
	// Unticking the filter (the form's hidden fit=0 alone) shows everything.
	all := get(h, "/discover?fit=0", false).Body.String()
	for _, want := range []string{"huge", "cloudy", `hx-get="/discover?fit=0&amp;page=2"`} {
		if !strings.Contains(all, want) {
			t.Errorf("unfiltered page missing %q", want)
		}
	}
	// Asking for cloud models shows them despite the filter.
	if cloud := get(h, "/discover?c=cloud", false).Body.String(); !strings.Contains(cloud, "cloudy") || strings.Contains(cloud, "huge") {
		t.Errorf("cloud filter: want cloudy and not huge")
	}

	// Filter changes swap just the results.
	frag := get(h, "/discover?q=x", true).Body.String()
	if strings.Contains(frag, "<!doctype html>") || !strings.Contains(frag, `id="discover-results"`) {
		t.Errorf("htmx search didn't return the results fragment:\n%s", frag)
	}
	// Scrolling fetches just the next page's cards.
	more := get(h, "/discover?page=2", true).Body.String()
	if strings.Contains(more, `id="discover-results"`) || !strings.Contains(more, "second-page") || strings.Contains(more, "page=3") {
		t.Errorf("next page fragment wrong:\n%s", more)
	}
}

func TestDiscoverTags(t *testing.T) {
	lib := fakeLibrary(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL = lib.URL })

	body := get(h, "/discover/tags?model=model-000", true).Body.String()
	if !strings.Contains(body, "same as model-000:8b") {
		t.Errorf("latest isn't shown as an alias of 8b:\n%s", body)
	}
	if strings.Contains(body, `value="model-000:latest"`) {
		t.Errorf("installed tag offered for download:\n%s", body)
	}
	if strings.Contains(body, `value="model-000:8b"`) {
		t.Errorf("8b offered for download, though it's the same download as the installed latest:\n%s", body)
	}
	if strings.Contains(body, `value="model-000:cloud"`) || !strings.Contains(body, "model-000:cloud") {
		t.Errorf("cloud tag should be listed without a Download button:\n%s", body)
	}
	if n := strings.Count(body, ">Installed<"); n != 2 {
		t.Errorf("%d tags marked installed, want 2 (latest and its alias 8b)", n)
	}

	if body := get(h, "/discover/tags?model=nope", true).Body.String(); !strings.Contains(body, "nope wasn&#39;t found on ollama.com") {
		t.Errorf("missing model: %s", body)
	}
}

func TestDiscoverSearchError(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL) // library unreachable
	body := get(h, "/discover", false).Body.String()
	if !strings.Contains(body, "Search failed: couldn&#39;t reach ollama.com") {
		t.Errorf("no error shown:\n%s", body)
	}
}

func TestDiscoverStateURL(t *testing.T) {
	st := parseDiscoverState(map[string][]string{"q": {" qwen "}, "c": {"vision", "bogus", "vision", "cloud"}, "o": {"newest"}, "fit": {"0"}, "page": {"3"}})
	if want := "/discover?c=vision&c=cloud&fit=0&o=newest&page=3&q=qwen"; st.URL() != want {
		t.Errorf("URL = %s, want %s", st.URL(), want)
	}
	for fit, want := range map[string]bool{"": true, "0": false, "0,1": true} {
		q := map[string][]string{}
		if fit != "" {
			q["fit"] = strings.Split(fit, ",")
		}
		if got := parseDiscoverState(q).Fit; got != want {
			t.Errorf("fit=%q: Fit = %v, want %v", fit, got, want)
		}
	}
	if got := parseDiscoverState(nil).URL(); got != "/discover" {
		t.Errorf("empty state URL = %s", got)
	}
}

func fakeHF(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			if r.URL.Query().Get("cursor") == "" {
				w.Header().Set("Link", `<`+"http://"+r.Host+`/api/models?cursor=p2>; rel="next"`)
				fmt.Fprint(w, `[
					{"id":"owner/Small-GGUF","downloads":1234567,"likes":42,"pipeline_tag":"text-generation","lastModified":"2026-01-02T00:00:00Z",
					 "gated":false,"tags":["gguf","base_model:quantized:owner/Small"],"gguf":{"total":8000000000,"architecture":"llama","context_length":131072}},
					{"id":"owner/Huge-GGUF","downloads":5,"likes":1,"gated":"manual","gguf":{"total":4000000000000}},
					{"id":"owner/Mystery-GGUF","downloads":5,"likes":1,"gated":false}]`)
				return
			}
			fmt.Fprint(w, `[{"id":"owner/Later-GGUF","downloads":1,"likes":0,"gated":false,"gguf":{"total":1000000000}}]`)
		case "/api/models/owner/Small-GGUF/tree/main":
			fmt.Fprint(w, `[
				{"type":"file","path":"Small-Q4_K_M.gguf","size":5000000000},
				{"type":"file","path":"Small-Q8_0.gguf","size":9000000000},
				{"type":"file","path":"mmproj-F16.gguf","size":600000000}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDiscoverHuggingFace(t *testing.T) {
	hf := fakeHF(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.HFURL = hf.URL })
	testSampler.SetLatest(sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 * gb, GPUs: []sysinfo.GPU{{Name: "RTX", MemTotal: 12 * gb}}})
	if _, err := testStore.EnqueueDownload(t.Context(), "hf.co/owner/small-gguf:q8_0", "admin"); err != nil {
		t.Fatal(err)
	}

	body := get(h, "/discover?src=hf", false).Body.String()
	for _, want := range []string{"owner/Small-GGUF", "A quantisation of <strong>owner/Small</strong>", "8B parameters", "1.2M downloads",
		"128K context", `class="badge queued"`, `hx-get="/discover?cursor=p2&amp;src=hf"`, `aria-current="true">Hugging Face`} {
		if !strings.Contains(body, want) {
			t.Errorf("HF page missing %q", want)
		}
	}
	// Too big, and unknown size, are hidden by the fit filter.
	for _, hidden := range []string{"owner/Huge-GGUF", "owner/Mystery-GGUF"} {
		if strings.Contains(body, hidden) {
			t.Errorf("HF page shows %s", hidden)
		}
	}
	if all := get(h, "/discover?src=hf&fit=0", false).Body.String(); !strings.Contains(all, "owner/Huge-GGUF") || !strings.Contains(all, ">gated<") {
		t.Error("unfiltered HF page should show the huge, gated repo")
	}
	if more := get(h, "/discover?src=hf&cursor=p2", true).Body.String(); !strings.Contains(more, "owner/Later-GGUF") || strings.Contains(more, "cursor=") {
		t.Errorf("next page wrong:\n%s", more)
	}

	files := get(h, "/discover/hf/files?repo=owner/Small-GGUF&ctx=131072", true).Body.String()
	if !strings.Contains(files, `value="hf.co/owner/Small-GGUF:Q4_K_M"`) {
		t.Errorf("Q4_K_M not offered for download:\n%s", files)
	}
	// Q8_0 is queued (under a differently-cased name): its status, not a button.
	if strings.Contains(files, `value="hf.co/owner/Small-GGUF:Q8_0"`) || strings.Count(files, `class="badge queued"`) != 1 {
		t.Errorf("queued Q8_0 should show its status:\n%s", files)
	}
	if strings.Contains(files, "mmproj") {
		t.Error("vision projector listed as a quantisation")
	}
}

func TestDiscoverDownloadStaysOnPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	hx := []string{"HX-Request", "true"}

	rec := do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}, "from": {"discover"}}, testSession, hx...)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `class="badge queued"`) || !strings.Contains(body, `id="nav-dl-badge" class="nav-badge" hx-swap-oob="true"`) {
		t.Fatalf("queueing from discover: %d\n%s", rec.Code, body)
	}

	// Again: already queued, so the button comes back with the reason.
	body = do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}, "from": {"discover"}}, testSession, hx...).Body.String()
	if !strings.Contains(body, "already queued or downloading") || !strings.Contains(body, `value="qwen3:8b"`) {
		t.Errorf("duplicate: %s", body)
	}

	// Too big: a dialog that confirms in place.
	body = do(h, "POST", "/downloads", url.Values{"model": {"huge-model:70b"}, "from": {"discover"}}, testSession, hx...).Body.String()
	if !strings.Contains(body, "<dialog data-dialog-autoopen") || !strings.Contains(body, `name="confirm" value="1"`) {
		t.Errorf("too big: %s", body)
	}
	if active, _ := testStore.ActiveDownloads(t.Context()); len(active) != 1 {
		t.Errorf("want only qwen3:8b queued, got %+v", active)
	}

	// Without htmx it still lands on the downloads page.
	if rec := do(h, "POST", "/downloads", url.Values{"model": {"llama3.2"}, "from": {"discover"}}, testSession); rec.Code != http.StatusSeeOther {
		t.Errorf("plain post = %d, want a redirect", rec.Code)
	}
}
