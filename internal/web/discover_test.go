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
	"github.com/mophead64/ollama-model-manager/internal/store"
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
// Each chip is "cloud", a size ("8b") or a capability ("tools").
func libraryResult(name, desc string, chips ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<li class="border-b"><a href="/library/%s" class="group flex"><div class="min-w-0 flex-1"><h2 title="%[1]s"><span >%[1]s</span></h2>`, name)
	fmt.Fprintf(&b, `<p class="mt-1 max-w-2xl truncate" title="%s">%[1]s</p><div class="mt-3 flex">`, desc)
	var sizes []string
	for _, c := range chips {
		switch {
		case c == "cloud":
			b.WriteString(`<span class="group/tip relative inline-flex"><span role="tooltip" class="absolute">Runs on Ollama’s cloud</span><svg viewBox="0 0 24 24"><path d="M0 0"/></svg>Cloud</span>`)
		case c[len(c)-1] == 'b':
			sizes = append(sizes, `<span  class="font-medium text-black">`+c+`</span>`)
		default:
			fmt.Fprintf(&b, `<span  class="inline-flex items-center gap-1.5"><svg viewBox="0 0 24 24"><path d="M0 0"/></svg>%s</span>`, strings.ToUpper(c[:1])+c[1:])
		}
	}
	if sizes != nil {
		b.WriteString(`<span class="group/tip relative inline-flex items-center gap-1.5"><span role="tooltip" class="absolute">Runs on your computer</span>` +
			strings.Join(sizes, `<span class="text-black/50" aria-hidden="true">·</span>`) + `</span>`)
	}
	b.WriteString(`</div></div><span class="inline-flex tabular-nums" title="1,234,567 downloads"><svg viewBox="0 0 24 24"><path d="M0 0"/></svg><span >1.2M</span></span></a></li>`)
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
		case r.URL.Path == "/search" && r.Header.Get("HX-Request") != "true":
			// Like ollama.com: later pages only for its own htmx requests.
			http.Redirect(w, r, "/search", http.StatusSeeOther)
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
	if body := get(h, "/discover?src=ollama", false).Body.String(); !strings.Contains(body, "huge") {
		t.Error("with hardware unknown, the fit filter hid a model")
	}
	testSampler.SetLatest(sysinfo.Snapshot{Time: time.Now(), MemTotal: 32 * gb, GPUs: []sysinfo.GPU{{Name: "RTX", MemTotal: 12 * gb}}})

	// By default, only models that would run here.
	body := get(h, "/discover?src=ollama", false).Body.String()
	for _, want := range []string{"<!doctype html>", "model-000", "Installed already.", "Installed",
		`hx-get="/discover?page=2&amp;src=ollama"`, "1.2M pulls"} {
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
	all := get(h, "/discover?src=ollama&fit=0", false).Body.String()
	for _, want := range []string{"huge", "cloudy", `hx-get="/discover?fit=0&amp;page=2&amp;src=ollama"`} {
		if !strings.Contains(all, want) {
			t.Errorf("unfiltered page missing %q", want)
		}
	}
	// Asking for cloud models shows them despite the filter.
	if cloud := get(h, "/discover?src=ollama&c=cloud", false).Body.String(); !strings.Contains(cloud, "cloudy") || strings.Contains(cloud, "huge") {
		t.Errorf("cloud filter: want cloudy and not huge")
	}

	// Filter changes swap just the results.
	frag := get(h, "/discover?src=ollama&q=x", true).Body.String()
	if strings.Contains(frag, "<!doctype html>") || !strings.Contains(frag, `id="discover-results"`) {
		t.Errorf("htmx search didn't return the results fragment:\n%s", frag)
	}
	// Scrolling fetches just the next page's cards.
	more := get(h, "/discover?src=ollama&page=2", true).Body.String()
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
	body := get(h, "/discover?src=ollama", false).Body.String()
	if !strings.Contains(body, "Search failed: couldn&#39;t reach ollama.com") {
		t.Errorf("no error shown:\n%s", body)
	}
}

func TestDiscoverStateURL(t *testing.T) {
	st := parseDiscoverState(map[string][]string{"src": {"ollama"}, "q": {" qwen "}, "c": {"vision", "bogus", "vision", "cloud"}, "o": {"newest"}, "fit": {"0"}, "page": {"3"}})
	if want := "/discover?c=vision&c=cloud&fit=0&o=newest&page=3&q=qwen&src=ollama"; st.URL() != want {
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
	if st := parseDiscoverState(map[string][]string{"src": {"hf"}, "bl": {"hide"}}); !st.HideBL || st.URL() != "/discover?bl=hide" {
		t.Errorf("hide blacklisted on Hugging Face = %+v, %s", st, st.URL())
	}
	if st := parseDiscoverState(nil); !st.HF() || st.URL() != "/discover" {
		t.Errorf("empty state should be Hugging Face, the default: %+v, %s", st, st.URL())
	}
}

func fakeHF(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/models":
			if r.URL.Query().Get("search") == "layers" { // only a repo Ollama can't pull
				fmt.Fprint(w, `[{"id":"someone/Big-Q2_K-layers","downloads":3,"likes":0,"gated":false,"gguf":{"total":400000000},
					"siblings":[{"rfilename":"README.md"},{"rfilename":"layers/layer-000.gguf"},{"rfilename":"layers/layer-001.gguf"}]}]`)
				return
			}
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
	if _, err := testStore.EnqueueDownload(t.Context(), "hf.co/owner/small-gguf:q8_0", "admin", 0); err != nil {
		t.Fatal(err)
	}

	// Hugging Face is the default, without the ollama.com tab's caveat.
	if body := get(h, "/discover", false).Body.String(); !strings.Contains(body, `aria-current="true">Hugging Face`) || strings.Contains(body, `class="disc-banner warn"`) {
		t.Error("a bare /discover should be Hugging Face, with no note about reading ollama.com's pages")
	}
	body := get(h, "/discover?src=hf", false).Body.String()
	for _, want := range []string{"owner/Small-GGUF", "A quantisation of <strong>owner/Small</strong>", "8B parameters", "1.2M downloads",
		"128K context", `class="badge queued"`, `hx-get="/discover?cursor=p2"`, `aria-current="true">Hugging Face`} {
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

func TestDiscoverShowsBlacklist(t *testing.T) {
	lib, hf := fakeLibrary(t), fakeHF(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL, c.HFURL = lib.URL, hf.URL })
	at := time.Date(2026, 9, 1, 10, 30, 0, 0, time.Local)
	for _, e := range []store.BlacklistEntry{
		{Model: "model-000:cloud", Reason: "Too slow & vague.", By: "admin", At: at},
		// Pulled under another name, but the same download as model-000's latest and 8b.
		{Model: "renamed:v1", Digest: "aaa111" + strings.Repeat("f", 58), By: "admin", At: at},
		{Model: "hf.co/owner/Small-GGUF:Q4_K_M", Reason: "Lost too much quality.", By: "admin", At: at},
	} {
		testStore.BlacklistModel(t.Context(), e)
	}

	// Cards: a badge whose tooltip has each entry.
	body := get(h, "/discover?src=ollama&fit=0", false).Body.String()
	for _, want := range []string{
		`<a class="badge error tip-wrap" href="/models/blacklist">Blacklisted<span class="tip wide right" role="tooltip">`,
		"<strong>model-000:cloud</strong> blacklisted 2026-09-01 10:30:00 by admin",
		`<span class="bl-tip-reason">Too slow &amp; vague.</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ollama.com card missing %q", want)
		}
	}
	if strings.Count(body, "tip-wrap\" href=\"/models/blacklist\"") != 1 {
		t.Errorf("only model-000's card should be marked")
	}
	hfBody := get(h, "/discover?src=hf&fit=0", false).Body.String()
	if !strings.Contains(hfBody, "<strong>hf.co/owner/Small-GGUF:Q4_K_M</strong>") || !strings.Contains(hfBody, "Lost too much quality.") {
		t.Error("Hugging Face card should be marked")
	}

	// Tags: by name, or by digest for an alias.
	tags := get(h, "/discover/tags?model=model-000", true).Body.String()
	if n := strings.Count(tags, ">Blacklisted<"); n != 3 {
		t.Errorf("%d tags marked, want 3 (cloud by name; latest and 8b by digest)", n)
	}
	if !strings.Contains(tags, "<strong>renamed:v1</strong>") || !strings.Contains(tags, "No reason given.") {
		t.Error("alias tags should show the entry they matched")
	}
	files := get(h, "/discover/hf/files?repo=owner/Small-GGUF&ctx=131072", true).Body.String()
	if n := strings.Count(files, ">Blacklisted<"); n != 1 {
		t.Errorf("%d HF quants marked, want just Q4_K_M:\n%s", n, files)
	}

	// Hide blacklisted: off by default; on, a model goes if any of its tags is blacklisted.
	if !strings.Contains(body, `name="bl" value="hide" >`) {
		t.Error("the filter should be offered, unticked")
	}
	hidden := get(h, "/discover?src=ollama&fit=0&bl=hide", false).Body.String()
	if strings.Contains(hidden, `<h3 class="disc-name">model-000</h3>`) || !strings.Contains(hidden, `<h3 class="disc-name">huge</h3>`) {
		t.Error("only model-000 (one tag blacklisted) should be hidden")
	}
	if !strings.Contains(hidden, `name="bl" value="hide" checked>`) || !strings.Contains(hidden, `hx-get="/discover?bl=hide&amp;fit=0&amp;page=2&amp;src=ollama"`) {
		t.Error("the filter should stay ticked, and carry on to the next page")
	}
	hfHidden := get(h, "/discover?src=hf&fit=0&bl=hide", false).Body.String()
	if strings.Contains(hfHidden, `<h3 class="disc-name">owner/Small-GGUF</h3>`) || !strings.Contains(hfHidden, `<h3 class="disc-name">owner/Huge-GGUF</h3>`) {
		t.Error("Small-GGUF (one quant blacklisted) should be hidden on Hugging Face")
	}
}

