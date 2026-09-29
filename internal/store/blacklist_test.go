package store

import (
	"context"
	"testing"
	"time"
)

func TestBlacklist(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	if got, err := st.Blacklist(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty store = %v, %v", got, err)
	}
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	st.BlacklistModel(ctx, BlacklistEntry{Model: "llama3.2:latest", Reason: "too chatty", Family: "llama", Size: 2000, By: "admin", At: first})
	st.BlacklistModel(ctx, BlacklistEntry{Model: "qwen3:8b", Reason: "slow", By: "admin", At: first.Add(time.Hour)})
	// Again, differently cased: replaces the first entry.
	if err := st.BlacklistModel(ctx, BlacklistEntry{Model: "Llama3.2:latest", Reason: "wrong answers", By: "josh", At: first.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got, err := st.Blacklist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Reason != "wrong answers" || got[0].By != "josh" || got[1].Model != "qwen3:8b" {
		t.Fatalf("blacklist = %+v, want the replaced entry first", got)
	}
	if !got[1].At.Equal(first.Add(time.Hour)) {
		t.Errorf("time = %v", got[1].At)
	}

	if ok, err := st.UpdateBlacklistReason(ctx, "qwen3:8b", "slow, and wrong"); !ok || err != nil {
		t.Fatalf("update reason = %v, %v", ok, err)
	}
	if got, _ := st.Blacklist(ctx); got[1].Reason != "slow, and wrong" || got[1].By != "admin" {
		t.Errorf("after update: %+v", got[1])
	}
	if ok, err := st.UpdateBlacklistReason(ctx, "missing:latest", "x"); ok || err != nil {
		t.Errorf("updating a model that isn't blacklisted = %v, %v", ok, err)
	}

	if err := st.UnblacklistModel(ctx, "qwen3:8b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Blacklist(ctx); len(got) != 1 {
		t.Errorf("after removing one: %+v", got)
	}
}
