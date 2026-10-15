package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestLabelsFlow(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	post := func(path string, form url.Values, referer string) *http.Response {
		return do(h, "POST", path, form, testSession, "Referer", referer).Result()
	}

	// The Labels tab: built-in labels from the start, and making one.
	page := get(h, "/models/labels", false).Body.String()
	for _, want := range []string{`aria-current="true">Labels</a>`, `<span class="lbl blue">Coding</span>`, "Built-in", `action="/models/labels"`} {
		if !strings.Contains(page, want) {
			t.Errorf("labels page missing %q", want)
		}
	}
	if resp := post("/models/labels", url.Values{"name": {"Used by Home Assistant"}, "color": {"green"}}, "/models/labels"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("make label: %d", resp.StatusCode)
	}
	if body := do(h, "POST", "/models/labels", url.Values{"name": {"used by home assistant"}, "color": {"red"}}, testSession).Body.String(); !strings.Contains(body, "There&#39;s already a label with that name.") {
		t.Error("a duplicate name should be refused, on the page")
	}
	if rec := do(h, "POST", "/models/labels", url.Values{"name": {"X"}, "color": {"teal"}}, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown colour: %d", rec.Code)
	}
	labels, _ := testStore.Labels(t.Context())
	var ha, coding int64
	for _, l := range labels {
		switch l.Name {
		case "Used by Home Assistant":
			ha = l.ID
		case "Coding":
			coding = l.ID
		}
	}
	if ha == 0 || coding == 0 {
		t.Fatalf("labels: %+v", labels)
	}

	// Labelling a model from its page, with a new label made there too.
	resp := post("/models/labels/set", url.Values{"model": {"user/custom:v1"}, "label": {strconv.FormatInt(ha, 10)},
		"new_name": {"Night shift"}, "new_color": {"purple"}}, "/models/user/custom:v1")
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/models/user/custom:v1?labelled=") {
		t.Errorf("after labelling, went to %q", loc)
	}
	detail := get(h, "/models/user/custom:v1?labelled=Updated", false).Body.String()
	for _, want := range []string{`<span class="lbl green">Used by Home Assistant</span>`, `<span class="lbl purple">Night shift</span>`,
		`id="edit-labels-modal"`, `name="label" value="` + strconv.FormatInt(ha, 10) + `" checked>`} {
		if !strings.Contains(detail, want) {
			t.Errorf("model page missing %q", want)
		}
	}

	// The Models page and dashboard show them; the list filters by them.
	models := get(h, "/models", false).Body.String()
	if !strings.Contains(models, `<div class="lbl-row"><span class="lbl purple">Night shift</span><span class="lbl green">Used by Home Assistant</span></div>`) {
		t.Error("models list should show the model's labels under its name")
	}
	if !strings.Contains(models, `<select id="f-label" name="label">`) || !strings.Contains(models, `id="bulk-label-modal"`) {
		t.Error("models list should offer a label filter and the bulk Label dialog")
	}
	only := get(h, "/models?label="+strconv.FormatInt(ha, 10), true).Body.String()
	if !strings.Contains(only, "user/custom:v1") || strings.Contains(only, "model-000:latest") {
		t.Error("the label filter should leave just the labelled model")
	}

	// Bulk: add Coding to both, then take it off one.
	post("/models/bulk/label", url.Values{"name": {"user/custom:v1", "model-000:latest"}, "label": {strconv.FormatInt(coding, 10)}, "op": {"add"}}, "/models")
	post("/models/bulk/label", url.Values{"name": {"user/custom:v1"}, "label": {strconv.FormatInt(coding, 10)}, "op": {"remove"}}, "/models")
	byModel, _ := testStore.ModelLabels(t.Context())
	if got := byModel["model-000:latest"]; len(got) != 1 || got[0].Name != "Coding" {
		t.Errorf("model-000's labels = %+v", got)
	}
	if got := byModel["user/custom:v1"]; len(got) != 2 {
		t.Errorf("user/custom:v1 should have had Coding taken off: %+v", got)
	}

	// Editing and deleting a label; built-in ones can't be.
	post("/models/labels/"+strconv.FormatInt(ha, 10), url.Values{"name": {"Home Assistant"}, "color": {"amber"}}, "/models/labels")
	if body := get(h, "/models/labels", false).Body.String(); !strings.Contains(body, `<span class="lbl amber">Home Assistant</span>`) {
		t.Error("the label wasn't updated")
	}
	if rec := do(h, "POST", "/models/labels/"+strconv.FormatInt(coding, 10)+"/delete", nil, testSession); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting a built-in label: %d", rec.Code)
	}
	post("/models/labels/"+strconv.FormatInt(ha, 10)+"/delete", nil, "/models/labels")
	byModel, _ = testStore.ModelLabels(t.Context())
	if got := byModel["user/custom:v1"]; len(got) != 1 || got[0].Name != "Night shift" {
		t.Errorf("after deleting Home Assistant, user/custom:v1 = %+v", got)
	}
}
