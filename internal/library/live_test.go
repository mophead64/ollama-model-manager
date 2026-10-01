package library

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"
)

// TestLiveOllamaCom runs the parsers against the real ollama.com, to catch
// its markup changing (the fixtures in testdata can't). It's skipped unless
// LIVE_LIBRARY=1; the weekly "Live ollama.com check" workflow runs it.
func TestLiveOllamaCom(t *testing.T) {
	if os.Getenv("LIVE_LIBRARY") != "1" {
		t.Skip("set LIVE_LIBRARY=1 to check the parser against ollama.com")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := New("", "", "")

	page, err := c.Search(ctx, Query{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Models) < 10 {
		t.Fatalf("search found %d models, want a full page", len(page.Models))
	}
	if page.NextPage != 2 {
		t.Errorf("search: next page = %d, want 2", page.NextPage)
	}
	// Individual fields can be missing from a model or two, but not from all.
	var desc, sizes, pulls, tags, updated, caps int
	for _, m := range page.Models {
		if m.Name == "" {
			t.Errorf("a result has no name: %+v", m)
		}
		for _, f := range []struct {
			ok bool
			n  *int
		}{{m.Description != "", &desc}, {len(m.Sizes) > 0, &sizes}, {m.Pulls != "", &pulls}, {m.Tags > 0, &tags}, {m.Updated != "", &updated}, {len(m.Capabilities) > 0, &caps}} {
			if f.ok {
				*f.n++
			}
		}
	}
	for name, n := range map[string]int{"description": desc, "sizes": sizes, "pulls": pulls, "tag count": tags, "updated": updated, "capabilities": caps} {
		if n == 0 {
			t.Errorf("no search result has its %s: the markup may have changed", name)
		}
	}

	found, err := c.Search(ctx, Query{Text: "llama3.2"})
	if err != nil {
		t.Fatalf("search for llama3.2: %v", err)
	}
	if !slices.ContainsFunc(found.Models, func(m Model) bool { return m.Name == "llama3.2" }) {
		t.Errorf("searching for llama3.2 didn't find it: %+v", found.Models)
	}

	list, err := c.Tags(ctx, "llama3.2")
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	var latest *Tag
	for i, tg := range list {
		if tg.Name == "llama3.2:latest" {
			latest = &list[i]
		}
	}
	if latest == nil {
		t.Fatalf("llama3.2's tags don't include latest: %+v", list)
	}
	if latest.Size <= 0 || latest.Digest == "" || latest.Context == "" || latest.Input == "" || latest.Updated == "" {
		t.Errorf("llama3.2:latest is missing details: %+v", *latest)
	}
}
