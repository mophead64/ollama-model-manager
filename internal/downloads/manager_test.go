package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// fakeOllama serves /api/version, /api/tags and a scripted /api/pull:
//   - "broken:*" fails after the manifest
//   - "slow:*" trickles progress until the client goes away
//   - anything else pulls two layers (900 + 100 bytes) and succeeds
func fakeOllama(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.12.3"}`))
			return
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"installed:latest"}]}`))
			return
		}
		var req struct{ Model string }
		json.NewDecoder(r.Body).Decode(&req)
		fl := w.(http.Flusher)
		send := func(s string) { fmt.Fprintln(w, s); fl.Flush() }
		send(`{"status":"pulling manifest"}`)
		switch {
		case strings.HasPrefix(req.Model, "broken:"):
			send(`{"error":"pull model manifest: file does not exist"}`)
		case strings.HasPrefix(req.Model, "slow:"):
			for i := int64(0); ; i++ {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
				send(fmt.Sprintf(`{"status":"pulling aaa","digest":"sha256:aaa","total":1000000,"completed":%d}`, i*10))
			}
		default:
			for _, c := range []int{0, 300, 600, 900} {
				send(fmt.Sprintf(`{"status":"pulling aaa","digest":"sha256:aaa","total":900,"completed":%d}`, c))
			}
			send(`{"status":"pulling bbb","digest":"sha256:bbb","total":100,"completed":100}`)
			send(`{"status":"verifying sha256 digest"}`)
			send(`{"status":"writing manifest"}`)
			send(`{"status":"success"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// registryTransport answers manifest checks locally: 404 for models named
// "missing*", a network error for "offline*", 200 otherwise.
type registryTransport struct{}

func (registryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch {
	case strings.Contains(r.URL.Path, "/missing"):
		return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
	case strings.Contains(r.URL.Path, "/offline"):
		return nil, errors.New("dial tcp: no route to host")
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(""))}, nil
}

func newTestManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := New(st, ollama.New(fakeOllama(t).URL), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.registry = &http.Client{Transport: registryTransport{}}
	return m, st
}