func TestDiscoverAllBlacklisted(t *testing.T) {
	lib := fakeLibrary(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL = lib.URL })
	for _, m := range []string{"model-000:8b", "huge:4000b", "cloudy:latest"} {
		testStore.BlacklistModel(t.Context(), store.BlacklistEntry{Model: m, By: "admin", At: time.Now()})
	}
	// The search has a next page, so the page still scrolls; its first page is just empty.
	body := get(h, "/discover?src=ollama&fit=0&bl=hide", true).Body.String()
	for _, name := range []string{"model-000", "huge", "cloudy"} {
		if strings.Contains(body, `<h3 class="disc-name">`+name+`</h3>`) {
			t.Errorf("%s should be hidden", name)
		}
	}
}

func TestDiscoverHidesUnpullableHFRepos(t *testing.T) {
	hf := fakeHF(t)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.HFURL = hf.URL })
	body := get(h, "/discover?src=hf&q=layers&fit=0", false).Body.String()
	if strings.Contains(body, "someone/Big-Q2_K-layers") {
		t.Error("a repo with no GGUF Ollama can pull shouldn't get a card")
	}
	if !strings.Contains(body, "Found 1 repo for “layers”, but none has a GGUF file Ollama can pull") {
		t.Errorf("the page should say why nothing's shown:\n%s", body)
	}
}

