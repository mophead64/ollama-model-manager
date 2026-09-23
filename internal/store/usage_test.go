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