// waitFor polls until the download reaches status, failing after 5s.
func waitFor(t *testing.T, st *store.Store, id int64, status string) *store.Download {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, _ := st.GetDownload(context.Background(), id)
		if d != nil && d.Status == status {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("download %d: status %v, want %s", id, d, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func logText(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	logs, _ := st.DownloadLogs(context.Background(), id)
	var b strings.Builder
	for _, l := range logs {
		b.WriteString(l.Level + ": " + l.Message + "\n")
	}
	return b.String()
}

func runManager(t *testing.T, m *Manager) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestEnqueueChecks(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)

	if _, _, _, err := m.Enqueue(ctx, "missing-model", "admin"); err == nil || !strings.Contains(err.Error(), "wasn't found") {
		t.Errorf("missing model err = %v", err)
	}
	if _, _, _, err := m.Enqueue(ctx, "not a name", "admin"); err == nil {
		t.Error("invalid name should be rejected")
	}

	id, name, warn, err := m.Enqueue(ctx, "ollama pull good", "admin")
	if err != nil || name != "good:latest" || warn != "" {
		t.Fatalf("Enqueue = %d %q %q %v", id, name, warn, err)
	}
	if _, _, _, err := m.Enqueue(ctx, "GOOD:latest", "admin"); !errors.Is(err, store.ErrAlreadyQueued) {
		t.Errorf("duplicate err = %v", err)
	}

	id2, _, warn, err := m.Enqueue(ctx, "offline-model", "admin")
	if err != nil || !strings.Contains(warn, "queued anyway") {
		t.Errorf("unreachable registry should queue with a warning: %q %v", warn, err)
	}
	if !strings.Contains(logText(t, st, id2), "warn: Couldn't verify") {
		t.Errorf("log should record the failed check:\n%s", logText(t, st, id2))
	}

	id3, _, _, _ := m.Enqueue(ctx, "installed", "admin")
	if !strings.Contains(logText(t, st, id3), "Already installed") {
		t.Error("re-pulling an installed model should be noted in the log")
	}
}

func TestQueueProcessesInOrder(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	a, _, _, _ := m.Enqueue(ctx, "good", "admin")
	b, _, _, _ := m.Enqueue(ctx, "broken", "admin")
	runManager(t, m)

	d := waitFor(t, st, a, store.DownloadCompleted)
	if d.CompletedBytes != 1000 || d.TotalBytes != 1000 {
		t.Errorf("progress = %d/%d, want 1000/1000 across both layers", d.CompletedBytes, d.TotalBytes)
	}
	log := logText(t, st, a)
	for _, want := range []string{"Starting download", "pulling aaa (900 B)", "pulling bbb (100 B)", "30%", "90%", "Download complete"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Count(log, "pulling aaa") != 1 {
		t.Errorf("each status should be logged once:\n%s", log)
	}

	d = waitFor(t, st, b, store.DownloadFailed)
	if d.Error != "pull model manifest: file does not exist" || !strings.Contains(logText(t, st, b), "error: Failed: pull model manifest") {
		t.Errorf("failure not recorded: %q\n%s", d.Error, logText(t, st, b))
	}

	// Retry puts it back in the queue; it fails again, on attempt 2.
	if _, _, err := m.Retry(ctx, b, "admin", ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	d = waitFor(t, st, b, store.DownloadFailed)
	if d.Attempts != 2 || !strings.Contains(logText(t, st, b), "attempt 2") {
		t.Errorf("attempts = %d\n%s", d.Attempts, logText(t, st, b))
	}
}

func TestCancel(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	slow, _, _, _ := m.Enqueue(ctx, "slow", "admin")
	queued, _, _, _ := m.Enqueue(ctx, "good", "admin")

	// Cancelling a queued download just takes it out of the queue.
	m.Cancel(ctx, queued)
	waitFor(t, st, queued, store.DownloadCancelled)

	runManager(t, m)
	waitFor(t, st, slow, store.DownloadDownloading)
	deadline := time.Now().Add(5 * time.Second)
	for m.Live() == nil || m.Live().Completed == 0 {
		if time.Now().After(deadline) {
			t.Fatal("live progress never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if l := m.Live(); l.Model != "slow:latest" || l.Total != 1000000 {
		t.Errorf("live = %+v", l)
	}
	m.Cancel(ctx, slow)
	waitFor(t, st, slow, store.DownloadCancelled)
	if !strings.Contains(logText(t, st, slow), "Cancelled at") {
		t.Errorf("log:\n%s", logText(t, st, slow))
	}
	if m.Live() != nil {
		t.Error("live state should clear after cancel")
	}
}

func TestShutdownRequeues(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	id, _, _, _ := m.Enqueue(ctx, "slow", "admin")
	stop := runManager(t, m)
	waitFor(t, st, id, store.DownloadDownloading)
	stop()

	waitFor(t, st, id, store.DownloadQueued)
	if !strings.Contains(logText(t, st, id), "resume when the app restarts") {
		t.Errorf("log:\n%s", logText(t, st, id))
	}
}

func TestRequeueInterruptedOnStart(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	id, _, _, _ := m.Enqueue(ctx, "good", "admin")
	st.StartDownload(ctx, id) // as if the process died mid-download

	runManager(t, m)
	waitFor(t, st, id, store.DownloadCompleted)
	if !strings.Contains(logText(t, st, id), "Interrupted by an app restart") {
		t.Errorf("log:\n%s", logText(t, st, id))
	}
}

func TestTrackerSpeed(t *testing.T) {
	tr := newTracker()
	t0 := time.Now()
	// 5 MB already on disk from an earlier attempt, then 1 MB/s for 10s.
	for i := 0; i <= 10; i++ {
		tr.update(ollama.PullProgress{Status: "pulling x", Digest: "x", Total: 100 << 20, Completed: 5<<20 + int64(i)<<20}, t0.Add(time.Duration(i)*time.Second))
	}
	if got := tr.speed(); got < 0.99*(1<<20) || got > 1.01*(1<<20) {
		t.Errorf("speed = %.0f, want ~1 MiB/s (resumed bytes shouldn't count)", got)
	}
	if c, tot := tr.bytes(); c != 15<<20 || tot != 100<<20 {
		t.Errorf("bytes = %d/%d", c, tot)
	}
}

func TestRetryWithNewName(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	id, _, _, _ := m.Enqueue(ctx, "broken", "admin")
	runManager(t, m)
	waitFor(t, st, id, store.DownloadFailed)

	// A name the registry doesn't know is refused, leaving the download alone.
	if _, _, err := m.Retry(ctx, id, "admin", "missing-model"); err == nil || !strings.Contains(err.Error(), "wasn't found") {
		t.Errorf("unknown new name err = %v", err)
	}
	if d, _ := st.GetDownload(ctx, id); d.Model != "broken:latest" || d.Status != store.DownloadFailed {
		t.Errorf("rejected retry changed the download: %+v", d)
	}

	// A good name switches the download over and it succeeds.
	name, warn, err := m.Retry(ctx, id, "josh", "ollama pull good:v2")
	if err != nil || name != "good:v2" || warn != "" {
		t.Fatalf("Retry = %q %q %v", name, warn, err)
	}
	d := waitFor(t, st, id, store.DownloadCompleted)
	if d.Model != "good:v2" || d.Attempts != 2 {
		t.Errorf("download = %+v", d)
	}
	log := logText(t, st, id)
	for _, want := range []string{"Retry requested by josh, changing the model from broken:latest to good:v2", "Found good:v2 on registry.ollama.ai", "Download complete"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
}

func TestRetryNewNameAlreadyQueued(t *testing.T) {
	ctx := context.Background()
	m, st := newTestManager(t)
	failed, _, _, _ := m.Enqueue(ctx, "gemma", "admin")
	st.FinishDownload(ctx, failed, store.DownloadFailed, "boom")
	m.Enqueue(ctx, "qwen3:8b", "admin")

	if _, _, err := m.Retry(ctx, failed, "admin", "qwen3:8b"); !errors.Is(err, store.ErrAlreadyQueued) {
		t.Errorf("switching to a queued model err = %v", err)
	}
}

// flakyOllama is an Ollama that can go away. While down, /api/version answers
// 503 (as a proxy in front of a stopped Ollama would). Pulls of "drop:*" are
// cut off mid-stream the first dropFirst times (always, if it's negative),
// having got to 300 of 1000 bytes; after that, and for any other model, a
// pull succeeds.
type flakyOllama struct {
	mu        sync.Mutex
	down      bool
	dropFirst int
	pulls     int
}

func (f *flakyOllama) setDown(v bool) { f.mu.Lock(); f.down = v; f.mu.Unlock() }

func (f *flakyOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	switch r.URL.Path {
	case "/api/version":
		if down {
			http.Error(w, `{"error":"ollama is restarting"}`, http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"version":"0.12.3"}`))
		return
	case "/api/tags":
		w.Write([]byte(`{"models":[]}`))
		return
	}
	var req struct{ Model string }
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	f.pulls++
	drop := strings.HasPrefix(req.Model, "drop:") && (f.dropFirst < 0 || f.pulls <= f.dropFirst)
	f.mu.Unlock()

	fl := w.(http.Flusher)
	send := func(s string) { fmt.Fprintln(w, s); fl.Flush() }
	send(`{"status":"pulling manifest"}`)
	send(`{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":300}`)
	if drop {
		// Ollama stops: the connection goes without the stream finishing.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
		return
	}
	send(`{"status":"pulling aaa","digest":"sha256:aaa","total":1000,"completed":1000}`)
	send(`{"status":"success"}`)
}

func newFlakyManager(t *testing.T, f *flakyOllama) (*Manager, *store.Store) {
	t.Helper()
	m, st := newTestManager(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	m.ol = ollama.New(srv.URL)
	m.retryEvery = 10 * time.Millisecond
	return m, st
}

func TestResumesWhenOllamaComesBack(t *testing.T) {
	ctx := context.Background()
	f := &flakyOllama{dropFirst: 1}
	m, st := newFlakyManager(t, f)
	id, _, _, _ := m.Enqueue(ctx, "drop:big", "admin")
	runManager(t, m)

	d := waitFor(t, st, id, store.DownloadCompleted)
	if d.Attempts != 2 || d.Error != "" {
		t.Errorf("download = %+v, want completed on attempt 2", d)
	}
	log := logText(t, st, id)
	for _, want := range []string{"warn: Lost the connection to Ollama at 300 B of 1000 B", "It will resume when Ollama is back", "Starting download (attempt 2)", "Download complete"} {
		if !strings.Contains(log, want) {
			t.Errorf("log missing %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "Failed") {
		t.Errorf("an Ollama restart shouldn't fail the download:\n%s", log)
	}
}

func TestQueueWaitsWhileOllamaIsDown(t *testing.T) {
	ctx := context.Background()
	f := &flakyOllama{down: true}
	m, st := newFlakyManager(t, f)
	id, _, _, _ := m.Enqueue(ctx, "good", "admin")
	runManager(t, m)

	deadline := time.Now().Add(5 * time.Second)
	for m.Waiting() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the queue should be held while Ollama is down")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w := m.Waiting(); !strings.Contains(w.Err, "ollama is restarting") || w.Since.IsZero() {
		t.Errorf("waiting = %+v", w)
	}
	time.Sleep(50 * time.Millisecond) // several retries' worth
	if d, _ := st.GetDownload(ctx, id); d.Status != store.DownloadQueued || d.Attempts != 0 {
		t.Errorf("nothing should start while Ollama is down: %+v", d)
	}
	f.mu.Lock()
	pulls := f.pulls
	f.mu.Unlock()
	if pulls != 0 {
		t.Errorf("%d pulls sent to a down Ollama", pulls)
	}

	f.setDown(false)
	waitFor(t, st, id, store.DownloadCompleted)
	if m.Waiting() != nil {
		t.Error("the hold should clear once Ollama is back")
	}
}

func TestGivesUpOnRepeatedDrops(t *testing.T) {
	ctx := context.Background()
	f := &flakyOllama{dropFirst: -1}
	m, st := newFlakyManager(t, f)
	id, _, _, _ := m.Enqueue(ctx, "drop:always", "admin")
	runManager(t, m)

	d := waitFor(t, st, id, store.DownloadFailed)
	if d.Attempts != maxAutoResumes+1 || !strings.Contains(d.Error, "lost the connection to Ollama 5 times in a row without getting any further") {
		t.Errorf("download = %+v", d)
	}
}

func TestLostOllama(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{ollama.ErrPullCutOff, true},
		{fmt.Errorf("contact ollama at x: %w", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}), true},
		{io.ErrUnexpectedEOF, true},
		{&ollama.StatusError{StatusCode: 503}, true},
		{&ollama.StatusError{StatusCode: 502}, true},
		{&ollama.StatusError{StatusCode: 500}, false},
		{&ollama.StatusError{StatusCode: 404}, false},
		{&ollama.PullError{Message: "pull model manifest: file does not exist"}, false},
		{errors.New("unexpected response from ollama"), false},
	} {
		if got := lostOllama(c.err); got != c.want {
			t.Errorf("lostOllama(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
