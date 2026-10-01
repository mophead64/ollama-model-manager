package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

func TestModelTestFlow(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// The form offers the models that can chat.
	page := get(h, "/models/testing", false).Body.String()
	for _, want := range []string{`<h2>New test</h2>`, `name="model" value="model-000:latest"`, `name="model" value="user/custom:v1"`,
		`name="repeats" min="1" max="20" value="3"`, "No tests yet."} {
		if !strings.Contains(page, want) {
			t.Errorf("testing page missing %q", want)
		}
	}
	if pre := get(h, "/models/testing?model=user/custom:v1", false).Body.String(); !strings.Contains(pre, `value="user/custom:v1" checked`) {
		t.Error("?model= should tick that model")
	}

	// Bad forms come back with the error and what was entered.
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"prompt": {"Hi"}, "repeats": {"1"}}, "Pick at least one model."},
		{url.Values{"model": {"model-000:latest"}, "prompt": {"  "}, "repeats": {"1"}}, "Enter a prompt."},
		{url.Values{"model": {"model-000:latest"}, "prompt": {"Hi"}, "repeats": {"21"}}, "between 1 and 20 times"},
		{url.Values{"model": {"nope:latest"}, "prompt": {"Hi"}, "repeats": {"1"}}, "nope:latest isn&#39;t a model on this Ollama server"},
	} {
		rec := do(h, "POST", "/models/testing", c.form, testSession)
		if body := rec.Body.String(); rec.Code != http.StatusBadRequest || !strings.Contains(body, c.want) {
			t.Errorf("%v: %d, want 400 with %q", c.form, rec.Code, c.want)
		}
	}
	if rec := do(h, "POST", "/models/testing", url.Values{"model": {"model-000:latest"}, "prompt": {"Keep this"}, "repeats": {"0"}}, testSession); !strings.Contains(rec.Body.String(), ">Keep this</textarea>") {
		t.Error("a refused form should keep the prompt")
	}

	// A good one is queued and shown.
	rec := do(h, "POST", "/models/testing", url.Values{
		"model": {"user/custom:v1", "model-000:latest"}, "prompt": {"Say pong.\nNothing else."}, "repeats": {"2"},
	}, testSession)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/models/testing/") {
		t.Fatalf("create = %d %q\n%s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	detailURL, query, _ := strings.Cut(rec.Header().Get("Location"), "?")
	if query != "queued=1" {
		t.Errorf("after queuing, the test's page should say it's queued: %q", rec.Header().Get("Location"))
	}
	if body := get(h, detailURL+"?queued=1", false).Body.String(); !strings.Contains(body, "Queued the test. It runs in the background") {
		t.Error("queued notice missing")
	}
	queued := get(h, detailURL, false).Body.String()
	if strings.Contains(queued, "hx-preserve") || !strings.Contains(queued, `-pending">`) {
		t.Error("runs still to do shouldn't be preserved across refreshes")
	}
	for _, want := range []string{"Say pong.", `<span class="badge test-status queued">Queued</span>`, "Waiting", `hx-select="#test-results"`, "0 of 4 done", ">Cancel</button>"} {
		if !strings.Contains(queued, want) {
			t.Errorf("queued test page missing %q", want)
		}
	}
	// Mid-run, the list shows progress in its own column and a plain status.
	if mt, _ := testStore.ModelTests(t.Context()); len(mt) == 1 {
		testStore.StartModelTest(t.Context(), mt[0].ID)
		first, _ := testStore.NextPendingRun(t.Context(), mt[0].ID)
		first.Status = store.TestCompleted
		testStore.FinishRun(t.Context(), *first)
		mid := get(h, "/models/testing", false).Body.String()
		for _, want := range []string{`<span class="badge test-status running">Running</span>`, `<span class="tnum">1 of 4</span>`,
			`aria-valuenow="1"`, `style="width: 25.0%"`, "2 runs per model"} {
			if !strings.Contains(mid, want) {
				t.Errorf("list mid-run missing %q", want)
			}
		}
		testStore.RequeueInterruptedModelTests(t.Context()) // back to queued, for the runner below
	}
	list := get(h, "/models/testing", false).Body.String()
	if !strings.Contains(list, `>Say pong.</a>`) || !strings.Contains(list, `hx-select="#tests-panel"`) {
		t.Error("the list should show the test, named after its prompt's first line, and poll while it's queued")
	}

	// Run it.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go testRunner.Run(ctx)
	testRunner.Wake()
	deadline := time.Now().Add(5 * time.Second)
	for {
		all, _ := testStore.ModelTests(ctx)
		if len(all) == 1 && !all[0].Active() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("test didn't finish: %+v", all)
		}
		time.Sleep(10 * time.Millisecond)
	}

	done := get(h, detailURL, false).Body.String()
	for _, want := range []string{
		`<span class="badge test-status completed">Completed</span>`, "4 of 4 done", `<div class="test-reply md"><p>Pong</p>`,
		"30 tokens · 60.0 tokens/s · 1.5s (loading 900 ms)", // eval 30 tokens in 0.5s
		`<a class="model-link" href="#model-1">user/custom:v1</a>`,
		// The id carries the status, so a run that finished since the last
		// refresh isn't swapped back to how it looked while it ran.
		`-completed" hx-preserve="true">`,
		">Run again</button>", `id="delete-test-modal"`,
	} {
		if !strings.Contains(done, want) {
			t.Errorf("finished test page missing %q", want)
		}
	}
	if strings.Contains(done, `hx-select="#test-results"`) {
		t.Error("a finished test shouldn't keep polling")
	}
	// Models in the order picked.
	if !strings.Contains(done, `<a class="model-link" href="#model-2">model-000:latest</a>`) ||
		strings.Index(done, `<h2 class="mono">user/custom:v1</h2>`) > strings.Index(done, `<h2 class="mono">model-000:latest</h2>`) {
		t.Error("models should be in the order picked")
	}

	// Run again queues a copy; delete removes the original.
	id := strings.TrimPrefix(detailURL, "/models/testing/")
	rec = do(h, "POST", detailURL+"/rerun", url.Values{}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") == detailURL {
		t.Errorf("rerun = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = do(h, "POST", detailURL+"/delete", url.Values{}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/models/testing?deleted=Say+pong." {
		t.Errorf("delete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(h, "/models/testing/"+id, false); rec.Code != http.StatusNotFound {
		t.Errorf("deleted test = %d, want 404", rec.Code)
	}
}

func TestModelTestCancelAndDeleteGuard(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	id, _ := testStore.CreateModelTest(t.Context(), "Named", "Hi", []string{"model-000:latest"}, 1, "admin")
	path := "/models/testing/" + itoa(id)

	// An active test can't be deleted.
	if rec := do(h, "POST", path+"/delete", url.Values{}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting an active test = %d, want 400", rec.Code)
	}
	// Cancelling a queued one (the runner isn't running here) takes it out of the queue.
	rec := do(h, "POST", path+"/cancel", url.Values{}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path+"?cancelled=1" {
		t.Errorf("cancel = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if mt, _ := testStore.GetModelTest(t.Context(), id); mt.Status != store.TestCancelled {
		t.Errorf("status after cancel = %s", mt.Status)
	}
	if body := get(h, path+"?cancelled=1", false).Body.String(); !strings.Contains(body, "Cancelled the test.") || !strings.Contains(body, `<span class="badge test-status cancelled">Cancelled</span>`) {
		t.Error("cancelled test page wrong")
	}
	if rec := get(h, "/models/testing/abc", false); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", rec.Code)
	}
}

func TestModelTestForm(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	ctx := t.Context()

	// An earlier test's timings feed the estimate.
	id, _ := testStore.CreateModelTest(ctx, "Earlier", "Hi", []string{"user/custom:v1"}, 1, "admin")
	runs, _ := testStore.ModelTestRuns(ctx, id)
	runs[0].Status, runs[0].TotalMS = store.TestCompleted, 4200
	testStore.FinishRun(ctx, runs[0])
	testStore.FinishModelTest(ctx, id, store.TestCompleted)

	page := get(h, "/models/testing", false).Body.String()
	for _, want := range []string{
		// A short name, with the source and tag underneath, the full name in the tooltip.
		`<label class="test-model" title="user/custom:v1" data-search="user/custom:v1 qwen" data-est="4200"`,
		`<span class="test-model-name">custom</span>`, `<span class="test-model-sub">user · v1 · qwen</span>`,
		`<span class="test-model-sub">latest · llama</span>`, `data-est="0"`,
		`data-select-all`, `data-model-filter`, `data-repeats="5"`, `class="secondary small on" data-repeats="3" aria-pressed="true"`,
		`formaction="/models/testing/templates"`, `src="/static/tests.js?v=`,
		`data-needs="models prompt runs" aria-describedby="why-queue"`, `data-needs="prompt runs"`,
		`<a class="btn secondary test-reset" href="/models/testing" data-reset`, `value="3" data-default="3"`,
		"Save a prompt, models and runs as a template",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("form missing %q", want)
		}
	}
	// Grouped by family: llama before qwen.
	if strings.Index(page, `name="model" value="model-000:latest"`) > strings.Index(page, `name="model" value="user/custom:v1"`) {
		t.Error("models should be grouped by family")
	}
	// No badge on the tab with nothing queued; one with something queued.
	if strings.Contains(page, `test(s) queued or running`) {
		t.Error("no badge expected with nothing queued")
	}
	testStore.CreateModelTest(ctx, "Queued", "Hi", []string{"user/custom:v1"}, 1, "admin")
	if list := get(h, "/models", false).Body.String(); !strings.Contains(list, `Testing <span class="nav-badge" title="1 test(s) queued or running">1</span>`) {
		t.Error("the Testing tab should count queued tests, on every models tab")
	}
}

func TestModelTestTemplates(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// Save from the form: back to the form, filled in, with the template listed.
	rec := do(h, "POST", "/models/testing/templates", url.Values{
		"model": {"user/custom:v1"}, "prompt": {"Explain DNS."}, "repeats": {"5"}, "name": {"DNS"},
	}, testSession)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/models/testing?template=") || !strings.HasSuffix(loc, "&saved=1") {
		t.Fatalf("save template = %d %q", rec.Code, loc)
	}
	page := get(h, loc, false).Body.String()
	for _, want := range []string{"Saved the template DNS.", ">Explain DNS.</textarea>", `value="user/custom:v1" checked`,
		`name="repeats" min="1" max="20" value="5"`, `value="DNS"`, `<strong class="test-tpl-name">DNS</strong>`, "1 model · 5 runs each"} {
		if !strings.Contains(page, want) {
			t.Errorf("form after saving missing %q", want)
		}
	}
	tpls, _ := testStore.ModelTestTemplates(t.Context())
	id := itoa(tpls[0].ID)

	// Apply fills the form in from it, to check and queue; nothing's queued yet.
	if !strings.Contains(page, `<a class="btn secondary small" href="/models/testing?template=`+id+`"`) || strings.Contains(page, "/templates/"+id+"/run") {
		t.Error("templates should offer Apply, not Run")
	}
	applied := get(h, "/models/testing?template="+id, false).Body.String()
	if !strings.Contains(applied, "Filled in from the template DNS.") || !strings.Contains(applied, ">Explain DNS.</textarea>") || !strings.Contains(applied, `value="user/custom:v1" checked`) {
		t.Error("Apply should fill the form in")
	}
	if tests, _ := testStore.ModelTests(t.Context()); len(tests) != 0 {
		t.Errorf("applying a template shouldn't queue anything: %+v", tests)
	}
	if rec := do(h, "POST", "/models/testing/templates/"+id+"/run", url.Values{}, testSession); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("the old run endpoint = %d, want gone", rec.Code)
	}

	// A template doesn't need models (they're picked when it's used), but does need a prompt.
	if rec := do(h, "POST", "/models/testing/templates", url.Values{"repeats": {"1"}}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("a template with no prompt = %d, want 400", rec.Code)
	}
	rec = do(h, "POST", "/models/testing/templates", url.Values{"prompt": {"Any model"}, "repeats": {"2"}}, testSession)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a template with no models = %d, want saved", rec.Code)
	}
	noModels := rec.Header().Get("Location")
	if page := get(h, noModels, false).Body.String(); !strings.Contains(page, "Models picked when run · 2 runs each") || !strings.Contains(page, ">Any model</textarea>") {
		t.Error("a template without models should say they're picked when it's used, and fill in the rest")
	}
	testStore.DeleteModelTestTemplate(t.Context(), mustID(t, strings.TrimSuffix(strings.TrimPrefix(noModels, "/models/testing?template="), "&saved=1")))

	// Save a test as a template from its page.
	testID, _ := testStore.CreateModelTest(t.Context(), "From a test", "Hi", []string{"user/custom:v1"}, 2, "admin")
	testPath := "/models/testing/" + itoa(testID)
	if !strings.Contains(get(h, testPath, false).Body.String(), `action="`+testPath+`/template"`) {
		t.Error("a test's page should offer Save as template")
	}
	if rec := do(h, "POST", testPath+"/template", url.Values{}, testSession); rec.Code != http.StatusSeeOther {
		t.Errorf("save test as template = %d", rec.Code)
	}
	if tpls, _ := testStore.ModelTestTemplates(t.Context()); len(tpls) != 2 {
		t.Errorf("templates = %+v", tpls)
	}

	// A template naming a model that's gone: the form says so and leaves it out.
	gone, _ := testStore.SaveModelTestTemplate(t.Context(), store.ModelTestTemplate{Name: "Old", Prompt: "Hi", Models: []string{"deleted:latest", "user/custom:v1"}, Repeats: 1, CreatedBy: "admin"})
	if page := get(h, "/models/testing?template="+itoa(gone), false).Body.String(); !strings.Contains(page, "Not on this Ollama server any more, so left out: <code>deleted:latest</code>") {
		t.Error("missing models should be pointed out")
	}

	// Delete.
	rec = do(h, "POST", "/models/testing/templates/"+id+"/delete", url.Values{}, testSession)
	if rec.Header().Get("Location") != "/models/testing?template_deleted=DNS" {
		t.Errorf("delete template = %q", rec.Header().Get("Location"))
	}
	if page := get(h, "/models/testing?template="+id, false).Body.String(); strings.Contains(page, "Filled in from the template") {
		t.Error("a deleted template shouldn't fill anything in")
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("bad id %q", s)
	}
	return id
}
