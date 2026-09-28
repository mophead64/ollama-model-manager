package modeltest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// fakeChat replies "Hi from <model>" with fixed timings. "gone:latest" isn't
// there; "slow:latest" waits until release is closed (or the request goes).
// /api/ps reports loadedBefore as loaded, and /api/generate (an unload)
// is recorded in events, as "unload <model>", after each "chat <model>".
func fakeChat(t *testing.T, release chan struct{}, loadedBefore ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &req)
		switch r.URL.Path {
		case "/api/ps":
			var ms []string
			for _, m := range loadedBefore {
				ms = append(ms, `{"name":"`+m+`","model":"`+m+`"}`)
			}
			w.Write([]byte(`{"models":[` + strings.Join(ms, ",") + `]}`))
			return
		case "/api/generate":
			record("unload " + req.Model)
			w.Write([]byte(`{"done":true}`))
			return
		}
		calls.Add(1)
		record("chat " + req.Model)
		switch {
		case strings.Contains(string(body), `"model":"gone:latest"`):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"model 'gone:latest' not found"}`))
			return
		case strings.Contains(string(body), `"model":"slow:latest"`):
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		model := "qwen3:8b"
		if strings.Contains(string(body), `"model":"llama3.2:latest"`) {
			model = "llama3.2:latest"
		}
		w.Write([]byte(`{"message":{"content":"","thinking":"hmm"},"done":false}` + "\n"))
		w.Write([]byte(`{"message":{"content":"Hi from ` + model + `"},"done":false}` + "\n"))
		w.Write([]byte(`{"message":{"content":""},"done":true,"total_duration":3000000000,"load_duration":1000000000,` +
			`"prompt_eval_count":10,"prompt_eval_duration":200000000,"eval_count":40,"eval_duration":2000000000}` + "\n"))
	}))
	t.Cleanup(srv.Close)
	events.Store(&[]string{})
	return srv, &calls
}

var (
	events   atomic.Pointer[[]string]
	eventsMu sync.Mutex
)

func record(e string) {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	*events.Load() = append(*events.Load(), e)
}

func recorded() string {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	return strings.Join(*events.Load(), ", ")
}

func newRunner(t *testing.T, base string) (*Runner, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, ollama.New(base), slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func waitFor(t *testing.T, st *store.Store, id int64, done func(store.ModelTest) bool) store.ModelTest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if mt, _ := st.GetModelTest(context.Background(), id); mt != nil && done(*mt) {
			return *mt
		}
		time.Sleep(10 * time.Millisecond)
	}
	mt, _ := st.GetModelTest(context.Background(), id)
	t.Fatalf("test %d didn't get there: %+v", id, mt)
	return store.ModelTest{}
}

func TestRunnerRunsEachModel(t *testing.T) {
	srv, _ := fakeChat(t, nil)
	r, st := newRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)

	id, _ := st.CreateModelTest(ctx, "Greeting", "Say hi", []string{"qwen3:8b", "gone:latest", "llama3.2:latest"}, 2, "admin")
	r.Wake()
	mt := waitFor(t, st, id, func(mt store.ModelTest) bool { return !mt.Active() })
	if mt.Status != store.TestCompleted || mt.Done != 6 || mt.StartedAt == nil || mt.FinishedAt == nil {
		t.Fatalf("finished test = %+v", mt)
	}

	runs, _ := st.ModelTestRuns(ctx, id)
	for _, run := range runs {
		switch run.Model {
		case "gone:latest":
			// A missing model fails its runs without stopping the test.
			if run.Status != store.TestFailed || !strings.Contains(run.Error, "not found") {
				t.Errorf("missing model's run = %+v", run)
			}
		default:
			if run.Status != store.TestCompleted || run.Response != "Hi from "+run.Model || run.Thinking != "hmm" ||
				run.Tokens != 40 || run.PromptTokens != 10 || run.LoadMS != 1000 || run.EvalMS != 2000 || run.TotalMS != 3000 {
				t.Errorf("run = %+v", run)
			}
		}
	}
}

func TestRunnerAllFailed(t *testing.T) {
	srv, _ := fakeChat(t, nil)
	r, st := newRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)

	id, _ := st.CreateModelTest(ctx, "Nope", "Say hi", []string{"gone:latest"}, 1, "admin")
	r.Wake()
	if mt := waitFor(t, st, id, func(mt store.ModelTest) bool { return !mt.Active() }); mt.Status != store.TestFailed {
		t.Errorf("a test where nothing worked = %s, want failed", mt.Status)
	}
}

func TestRunnerCancel(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv, _ := fakeChat(t, release)
	r, st := newRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	running, _ := st.CreateModelTest(ctx, "Slow", "Say hi", []string{"slow:latest", "qwen3:8b"}, 1, "admin")
	queued, _ := st.CreateModelTest(ctx, "Next", "Say hi", []string{"qwen3:8b"}, 1, "admin")
	go r.Run(ctx)
	waitFor(t, st, running, func(mt store.ModelTest) bool { return mt.Status == store.TestRunning })

	// The queued one is dropped without running.
	if err := r.Cancel(ctx, queued); err != nil {
		t.Fatal(err)
	}
	if mt, _ := st.GetModelTest(ctx, queued); mt.Status != store.TestCancelled {
		t.Errorf("cancelled queued test = %s", mt.Status)
	}

	// The running one stops mid-reply; what's left is cancelled too.
	time.Sleep(50 * time.Millisecond) // let the slow request get to Ollama
	if err := r.Cancel(ctx, running); err != nil {
		t.Fatal(err)
	}
	mt := waitFor(t, st, running, func(mt store.ModelTest) bool { return !mt.Active() })
	runs, _ := st.ModelTestRuns(ctx, running)
	if mt.Status != store.TestCancelled || runs[0].Status != store.TestCancelled || runs[1].Status != store.TestCancelled {
		t.Errorf("cancelled running test = %s, runs %+v", mt.Status, runs)
	}
}

func TestRunnerResumesAfterRestart(t *testing.T) {
	srv, calls := fakeChat(t, nil)
	r, st := newRunner(t, srv.URL)
	ctx := t.Context()

	// As a restart would leave it: one run done, the next mid-flight.
	id, _ := st.CreateModelTest(ctx, "Resume", "Say hi", []string{"qwen3:8b"}, 2, "admin")
	st.StartModelTest(ctx, id)
	first, _ := st.NextPendingRun(ctx, id)
	st.StartRun(ctx, first.ID)
	first.Status, first.Response = store.TestCompleted, "earlier"
	st.FinishRun(ctx, *first)
	second, _ := st.NextPendingRun(ctx, id)
	st.StartRun(ctx, second.ID)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go r.Run(runCtx)
	if mt := waitFor(t, st, id, func(mt store.ModelTest) bool { return !mt.Active() }); mt.Status != store.TestCompleted {
		t.Fatalf("resumed test = %+v", mt)
	}
	runs, _ := st.ModelTestRuns(ctx, id)
	if runs[0].Response != "earlier" || runs[1].Response != "Hi from qwen3:8b" || calls.Load() != 1 {
		t.Errorf("only the interrupted run should be redone: %d calls, runs %+v", calls.Load(), runs)
	}
}

func TestRunnerUnloadsEachModelWhenDone(t *testing.T) {
	// llama3.2 was already loaded (someone's using it), so it's left be.
	srv, _ := fakeChat(t, nil, "llama3.2:latest")
	r, st := newRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)

	id, _ := st.CreateModelTest(ctx, "Unload", "Say hi", []string{"qwen3:8b", "llama3.2:latest", "gone:latest"}, 2, "admin")
	r.Wake()
	waitFor(t, st, id, func(mt store.ModelTest) bool { return !mt.Active() })

	want := "chat qwen3:8b, chat qwen3:8b, unload qwen3:8b, " + // before the next model starts
		"chat llama3.2:latest, chat llama3.2:latest, " + // loaded beforehand: kept
		"chat gone:latest, chat gone:latest, unload gone:latest" // even if its runs failed
	if got := recorded(); got != want {
		t.Errorf("events:\n got %s\nwant %s", got, want)
	}
}

func TestRunnerUnloadsWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv, _ := fakeChat(t, release)
	r, st := newRunner(t, srv.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go r.Run(ctx)

	id, _ := st.CreateModelTest(ctx, "Cancel", "Say hi", []string{"slow:latest", "qwen3:8b"}, 1, "admin")
	r.Wake()
	waitFor(t, st, id, func(mt store.ModelTest) bool { return mt.Status == store.TestRunning })
	time.Sleep(50 * time.Millisecond)
	r.Cancel(ctx, id)
	waitFor(t, st, id, func(mt store.ModelTest) bool { return !mt.Active() })
	deadline := time.Now().Add(2 * time.Second)
	for recorded() != "chat slow:latest, unload slow:latest" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := recorded(); got != "chat slow:latest, unload slow:latest" {
		t.Errorf("events = %s; the cancelled model should be unloaded, and nothing more run", got)
	}
}
