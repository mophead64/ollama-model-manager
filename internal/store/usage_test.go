package store

import (
	"context"
	"testing"
	"time"
)

func TestModelUsage(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	if got, err := st.ModelsLastUsed(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty store = %v, %v", got, err)
	}
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	later := first.Add(3 * time.Hour)
	st.MarkModelUsed(ctx, "llama3.2:latest", first)
	st.MarkModelUsed(ctx, "qwen3:8b", first)
	if err := st.MarkModelUsed(ctx, "llama3.2:latest", later); err != nil {
		t.Fatal(err)
	}

	got, err := st.ModelsLastUsed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["llama3.2:latest"].Equal(later) || !got["qwen3:8b"].Equal(first) {
		t.Errorf("last used = %v", got)
	}
}

func TestModelDailyUsage(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	st.AddModelUsage(ctx, "qwen3:8b", "2026-09-01", 15, 1)
	st.AddModelUsage(ctx, "qwen3:8b", "2026-09-01", 30, 0) // same day: added up
	st.AddModelUsage(ctx, "qwen3:8b", "2026-09-03", 15, 2)
	st.AddModelUsage(ctx, "llama3.2:latest", "2026-09-03", 15, 1)

	days, err := st.ModelDailyUsage(ctx, "qwen3:8b", "2026-09-02")
	if err != nil || len(days) != 1 || days[0] != (DailyUsage{Day: "2026-09-03", ActiveSeconds: 15, Loads: 2}) {
		t.Errorf("daily = %+v, %v", days, err)
	}
	if all, _ := st.ModelDailyUsage(ctx, "qwen3:8b", ""); len(all) != 2 || all[0].ActiveSeconds != 45 {
		t.Errorf("all days = %+v", all)
	}
	totals, err := st.ModelTotals(ctx)
	if err != nil || totals["qwen3:8b"] != (ModelTotal{ActiveSeconds: 60, Loads: 3}) || totals["llama3.2:latest"] != (ModelTotal{ActiveSeconds: 15, Loads: 1}) {
		t.Errorf("totals = %v, %v", totals, err)
	}
	active, loads, since, err := st.ModelUsageTotals(ctx, "qwen3:8b")
	if err != nil || active != 60 || loads != 3 || since != "2026-09-01" {
		t.Errorf("totals = %d, %d, %q, %v", active, loads, since, err)
	}
	if _, _, since, _ := st.ModelUsageTotals(ctx, "never:latest"); since != "" {
		t.Errorf("a model with no usage should have no since, got %q", since)
	}
}

func TestUsageTrackedSince(t *testing.T) {
	ctx := context.Background()

	// A new database starts tracking now, and remembers it.
	st := openTest(t)
	since, err := st.UsageTrackedSince(ctx)
	if err != nil || time.Since(since) > time.Minute {
		t.Fatalf("new database: %v, %v", since, err)
	}
	st.MarkModelUsed(ctx, "llama3.2:latest", since.Add(-48*time.Hour)) // recorded later, whatever it says
	if again, _ := st.UsageTrackedSince(ctx); !again.Equal(since) {
		t.Errorf("asked again: %v, want %v", again, since)
	}

	// One from before it was kept counts from its earliest recorded use.
	st = openTest(t)
	used := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	st.MarkModelUsed(ctx, "qwen3:8b", used)
	st.AddModelUsage(ctx, "qwen3:8b", "2026-06-03", 60, 1)
	if got, err := st.UsageTrackedSince(ctx); err != nil || !got.Equal(used) {
		t.Errorf("existing usage: %v, %v; want %v", got, err, used)
	}
}
