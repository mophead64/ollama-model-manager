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
	counts, err := st.ModelLoadCounts(ctx)
	if err != nil || counts["qwen3:8b"] != 3 || counts["llama3.2:latest"] != 1 {
		t.Errorf("load counts = %v, %v", counts, err)
	}
	active, loads, since, err := st.ModelUsageTotals(ctx, "qwen3:8b")
	if err != nil || active != 60 || loads != 3 || since != "2026-09-01" {
		t.Errorf("totals = %d, %d, %q, %v", active, loads, since, err)
	}
	if _, _, since, _ := st.ModelUsageTotals(ctx, "never:latest"); since != "" {
		t.Errorf("a model with no usage should have no since, got %q", since)
	}
}
