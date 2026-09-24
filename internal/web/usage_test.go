package web

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

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

	// Most recently used first; never-used models last.
	body = get(h, "/models?sort=used&dir=desc", true).Body.String()
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
