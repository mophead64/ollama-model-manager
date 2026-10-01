package store

import "testing"

func TestQueuedBytes(t *testing.T) {
	st := openTest(t)
	ctx := t.Context()
	const gb = 1 << 30

	running, _ := st.EnqueueDownload(ctx, "running:8b", "admin", 20*gb)
	st.EnqueueDownload(ctx, "queued:8b", "admin", 20*gb)
	st.EnqueueDownload(ctx, "unknown:8b", "admin", 0) // size unknown: counts as nothing
	done, _ := st.EnqueueDownload(ctx, "done:8b", "admin", 5*gb)
	if err := st.StartDownload(ctx, running); err != nil {
		t.Fatal(err)
	}
	// The pull reports its own total (a little different) and progress.
	if err := st.UpdateDownloadProgress(ctx, running, 15*gb, 21*gb); err != nil {
		t.Fatal(err)
	}
	st.FinishDownload(ctx, done, DownloadCompleted, "")

	if n, err := st.QueuedBytes(ctx); err != nil || n != 26*gb {
		t.Errorf("QueuedBytes = %.1f GB, %v; want 6 GB left of the running one + 20 GB queued", float64(n)/gb, err)
	}
}
