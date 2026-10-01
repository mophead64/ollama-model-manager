package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

func TestDeleteAndBlacklist(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// The delete dialogs offer it.
	for _, path := range []string{"/", "/models", "/models/user/custom:v1"} {
		if body := get(h, path, false).Body.String(); !strings.Contains(body, `name="blacklist" class="bl-toggle"`) || !strings.Contains(body, `name="reason"`) {
			t.Errorf("%s: delete dialog should offer blacklisting", path)
		}
	}

	// A plain delete doesn't blacklist.
	do(h, "POST", "/models/delete", url.Values{"name": {"model-000:latest"}, "reason": {"ignored"}}, testSession)
	if got, _ := testStore.Blacklist(t.Context()); len(got) != 0 {
		t.Fatalf("delete without the box ticked blacklisted: %+v", got)
	}

	rec := do(h, "POST", "/models/delete", url.Values{
		"name": {"user/custom:v1"}, "blacklist": {"on"}, "reason": {"  Poor at code.\nSlow too.  "}, "return": {"/"},
	}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?blacklisted=1&deleted=user%2Fcustom%3Av1" {
		t.Fatalf("delete and blacklist = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(deletedModels) != 2 {
		t.Errorf("model should still be deleted: %v", deletedModels)
	}
	got, err := testStore.Blacklist(t.Context())
	if err != nil || len(got) != 1 {
		t.Fatalf("blacklist = %+v, %v", got, err)
	}
	e := got[0]
	// Its details are kept from before the delete.
	if e.Model != "user/custom:v1" || e.Reason != "Poor at code.\nSlow too." || e.Family != "qwen" || e.Size != 2048 || e.Digest != "abc123" || e.By != "admin" || e.At.IsZero() {
		t.Errorf("entry = %+v", e)
	}

	if body := get(h, "/?blacklisted=1&deleted=user%2Fcustom%3Av1", false).Body.String(); !strings.Contains(body, `and added it to the <a href="/models/blacklist">blacklist</a>`) {
		t.Error("notice should mention the blacklist")
	}
	body := get(h, "/models/blacklist", false).Body.String()
	for _, want := range []string{`data-copy-text="user/custom:v1"`, "Poor at code.\nSlow too.", nbsp("2.0 KB"), "by admin", "1 model(s)"} {
		if !strings.Contains(body, want) {
			t.Errorf("blacklist page missing %q", want)
		}
	}

	// Each row: Edit (filled into the shared dialog), then Download and Remove.
	for _, want := range []string{
		`data-dialog-open="blacklist-edit-modal"`,
		`data-fill-name="user/custom:v1" data-fill-reason="Poor at code.` + "\n" + `Slow too.">Edit</button>`,
		`id="blacklist-edit-modal"`,
		`data-dialog-open="blacklist-download-modal" data-fill-name="user/custom:v1">Download</button>`,
		`data-dialog-open="blacklist-remove-modal" data-fill-name="user/custom:v1">Remove</button>`,
		`id="blacklist-remove-modal"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blacklist row missing %q", want)
		}
	}

	rec = do(h, "POST", "/models/blacklist/reason", url.Values{"name": {"user/custom:v1"}, "reason": {" Actually fine at code, just slow. "}}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/models/blacklist?updated=user%2Fcustom%3Av1" {
		t.Fatalf("edit reason = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if got, _ := testStore.Blacklist(t.Context()); got[0].Reason != "Actually fine at code, just slow." || got[0].Family != "qwen" {
		t.Errorf("after edit: %+v", got[0])
	}
	if body := get(h, "/models/blacklist?updated=user%2Fcustom%3Av1", false).Body.String(); !strings.Contains(body, "Updated the reason for <strong>user/custom:v1</strong>") {
		t.Error("edit should be confirmed")
	}
	if rec := do(h, "POST", "/models/blacklist/reason", url.Values{"name": {"missing:latest"}, "reason": {"x"}}, testSession); rec.Code != http.StatusNotFound {
		t.Errorf("editing a model that isn't blacklisted = %d, want 404", rec.Code)
	}
	if rec := do(h, "POST", "/models/blacklist/reason", url.Values{"name": {"user/custom:v1"}, "reason": {strings.Repeat("x", blacklistReasonMax+1)}}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("over-long edited reason = %d, want 400", rec.Code)
	}

	rec = do(h, "POST", "/models/blacklist/remove", url.Values{"name": {"user/custom:v1"}}, testSession)
	if rec.Header().Get("Location") != "/models/blacklist?removed=user%2Fcustom%3Av1" {
		t.Errorf("remove redirect = %q", rec.Header().Get("Location"))
	}
	if got, _ := testStore.Blacklist(t.Context()); len(got) != 0 {
		t.Errorf("after remove: %+v", got)
	}
	if body := get(h, "/models/blacklist", false).Body.String(); !strings.Contains(body, "No models are blacklisted") {
		t.Error("empty blacklist should say so")
	}
}

func TestBlacklistReasonTooLong(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	rec := do(h, "POST", "/models/delete", url.Values{
		"name": {"user/custom:v1"}, "blacklist": {"on"}, "reason": {strings.Repeat("x", blacklistReasonMax+1)},
	}, testSession)
	if rec.Code != http.StatusBadRequest || len(deletedModels) != 0 {
		t.Errorf("over-long reason should be refused before deleting: %d, %v", rec.Code, deletedModels)
	}
}

func TestBlacklistNotRecordedWhenDeleteFails(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	do(h, "POST", "/models/delete", url.Values{"name": {"fail-me"}, "blacklist": {"on"}}, testSession)
	if got, _ := testStore.Blacklist(t.Context()); len(got) != 0 {
		t.Errorf("failed delete shouldn't blacklist: %+v", got)
	}
}

func TestBlacklistDownload(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	for _, m := range []string{"qwen3:8b", "huge-model:70b", "missing-thing:7b"} {
		testStore.BlacklistModel(t.Context(), store.BlacklistEntry{Model: m, Reason: "meh", By: "admin", At: time.Now()})
	}
	onList := func(m string) bool {
		got, _ := testStore.Blacklist(t.Context())
		return slices.ContainsFunc(got, func(e store.BlacklistEntry) bool { return e.Model == m })
	}

	// Download is behind a dialog that says what'll happen.
	body := get(h, "/models/blacklist", false).Body.String()
	for _, want := range []string{
		`data-dialog-open="blacklist-download-modal" data-fill-name="qwen3:8b">Download</button>`,
		`<input type="hidden" name="unblacklist" data-fill="name">`,
		"will be removed from the blacklist, along with the reason you gave, and downloaded again.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blacklist page missing %q", want)
		}
	}

	// Queued: off the blacklist, and the downloads page says so.
	rec := do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}, "unblacklist": {"qwen3:8b"}}, testSession)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || loc != "/downloads?queued=qwen3%3A8b&unblacklisted=qwen3%3A8b" {
		t.Fatalf("download = %d %q", rec.Code, loc)
	}
	if onList("qwen3:8b") {
		t.Error("queued model should be off the blacklist")
	}
	if page := get(h, "/downloads?queued=qwen3%3A8b&unblacklisted=qwen3%3A8b", false).Body.String(); !strings.Contains(page, "Removed <strong>qwen3:8b</strong> from the blacklist.") {
		t.Error("downloads page should mention the blacklist")
	}

	// Refused by the registry: stays on the blacklist.
	do(h, "POST", "/downloads", url.Values{"model": {"missing-thing:7b"}, "unblacklist": {"missing-thing:7b"}}, testSession)
	if !onList("missing-thing:7b") {
		t.Error("a download that wasn't queued shouldn't unblacklist")
	}

	// Too big: stays until the confirmation, which carries the removal through.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"huge-model:70b"}, "unblacklist": {"huge-model:70b"}}, testSession)
	if !strings.Contains(rec.Body.String(), `<input type="hidden" name="unblacklist" value="huge-model:70b">`) || !onList("huge-model:70b") {
		t.Fatalf("confirmation should carry the removal, and not do it yet")
	}
	do(h, "POST", "/downloads", url.Values{"model": {"huge-model:70b"}, "confirm": {"1"}, "unblacklist": {"huge-model:70b"}}, testSession)
	if onList("huge-model:70b") {
		t.Error("confirmed download should unblacklist")
	}
}

func TestDownloadingBlacklistedModelAsksFirst(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	at := time.Date(2026, 9, 1, 10, 30, 0, 0, time.Local)
	for _, m := range []string{"qwen3:8b", "gemma3:latest"} {
		testStore.BlacklistModel(t.Context(), store.BlacklistEntry{Model: m, Reason: "Rambles & repeats itself.", By: "admin", At: at})
	}
	onList := func(m string) bool {
		got, _ := testStore.Blacklist(t.Context())
		return slices.ContainsFunc(got, func(e store.BlacklistEntry) bool { return e.Model == m })
	}
	queued := func() int {
		active, _ := testStore.ActiveDownloads(t.Context())
		return len(active)
	}

	// The Downloads page: a dialog with the entry, and nothing queued yet.
	rec := do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}}, testSession)
	body := rec.Body.String()
	for _, want := range []string{`id="confirm-download-modal" data-dialog-autoopen`, "<strong>qwen3:8b</strong> is on your blacklist. Downloading it takes it off.",
		"Blacklisted 2026-09-01 10:30:00 by admin", "Rambles &amp; repeats itself.", `<input type="hidden" name="unblacklist" value="qwen3:8b">`, `name="confirm" value="1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirmation missing %q", want)
		}
	}
	if queued() != 0 || !onList("qwen3:8b") {
		t.Fatal("nothing should happen before confirming")
	}
	// Download anyway: queued, and off the blacklist.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}, "confirm": {"1"}, "unblacklist": {"qwen3:8b"}}, testSession)
	if loc := rec.Header().Get("Location"); loc != "/downloads?queued=qwen3%3A8b&unblacklisted=qwen3%3A8b" || queued() != 1 || onList("qwen3:8b") {
		t.Errorf("confirmed = %q, %d queued, still listed %v", loc, queued(), onList("qwen3:8b"))
	}

	// Discover's Download (and Other quants'): the same, in place. "gemma3" is gemma3:latest.
	rec = do(h, "POST", "/downloads", url.Values{"model": {"gemma3"}, "from": {"discover"}}, testSession, "HX-Request", "true")
	if body := rec.Body.String(); !strings.Contains(body, "data-dialog-autoopen") || !strings.Contains(body, "is on your blacklist") || !strings.Contains(body, `name="unblacklist" value="gemma3:latest"`) {
		t.Fatalf("discover confirmation wrong:\n%s", body)
	}
	rec = do(h, "POST", "/downloads", url.Values{"model": {"gemma3"}, "from": {"discover"}, "confirm": {"1"}, "unblacklist": {"gemma3:latest"}}, testSession, "HX-Request", "true")
	if body := rec.Body.String(); !strings.Contains(body, "Removed from the blacklist.") || onList("gemma3:latest") || queued() != 2 {
		t.Errorf("discover confirmed wrong: listed %v, %d queued\n%s", onList("gemma3:latest"), queued(), body)
	}

	// Models that aren't blacklisted go straight through, as before.
	if rec := do(h, "POST", "/downloads", url.Values{"model": {"llama3.2"}}, testSession); rec.Code != http.StatusSeeOther {
		t.Errorf("a model not on the blacklist = %d, want queued", rec.Code)
	}
}

func TestBlacklistPageDownloadIsntAskedTwice(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	testStore.BlacklistModel(t.Context(), store.BlacklistEntry{Model: "qwen3:8b", By: "admin", At: time.Now()})
	// Its own dialog has said so; it sends unblacklist, so it goes ahead.
	rec := do(h, "POST", "/downloads", url.Values{"model": {"qwen3:8b"}, "unblacklist": {"qwen3:8b"}}, testSession)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/downloads?queued=qwen3%3A8b&unblacklisted=qwen3%3A8b" {
		t.Errorf("blacklist page download = %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
