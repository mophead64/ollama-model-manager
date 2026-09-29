// Package modeltest runs model tests: one prompt sent to one or more models,
// a number of times each, with every reply and its timings stored so the
// models can be compared. Tests queue and run one at a time in the
// background, like downloads, so they carry on without a browser open.
package modeltest

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// maxReply caps how much of a reply is kept, so a model that rambles on
// doesn't fill the database.
const maxReply = 256 << 10

var errCancelled = errors.New("cancelled by user")

type Runner struct {
	st  *store.Store
	ol  *ollama.Client
	log *slog.Logger

	wake chan struct{}

	mu       sync.Mutex
	cancelID int64
	cancel   context.CancelCauseFunc
}

func New(st *store.Store, ol *ollama.Client, log *slog.Logger) *Runner {
	return &Runner{st: st, ol: ol, log: log, wake: make(chan struct{}, 1)}
}

// Wake nudges the runner to look at the queue now, after a test is created.
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Cancel stops a test: a queued one is taken out of the queue, the running
// one stops after abandoning the reply in progress.
func (r *Runner) Cancel(ctx context.Context, id int64) error {
	r.mu.Lock()
	if r.cancelID == id && r.cancel != nil {
		r.cancel(errCancelled)
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	_, err := r.st.CancelQueuedModelTest(ctx, id)
	return err
}

// Run works through the queue until ctx is cancelled. Tests a previous run
// left part-way through carry on from the run that was going.
func (r *Runner) Run(ctx context.Context) {
	if n, err := r.st.RequeueInterruptedModelTests(ctx); err != nil {
		r.log.Error("failed to requeue interrupted model tests", "error", err)
	} else if n > 0 {
		r.log.Info("resuming model tests interrupted by a restart", "tests", n)
	}
	for {
		t, err := r.st.NextQueuedModelTest(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			r.log.Error("reading model test queue failed", "error", err)
		}
		if t == nil {
			select {
			case <-ctx.Done():
				return
			case <-r.wake:
			case <-time.After(time.Minute): // belt and braces; Wake is the normal trigger
			}
			continue
		}
		r.process(ctx, t)
		if ctx.Err() != nil {
			return
		}
	}
}

func (r *Runner) process(parent context.Context, t *store.ModelTest) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	r.mu.Lock()
	r.cancelID, r.cancel = t.ID, cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.cancelID, r.cancel = 0, nil
		r.mu.Unlock()
	}()

	if err := r.st.StartModelTest(parent, t.ID); err != nil {
		r.log.Error("start model test failed", "test", t.ID, "error", err)
		return
	}
	r.log.Info("model test started", "test", t.ID, "models", t.Models, "repeats", t.Repeats)

	// Each model is unloaded once its runs are done, so the next one has the
	// GPU to itself rather than spilling onto the CPU beside it. A model that
	// was loaded before the test got to it is left loaded: someone's using it.
	var current string
	var leaveLoaded bool
	release := func() {
		if current != "" && !leaveLoaded && parent.Err() == nil {
			r.unload(parent, t.ID, current)
		}
		current = ""
	}
	defer release()

	for {
		run, err := r.st.NextPendingRun(parent, t.ID)
		if err != nil {
			if parent.Err() == nil {
				r.log.Error("reading model test runs failed", "test", t.ID, "error", err)
				r.finish(parent, t.ID, store.TestFailed)
			}
			return
		}
		if run == nil {
			break
		}
		if run.Model != current {
			release()
			current, leaveLoaded = run.Model, r.loaded(parent, run.Model)
		}
		if err := r.st.StartRun(parent, run.ID); err != nil {
			r.log.Error("start model test run failed", "test", t.ID, "error", err)
			return
		}
		r.do(ctx, t.Prompt, run)

		switch {
		case parent.Err() != nil:
			// Shutting down: the run stays "running" and is done again on
			// the next start (see RequeueInterruptedModelTests).
			return
		case context.Cause(ctx) == errCancelled:
			run.Status, run.Error = store.TestCancelled, "Cancelled"
			r.st.FinishRun(parent, *run)
			r.finish(parent, t.ID, store.TestCancelled)
			return
		}
		if err := r.st.FinishRun(parent, *run); err != nil {
			r.log.Error("save model test run failed", "test", t.ID, "error", err)
		}
	}

	// It failed only if nothing worked (a model that's since been deleted,
	// Ollama down throughout); one bad run is shown against that run.
	status := store.TestFailed
	if runs, err := r.st.ModelTestRuns(parent, t.ID); err == nil {
		for _, run := range runs {
			if run.Status == store.TestCompleted {
				status = store.TestCompleted
				break
			}
		}
	}
	release() // before the test's marked done, so it's out of memory by then
	r.finish(parent, t.ID, status)
}

// loaded reports whether model is in memory now. If Ollama can't say, it's
// taken as not loaded, so the test tidies up after itself.
func (r *Runner) loaded(ctx context.Context, model string) bool {
	running, err := r.ol.Running(ctx)
	if err != nil {
		return false
	}
	for _, m := range running {
		if m.Name == model || m.Model == model {
			return true
		}
	}
	return false
}

func (r *Runner) unload(ctx context.Context, testID int64, model string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := r.ol.Unload(ctx, model); err != nil {
		r.log.Warn("unload after model test failed", "test", testID, "model", model, "error", err)
		return
	}
	r.log.Info("unloaded after model test", "test", testID, "model", model)
}

func (r *Runner) finish(ctx context.Context, id int64, status string) {
	if err := r.st.FinishModelTest(ctx, id, status); err != nil {
		r.log.Error("finish model test failed", "test", id, "error", err)
		return
	}
	r.log.Info("model test finished", "test", id, "status", status)
}

// do sends the prompt, as a fresh conversation, and fills in run's result.
func (r *Runner) do(ctx context.Context, prompt string, run *store.ModelTestRun) {
	var reply, thinking strings.Builder
	var last ollama.ChatChunk
	err := r.ol.Chat(ctx, run.Model, []ollama.ChatMessage{{Role: "user", Content: prompt}}, func(ch ollama.ChatChunk) {
		if reply.Len() < maxReply {
			reply.WriteString(ch.Message.Content)
		}
		if thinking.Len() < maxReply {
			thinking.WriteString(ch.Message.Thinking)
		}
		if ch.Done {
			last = ch
		}
	})
	run.Response, run.Thinking = reply.String(), thinking.String()
	if err != nil {
		run.Status, run.Error = store.TestFailed, err.Error()
		return
	}
	run.Status = store.TestCompleted
	run.PromptTokens, run.Tokens = last.PromptEvalCount, last.EvalCount
	run.LoadMS, run.PromptMS = last.LoadDuration/1e6, last.PromptEvalDuration/1e6
	run.EvalMS, run.TotalMS = last.EvalDuration/1e6, last.TotalDuration/1e6
}