// ollama.com answering with markup the parser can't read isn't "no models".
func TestDiscoverUnreadableLibrary(t *testing.T) {
	lib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><main class="redesigned">Nothing we recognise</main></body></html>`)
	}))
	t.Cleanup(lib.Close)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL = lib.URL })

	body := get(h, "/discover?src=ollama", false).Body.String()
	if !strings.Contains(body, "Search failed: couldn&#39;t read ollama.com (its layout may have changed)") || strings.Contains(body, "No models found") {
		t.Errorf("a blank search that reads nothing should say so:\n%s", body)
	}
	if body := get(h, "/discover?src=ollama&q=zzz", false).Body.String(); !strings.Contains(body, "No models found for “zzz”") {
		t.Errorf("a text search can find nothing:\n%s", body)
	}
	if body := get(h, "/discover/tags?model=qwen3", true).Body.String(); !strings.Contains(body, "Couldn&#39;t read qwen3&#39;s tags") {
		t.Errorf("tags: %s", body)
	}
}

// While browsing ollama.com isn't working, Discover in the nav and its
// Ollama library tab have a red dot saying why.
func TestDiscoverBrokenDot(t *testing.T) {
	lib := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<ul><li><a href="/library/a"></a></li><li><a href="/library/b"></a></li><li><a href="/library/c"></a></li></ul>`)
	}))
	t.Cleanup(lib.Close)
	h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.LibraryURL = lib.URL })

	if body := get(h, "/", false).Body.String(); strings.Contains(body, "nav-dot bad") {
		t.Error("red dot before ollama.com was even tried")
	}
	body := get(h, "/discover?src=ollama&fit=0", false).Body.String()
	if n := strings.Count(body, `class="nav-dot bad"`); n != 2 {
		t.Errorf("discover page has %d red dots, want 2 (nav and tab)", n)
	}
	if !strings.Contains(body, "missing their descriptions") {
		t.Error("the dot doesn't say what's wrong")
	}
	if !strings.Contains(body, "has no API for searching its library") || !strings.Contains(body, "isn't working right now") {
		t.Error("the Ollama library tab should explain it reads web pages, and that it's broken now")
	}
	if body := get(h, "/", false).Body.String(); strings.Count(body, `class="nav-dot bad"`) != 1 {
		t.Error("other pages should keep the nav's red dot")
	}
}
