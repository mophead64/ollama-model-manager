package store

import (
	"context"
	"strings"
	"testing"
)

func TestModelTests(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	id, err := st.CreateModelTest(ctx, "Haiku", "Write a haiku.", []string{"qwen3:8b", "llama3.2:latest"}, 2, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateModelTest(ctx, "x", "y", nil, 1, "admin"); err == nil {
		t.Error("a test with no models should be refused")
	}

	got, err := st.GetModelTest(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("get = %v, %v", got, err)
	}
	if got.Status != TestQueued || got.Total != 4 || got.Done != 0 || strings.Join(got.Models, ",") != "qwen3:8b,llama3.2:latest" || !got.Active() {
		t.Fatalf("new test = %+v", got)
	}
	if next, _ := st.NextQueuedModelTest(ctx); next == nil || next.ID != id {
		t.Fatalf("next queued = %+v", next)
	}
	if ok, err := st.DeleteModelTest(ctx, id); ok || err != nil {
		t.Errorf("deleting a queued test = %v, %v; want refused", ok, err)
	}

	// Runs go model by model, in the order picked.
	st.StartModelTest(ctx, id)
	r, _ := st.NextPendingRun(ctx, id)
	if r == nil || r.Model != "qwen3:8b" || r.Iteration != 1 {
		t.Fatalf("first run = %+v", r)
	}
	st.StartRun(ctx, r.ID)
	r.Status, r.Response, r.Tokens, r.EvalMS, r.TotalMS = TestCompleted, "Leaves fall", 50, 2000, 3000
	if err := st.FinishRun(ctx, *r); err != nil {
		t.Fatal(err)
	}
	if r2, _ := st.NextPendingRun(ctx, id); r2 == nil || r2.Model != "qwen3:8b" || r2.Iteration != 2 {
		t.Fatalf("second run = %+v", r2)
	}

	// An app restart mid-run: the run and test go back to be done again.
	r2, _ := st.NextPendingRun(ctx, id)
	st.StartRun(ctx, r2.ID)
	if n, err := st.RequeueInterruptedModelTests(ctx); n != 1 || err != nil {
		t.Fatalf("requeue = %d, %v", n, err)
	}
	if again, _ := st.NextPendingRun(ctx, id); again == nil || again.ID != r2.ID {
		t.Errorf("interrupted run should be next again: %+v", again)
	}
	if got, _ := st.GetModelTest(ctx, id); got.Status != TestQueued || got.Done != 1 {
		t.Errorf("after requeue = %+v", got)
	}

	// Cancelling marks what's left.
	st.StartModelTest(ctx, id)
	if err := st.FinishModelTest(ctx, id, TestCancelled); err != nil {
		t.Fatal(err)
	}
	runs, _ := st.ModelTestRuns(ctx, id)
	if len(runs) != 4 || runs[0].Status != TestCompleted || runs[0].TokensPerSec() != 25 || runs[3].Status != TestCancelled {
		t.Errorf("runs after cancel = %+v", runs)
	}
	if got, _ := st.GetModelTest(ctx, id); got.Done != 4 || got.Active() {
		t.Errorf("cancelled test = %+v", got)
	}

	// Queued tests can be cancelled directly; finished ones deleted.
	id2, _ := st.CreateModelTest(ctx, "Second", "Hi", []string{"qwen3:8b"}, 1, "admin")
	if ok, err := st.CancelQueuedModelTest(ctx, id2); !ok || err != nil {
		t.Errorf("cancel queued = %v, %v", ok, err)
	}
	if all, _ := st.ModelTests(ctx); len(all) != 2 || all[0].ID != id2 {
		t.Errorf("tests should be newest first: %+v", all)
	}
	if ok, err := st.DeleteModelTest(ctx, id); !ok || err != nil {
		t.Errorf("delete finished = %v, %v", ok, err)
	}
	if runs, _ := st.ModelTestRuns(ctx, id); len(runs) != 0 {
		t.Errorf("deleting a test should delete its runs: %+v", runs)
	}
}

func TestModelTestExtras(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	id, _ := st.CreateModelTest(ctx, "A", "Hi", []string{"qwen3:8b", "llama3.2:latest"}, 2, "admin")
	if n, err := st.CountActiveModelTests(ctx); n != 1 || err != nil {
		t.Errorf("active = %d, %v", n, err)
	}
	runs, _ := st.ModelTestRuns(ctx, id)
	for i, ms := range []int64{1000, 3000} { // qwen3's two runs
		r := runs[i]
		r.Status, r.TotalMS = TestCompleted, ms
		st.FinishRun(ctx, r)
	}
	failed := runs[2]
	failed.Status, failed.TotalMS = TestFailed, 99999
	st.FinishRun(ctx, failed)
	times, err := st.ModelRunTimes(ctx)
	if err != nil || len(times) != 1 || times["qwen3:8b"] != 2000 {
		t.Errorf("run times = %v, %v; want only qwen3's completed runs, averaged", times, err)
	}

	tid, err := st.SaveModelTestTemplate(ctx, ModelTestTemplate{Name: "Jokes", Prompt: "Tell a joke\nplease", Models: []string{"qwen3:8b", "hf.co/a/b:Q4_K_M"}, Repeats: 3, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	st.SaveModelTestTemplate(ctx, ModelTestTemplate{Name: "another", Prompt: "x", Models: []string{"qwen3:8b"}, Repeats: 1, CreatedBy: "admin"})
	got, _ := st.GetModelTestTemplate(ctx, tid)
	if got == nil || got.Prompt != "Tell a joke\nplease" || strings.Join(got.Models, ",") != "qwen3:8b,hf.co/a/b:Q4_K_M" || got.Repeats != 3 {
		t.Fatalf("template = %+v", got)
	}
	if all, _ := st.ModelTestTemplates(ctx); len(all) != 2 || all[0].Name != "another" {
		t.Errorf("templates should be by name, ignoring case: %+v", all)
	}
	st.DeleteModelTestTemplate(ctx, tid)
	if got, _ := st.GetModelTestTemplate(ctx, tid); got != nil {
		t.Error("deleted template still there")
	}
}
