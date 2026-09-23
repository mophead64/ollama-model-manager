// Package usage notes roughly when each model was last used. Ollama keeps no
// usage history, but every request to a model pushes back the time it will
// be unloaded, so polling the list of loaded models (/api/ps) shows when a
// model is used: it appears, or its unload time moves later. The result is
// only as precise as the poll interval, and only covers time this app was
// running.
package usage

import (
	"context"
	"log/slog"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// Ollama is the part of the Ollama client the tracker needs.
type Ollama interface {
	Running(ctx context.Context) ([]ollama.RunningModel, error)
}

// Store is where sightings are recorded.
type Store interface {
	MarkModelUsed(ctx context.Context, model string, at time.Time) error
}

// Tracker polls Ollama and records models as used when it sees them used.
type Tracker struct {
	ol       Ollama
	st       Store
	interval time.Duration
	log      *slog.Logger

	// Each loaded model's unload time at the last poll; nil before the first.
	seen map[string]time.Time
	// Whether the last poll failed, so an outage is logged once, not every poll.
	failing bool
}

func New(ol Ollama, st Store, interval time.Duration, log *slog.Logger) *Tracker {
	return &Tracker{ol: ol, st: st, interval: interval, log: log}
}

// Run polls until ctx is cancelled.
func (t *Tracker) Run(ctx context.Context) {
	tick := time.NewTicker(t.interval)
	defer tick.Stop()
	for {
		t.poll(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// poll compares what's loaded now with the previous poll. The first poll only
// takes stock: models already loaded when the app starts were used at some
// point, but there's no telling when.
func (t *Tracker) poll(ctx context.Context, now time.Time) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	running, err := t.ol.Running(pctx)
	if err != nil {
		if !t.failing {
			t.log.Warn("usage tracking: can't list loaded models", "error", err)
		}
		// What was loaded is kept: once Ollama's back, a model used in the
		// meantime has a later unload time (or is new) and is still caught,
		// while idle ones aren't miscounted.
		t.failing = true
		return
	}
	t.failing = false

	first := t.seen == nil
	next := make(map[string]time.Time, len(running))
	for _, m := range running {
		next[m.Name] = m.ExpiresAt
		prev, wasLoaded := t.seen[m.Name]
		// A second's slack: the same expiry can come back re-serialised.
		if first || (wasLoaded && !m.ExpiresAt.After(prev.Add(time.Second))) {
			continue
		}
		if err := t.st.MarkModelUsed(ctx, m.Name, now); err != nil {
			t.log.Warn("usage tracking: can't record use", "model", m.Name, "error", err)
		}
	}
	t.seen = next
}
