package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDashboardRecentlyUsed(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 3).URL)
	testStore.MarkModelUsed(t.Context(), "model-001:latest", time.Now().Add(-3*time.Hour))
	testStore.MarkModelUsed(t.Context(), "user/custom:v1", time.Now().Add(-10*time.Minute))

	body := get(h, "/", false).Body.String()
	order := regexp.MustCompile(`class="model-name" data-copy-text="([^"]+)"`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, m := range order {
		got = append(got, m[1])
	}
	if strings.Join(got, ",") != "user/custom:v1,model-001:latest" {
		t.Errorf("recently used = %v, want most recent first and unused models left out", got)
	}
	if !strings.Contains(body, `<span class="tip" role="tooltip">`+formatTime(time.Now().Add(-10 * time.Minute))[:10]) {
		t.Error("last used should have a timestamp tooltip")
	}
	// fakeOllama has user/custom:v1 loaded, and not model-001.
	if !strings.Contains(body, `aria-label="Loaded in memory" title="Loaded in memory"></span><span class="copy-tip-wrap"><button type="button" class="model-name" data-copy-text="user/custom:v1"`) {
		t.Error("loaded model should have the loaded dot")
	}
	if strings.Contains(body, `aria-label="Loaded in memory" title="Loaded in memory"></span><span class="copy-tip-wrap"><button type="button" class="model-name" data-copy-text="model-001:latest"`) {
		t.Error("model not in memory shouldn't have the loaded dot")
	}
	// The same split button as the models page, with Delete returning here.
	for _, want := range []string{`href="/models/user/custom:v1">View</a>`, `data-fill-return="/"`, `id="delete-model-modal"`, `id="load-model-modal"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestDashboardRecentlyUsedLimit(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 15).URL)
	for i := range 15 {
		testStore.MarkModelUsed(t.Context(), fmt.Sprintf("model-%03d:latest", i), time.Now().Add(-time.Duration(i)*time.Minute))
	}
	body := get(h, "/", false).Body.String()
	got := regexp.MustCompile(`class="model-name" data-copy-text="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(got) != dashboardRecent || got[0][1] != "model-000:latest" || got[9][1] != "model-009:latest" {
		t.Errorf("recently used = %v, want model-000..009, most recent first", got)
	}
}

func TestLastUsedColumn(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 3).URL)
	testStore.MarkModelUsed(t.Context(), "model-001:latest", time.Now().Add(-3*time.Hour))
	testStore.MarkModelUsed(t.Context(), "user/custom:v1", time.Now().Add(-10*time.Minute))

	body := get(h, "/models", true).Body.String()
	for _, want := range []string{">Last used<", "3 hours ago", "10 minutes ago", "Not seen in use since this app started tracking"} {
		if !strings.Contains(body, want) {
			t.Errorf("models table missing %q", want)
		}
	}

	// Most recently used first (the default); never-used models last.
	body = get(h, "/models", true).Body.String()
	order := regexp.MustCompile(`class="model-name" data-copy-text="([^"]+)"`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, m := range order {
		got = append(got, m[1])
	}
	if strings.Join(got, ",") != "user/custom:v1,model-001:latest,model-000:latest,model-002:latest" {
		t.Errorf("sorted by last used = %v", got)
	}

	detail := get(h, "/models/user/custom:v1", false).Body.String()
	if !strings.Contains(detail, `id="model-memory"`) || !strings.Contains(detail, "10 minutes ago</span>") {
		t.Error("detail page should show when the model was last used")
	}
}

func TestTimeAgo(t *testing.T) {
	cases := map[time.Duration]string{
		20 * time.Second:     "just now",
		time.Minute:          "1 minute ago",
		59 * time.Minute:     "59 minutes ago",
		2 * time.Hour:        "2 hours ago",
		3 * 24 * time.Hour:   "3 days ago",
		65 * 24 * time.Hour:  "2 months ago",
		800 * 24 * time.Hour: "2 years ago",
	}
	for d, want := range cases {
		if got := timeAgo(time.Now().Add(-d)); got != want {
			t.Errorf("timeAgo(-%v) = %q, want %q", d, got, want)
		}
	}
}
