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

func TestLoadsColumn(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 3).URL)
	today := time.Now().Format(time.DateOnly)
	testStore.AddModelUsage(t.Context(), "model-001:latest", today, 30, 5)
	testStore.AddModelUsage(t.Context(), "user/custom:v1", today, 15, 2)

	body := get(h, "/models", true).Body.String()
	for _, want := range []string{">Loads<", `title="Times Ollama has loaded it into memory, since this app started tracking">5</span>`,
		`title="Not seen loaded since this app started tracking">—</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("models table missing %q", want)
		}
	}
	// Most loaded first; never-seen models last.
	body = get(h, "/models?sort=loads&dir=desc", true).Body.String()
	order := regexp.MustCompile(`class="model-name" data-copy-text="([^"]+)"`).FindAllStringSubmatch(body, -1)
	var got []string
	for _, m := range order {
		got = append(got, m[1])
	}
	if strings.Join(got, ",") != "model-001:latest,user/custom:v1,model-000:latest,model-002:latest" {
		t.Errorf("sorted by loads = %v", got)
	}
}

func TestModelUsagePanel(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// Nothing recorded yet.
	if body := get(h, "/models/user/custom:v1", false).Body.String(); !strings.Contains(body, "No use recorded yet.") {
		t.Error("a model with no usage should say so")
	}

	now := time.Now()
	day := func(back int) string { return now.AddDate(0, 0, -back).Format(time.DateOnly) }
	testStore.AddModelUsage(t.Context(), "user/custom:v1", day(0), 600, 2)   // today: 10 min, busiest
	testStore.AddModelUsage(t.Context(), "user/custom:v1", day(3), 150, 1)   // 2.5 min
	testStore.AddModelUsage(t.Context(), "user/custom:v1", day(40), 3600, 4) // before the chart

	body := get(h, "/models/user/custom:v1", false).Body.String()
	for _, want := range []string{
		"<h2 style=\"margin:0\">Usage</h2>", "Recorded since " + now.AddDate(0, 0, -40).Format("2 Jan 2006"),
		`≈ 13 min</div><div class="label">in use, last 30 days`, // 750 s
		`2 <span class="muted">of 30</span>`, `>3</div><div class="label">times loaded, last 30 days`,
		`>7</div><div class="label">times loaded in all (≈ 1.2 h in use)`,
		`style="height: 100.0%"`, `style="height: 25.0%"`, // today, and three days ago
		"<strong>" + now.Format("Mon 2 Jan") + "</strong> · ≈ 10 min in use · loaded 2×",
		"Show as a table", "<td class=\"tnum\">≈ 3 min</td><td class=\"tnum\">1</td>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("usage panel missing %q", want)
		}
	}
	if n := strings.Count(body, `class="usage-col tip-wrap"`); n != 30 {
		t.Errorf("%d bars, want one per day for 30 days", n)
	}
}
