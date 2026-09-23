package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQueueDownloadFlow(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// Unknown model: re-rendered with the error and what was typed.
	rec := do(h, "POST", "/downloads", url.Values{"model": {"missing-thing:7b"}}, testSession)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "missing-thing:7b wasn") || !strings.Contains(body, `value="missing-thing:7b"`) {
		t.Errorf("unknown model: %d\n%s", rec.Code, body)
	}

	// Pasted pull command is normalised and queued.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"ollama pull qwen3:8b"}}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/downloads?queued=qwen3%3A8b" {
		t.Fatalf("queue: %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	page := get(h, "/downloads?queued=qwen3%3A8b", false).Body.String()
	for _, want := range []string{"Queued <strong>qwen3:8b</strong>", `id="nav-dl-badge" class="nav-badge">1<`, "qwen3:8b", `hx-trigger="every 2s"`} {
		if !strings.Contains(page, want) {
			t.Errorf("downloads page missing %q", want)
		}
	}

	// Duplicate refused.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}}, testSession)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already queued") {
		t.Errorf("duplicate: %d", rec.Code)
	}

	// Run the worker: it completes and moves to the history.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { testManager.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		frag := get(h, "/downloads", true).Body.String()
		if strings.Contains(frag, `<span class="badge completed">completed</span>`) {
			if !strings.Contains(frag, `hx-swap-oob="true" hidden`) {
				t.Error("fragment should clear the nav badge out of band once idle")
			}
			if !strings.Contains(frag, `hx-trigger="every 15s"`) {
				t.Error("polling should slow down once idle")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download never completed:\n%s", frag)
		}
		time.Sleep(20 * time.Millisecond)
	}

	active, _ := testStore.ActiveDownloads(context.Background())
	hist, _, _ := testStore.FinishedDownloads(context.Background(), 1, 10)
	if len(active) != 0 || len(hist) != 1 {
		t.Fatalf("active=%d history=%d", len(active), len(hist))
	}
	detail := get(h, "/downloads/"+itoa(hist[0].ID), false).Body.String()
	for _, want := range []string{"Queued by admin", "Found qwen3:8b on registry.ollama.ai", "Download complete", "View model"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail/log missing %q", want)
		}
	}
	if strings.Contains(detail, `hx-trigger="every 2s"`) {
		t.Error("a finished download's page shouldn't keep polling")
	}

	// Clear history.
	do(h, "POST", "/downloads/clear", url.Values{}, testSession)
	if _, n, _ := testStore.FinishedDownloads(context.Background(), 1, 10); n != 0 {
		t.Errorf("history should be empty after clearing, has %d", n)
	}
	if rec := get(h, "/downloads/"+itoa(hist[0].ID), false); rec.Code != http.StatusNotFound {
		t.Errorf("cleared download should 404, got %d", rec.Code)
	}
}

func TestCancelQueuedAndRetry(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	do(h, "POST", "/downloads", url.Values{"model": {"gemma3"}}, testSession)
	active, _ := testStore.ActiveDownloads(context.Background())
	id := itoa(active[0].ID)

	rec := do(h, "POST", "/downloads/"+id+"/cancel", url.Values{"from": {"detail"}}, testSession)
	if rec.Header().Get("Location") != "/downloads/"+id {
		t.Errorf("cancel from detail should return there, got %q", rec.Header().Get("Location"))
	}
	d, _ := testStore.GetDownload(context.Background(), active[0].ID)
	if d.Status != "cancelled" {
		t.Fatalf("status = %s", d.Status)
	}
	if body := get(h, "/downloads", false).Body.String(); !strings.Contains(body, "/downloads/"+id+"/retry") {
		t.Error("cancelled download should offer Retry")
	}
	do(h, "POST", "/downloads/"+id+"/retry", url.Values{}, testSession)
	if d, _ := testStore.GetDownload(context.Background(), active[0].ID); d.Status != "queued" {
		t.Errorf("after retry status = %s", d.Status)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestRetryDialogChangesModel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	ctx := context.Background()
	do(h, "POST", "/downloads", url.Values{"model": {"gemma3:typo"}}, testSession)
	active, _ := testStore.ActiveDownloads(ctx)
	id := active[0].ID
	testStore.FinishDownload(ctx, id, "failed", "pull model manifest: file does not exist")

	page := get(h, "/downloads", false).Body.String()
	if !strings.Contains(page, `data-fill-model="gemma3:typo" data-fill-action="/downloads/`+itoa(id)+`/retry"`) {
		t.Errorf("Retry should open the dialog prefilled:\n%s", page)
	}
	if strings.Contains(page, "data-dialog-autoopen") {
		t.Error("retry dialog shouldn't open by itself")
	}

	// Rejected new name: list page re-rendered with the dialog open on the error.
	rec := do(h, "POST", "/downloads/"+itoa(id)+"/retry", url.Values{"model": {"missing-model:1b"}}, testSession)
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "data-dialog-autoopen") ||
		!strings.Contains(body, "missing-model:1b wasn") || !strings.Contains(body, `action="/downloads/`+itoa(id)+`/retry"`) {
		t.Errorf("rejected retry: %d\n%s", rec.Code, body)
	}

	// Same from the detail page stays on the detail page.
	rec = do(h, "POST", "/downloads/"+itoa(id)+"/retry", url.Values{"model": {"missing-model:1b"}, "from": {"detail"}}, testSession)
	if body := rec.Body.String(); !strings.Contains(body, "All downloads") || !strings.Contains(body, "data-dialog-autoopen") {
		t.Errorf("rejected retry from detail should re-render the detail page with the dialog open")
	}

	// Accepted: switched to the new model and queued.
	rec = do(h, "POST", "/downloads/"+itoa(id)+"/retry", url.Values{"model": {"gemma3:4b"}}, testSession)
	if rec.Header().Get("Location") != "/downloads?queued=gemma3%3A4b" {
		t.Fatalf("retry redirect = %q", rec.Header().Get("Location"))
	}
	d, _ := testStore.GetDownload(ctx, id)
	if d.Model != "gemma3:4b" || d.Status != "queued" {
		t.Errorf("download after retry = %+v", d)
	}
	logs, _ := testStore.DownloadLogs(ctx, id)
	if !strings.Contains(logs[len(logs)-2].Message+logs[len(logs)-1].Message, "changing the model from gemma3:typo to gemma3:4b") {
		t.Errorf("model change should be logged: %+v", logs)
	}
}

func TestDetailShowsSuggestions(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	ctx := context.Background()
	do(h, "POST", "/downloads", url.Values{"model": {"hf.co/liodon-ai/DeepHat-GGUF:Q8_0"}}, testSession)
	active, _ := testStore.ActiveDownloads(ctx)
	id := active[0].ID
	testStore.FinishDownload(ctx, id, "failed", `Head "https://us.aws.cdn.hf.co/x?sig=abc": blocked redirect to a different host`)

	page := get(h, "/downloads/"+itoa(id), false).Body.String()
	for _, want := range []string{`id="suggestions"`, "<h3>Update Ollama</h3>", `href="https://ollama.com/download" target="_blank"`} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	list := get(h, "/downloads", false).Body.String()
	if !strings.Contains(list, `href="/downloads/`+itoa(id)+`#suggestions">💡 Update Ollama</a>`) {
		t.Error("history row should link to the suggestion")
	}
}
