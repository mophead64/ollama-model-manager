// Package downloads runs the model download queue: a single background worker
// that pulls queued models through Ollama one at a time, recording progress
// and a per-download log in the store. It runs independently of any browser,
// so a download carries on after the user closes the page, and the queue
// survives restarts (an interrupted download resumes where Ollama left off).
package downloads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

const (
	// If Ollama sends nothing for this long the pull is treated as stuck and
	// failed. Ollama reports progress several times a second while bytes are
	// flowing, and retries flaky connections itself, so this is generous.
	stallTimeout = 10 * time.Minute
	// How often live progress is written to the database (it's shown live
	// from memory; the database copy is for after a restart).
	persistEvery = 5 * time.Second
	// Speed is averaged over this window, so the ETA doesn't jump around.
	speedWindow = 15 * time.Second
)

// Live is a snapshot of the download in progress, for the UI.
type Live struct {
	ID        int64
	Model     string
	Status    string // Ollama's latest status line, e.g. "pulling 6a0746a1ec1a"
	Completed int64
	Total     int64
	Speed     float64       // bytes/sec over the last speedWindow; 0 until known
	ETA       time.Duration // 0 until known
	StartedAt time.Time
}

// Percent is progress through the bytes Ollama has announced so far.
func (l Live) Percent() float64 {
	if l.Total <= 0 {
		return 0
	}
	return min(100, 100*float64(l.Completed)/float64(l.Total))
}

type Manager struct {
	st       *store.Store
	ol       *ollama.Client
	log      *slog.Logger
	registry *http.Client // nil = default; tests point this at a fake

	wake chan struct{}

	mu       sync.Mutex
	live     *Live
	cancelID int64
	cancel   context.CancelCauseFunc
}

func New(st *store.Store, ol *ollama.Client, log *slog.Logger) *Manager {
	return &Manager{st: st, ol: ol, log: log, wake: make(chan struct{}, 1)}
}

var (
	errCancelled = errors.New("cancelled by user")
	errStalled   = fmt.Errorf("no progress from Ollama for %s", stallTimeout)
)

// Enqueue checks a requested model and adds it to the queue. The returned
// warning is non-empty when it was queued but couldn't be fully checked.
func (m *Manager) Enqueue(ctx context.Context, input, requestedBy string) (id int64, name, warning string, err error) {
	c, err := m.check(ctx, input)
	if err != nil {
		return 0, c.name, "", err
	}
	id, err = m.st.EnqueueDownload(ctx, c.name, requestedBy)
	if err != nil {
		return 0, c.name, "", err
	}
	m.logf(id, "info", "Queued by %s", requestedBy)
	m.logCheck(ctx, id, c)
	m.Wake()
	return id, c.name, c.warning, nil
}

// checked is the outcome of vetting a requested model name.
type checked struct {
	name    string // normalised, e.g. "qwen3:8b"
	warning string // for the user, when the registry couldn't be asked
	note    string // for the download's log
}

// check normalises a requested name and asks its registry whether it exists.
// A registry that can't be reached isn't an error (the name might be fine),
// just a warning.
func (m *Manager) check(ctx context.Context, input string) (checked, error) {
	ref, err := ollama.NormalizeName(input)
	if err != nil {
		return checked{}, err
	}
	c := checked{name: ref.String()}
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	switch err := ollama.CheckRegistry(checkCtx, m.registry, ref); {
	case errors.Is(err, ollama.ErrModelNotFound):
		return c, fmt.Errorf("%s wasn't found on %s. Check the name and tag.", c.name, ref.Host)
	case errors.Is(err, ollama.ErrGated):
		c.warning = fmt.Sprintf("%s is a gated Hugging Face model: it only downloads once you've been granted access and Ollama is authorised. See the download's page for how.", c.name)
		c.note = "Gated Hugging Face model: access has to be granted before Ollama can pull it"
	case errors.Is(err, ollama.ErrPrivateOrMissing):
		c.warning = fmt.Sprintf("Hugging Face says %s doesn't exist or is private; queued in case it's private and Ollama has access. If the name is wrong the download will fail.", c.name)
		c.note = "Hugging Face says this repo doesn't exist or is private"
	case err != nil:
		c.warning = fmt.Sprintf("Couldn't check %s exists (%v); queued anyway. If the name is wrong the download will fail.", c.name, err)
		c.note = "Couldn't verify the model with its registry: " + err.Error()
	default:
		c.note = "Found " + c.name + " on " + ref.Host
	}
	return c, nil
}

func (m *Manager) logCheck(ctx context.Context, id int64, c checked) {
	if c.warning != "" {
		m.logf(id, "warn", "%s", c.note)
	} else {
		m.logf(id, "info", "%s", c.note)
	}
	if installed, _ := m.installed(ctx, c.name); installed {
		m.logf(id, "info", "Already installed; pulling again fetches any newer version")
	}
}

func (m *Manager) installed(ctx context.Context, name string) (bool, error) {
	models, err := m.ol.List(ctx)
	if err != nil {
		return false, err
	}
	for _, mdl := range models {
		if strings.EqualFold(mdl.Name, name) {
			return true, nil
		}
	}
	return false, nil
}

// SetRegistryClient overrides the HTTP client used to check models exist in
// their registry (for tests).
func (m *Manager) SetRegistryClient(c *http.Client) { m.registry = c }

// Wake nudges the worker to look at the queue now.
func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Live returns the download in progress, or nil.
func (m *Manager) Live() *Live {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live == nil {
		return nil
	}
	l := *m.live
	return &l
}

// Cancel stops a download: a queued one is taken out of the queue, the
// running one is aborted (Ollama keeps what it downloaded, so a retry resumes).
func (m *Manager) Cancel(ctx context.Context, id int64) error {
	m.mu.Lock()
	if m.cancelID == id && m.cancel != nil {
		m.cancel(errCancelled)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	d, err := m.st.GetDownload(ctx, id)
	if err != nil || d == nil || d.Status != store.DownloadQueued {
		return err
	}
	if err := m.st.FinishDownload(ctx, id, store.DownloadCancelled, ""); err != nil {
		return err
	}
	m.logf(id, "info", "Removed from the queue")
	return nil
}

// Retry puts a failed or cancelled download at the back of the queue. If
// newInput names a different model (say the first attempt had a typo, or a
// different quant is wanted) the download is switched to it, after the same
// checks as a new request; "" or the same name retries as-is.
func (m *Manager) Retry(ctx context.Context, id int64, by, newInput string) (name, warning string, err error) {
	d, err := m.st.GetDownload(ctx, id)
	if err != nil || d == nil || !d.Retryable() {
		return "", "", err
	}
	name = d.Model
	var c *checked
	if strings.TrimSpace(newInput) != "" {
		chk, err := m.check(ctx, newInput)
		if err != nil {
			return chk.name, "", err
		}
		if !strings.EqualFold(chk.name, d.Model) {
			c, name, warning = &chk, chk.name, chk.warning
		}
	}

	// Same guard as Enqueue: don't let a retry duplicate a queued model.
	active, err := m.st.ActiveDownloads(ctx)
	if err != nil {
		return name, "", err
	}
	for _, a := range active {
		if strings.EqualFold(a.Model, name) {
			return name, "", store.ErrAlreadyQueued
		}
	}

	if c != nil {
		if err := m.st.SetDownloadModel(ctx, id, c.name); err != nil {
			return name, "", err
		}
	}
	if err := m.st.RequeueDownload(ctx, id, false); err != nil {
		return name, "", err
	}
	if c != nil {
		m.logf(id, "info", "Retry requested by %s, changing the model from %s to %s", by, d.Model, c.name)
		m.logCheck(ctx, id, *c)
	} else {
		m.logf(id, "info", "Retry requested by %s", by)
	}
	m.Wake()
	return name, warning, nil
}

// Run processes the queue until ctx is cancelled. Downloads a previous run
// left mid-flight are put back at the front of the queue first.
func (m *Manager) Run(ctx context.Context) {
	if ds, err := m.st.RequeueInterrupted(ctx); err != nil {
		m.log.Error("failed to requeue interrupted downloads", "error", err)
	} else {
		for _, d := range ds {
			m.logf(d.ID, "warn", "Interrupted by an app restart; resuming")
		}
	}

	for {
		d, err := m.st.NextQueuedDownload(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.log.Error("reading download queue failed", "error", err)
		}
		if d == nil {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			case <-time.After(time.Minute): // belt and braces; Wake is the normal trigger
			}
			continue
		}
		m.process(ctx, d)
		if ctx.Err() != nil {
			return
		}
	}
}

func (m *Manager) process(parent context.Context, d *store.Download) {
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)

	if err := m.st.StartDownload(parent, d.ID); err != nil {
		m.log.Error("failed to start download", "id", d.ID, "error", err)
		return
	}
	attempt := d.Attempts + 1
	if attempt > 1 {
		m.logf(d.ID, "info", "Starting download (attempt %d)", attempt)
	} else {
		m.logf(d.ID, "info", "Starting download")
	}
	m.log.Info("download started", "model", d.Model, "id", d.ID)

	now := time.Now()
	m.mu.Lock()
	m.live = &Live{ID: d.ID, Model: d.Model, Status: "starting", StartedAt: now}
	m.cancelID, m.cancel = d.ID, cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.live, m.cancelID, m.cancel = nil, 0, nil
		m.mu.Unlock()
	}()

	tr := newTracker()
	stall := time.AfterFunc(stallTimeout, func() { cancel(errStalled) })
	defer stall.Stop()
	var lastPersist time.Time
	nextLogPct := 10

	err := m.ol.Pull(ctx, d.Model, func(p ollama.PullProgress) {
		stall.Reset(stallTimeout)
		t := time.Now()
		if newStatus := tr.update(p, t); newStatus {
			m.logf(d.ID, "info", "%s", describeStatus(p))
		}
		completed, total := tr.bytes()
		speed := tr.speed()
		var eta time.Duration
		if speed > 0 && total > completed {
			eta = time.Duration(float64(total-completed) / speed * float64(time.Second))
		}
		m.mu.Lock()
		if m.live != nil {
			m.live.Status, m.live.Completed, m.live.Total, m.live.Speed, m.live.ETA = p.Status, completed, total, speed, eta
		}
		m.mu.Unlock()

		// A milestone every 10% leaves a trail in the log, so a failure shows
		// how far it got and how fast it was going.
		if total > 0 {
			for pct := 100 * completed / total; pct >= int64(nextLogPct) && nextLogPct < 100; nextLogPct += 10 {
				m.logf(d.ID, "info", "%d%% (%s of %s, %s)", nextLogPct, fmtBytes(completed), fmtBytes(total), fmtSpeed(speed))
			}
		}
		if t.Sub(lastPersist) >= persistEvery {
			lastPersist = t
			if err := m.st.UpdateDownloadProgress(parent, d.ID, completed, total); err != nil {
				m.log.Warn("failed to save download progress", "id", d.ID, "error", err)
			}
		}
	})

	completed, total := tr.bytes()
	bg := context.WithoutCancel(parent) // record the outcome even while shutting down
	m.st.UpdateDownloadProgress(bg, d.ID, completed, total)
	took := time.Since(now).Round(time.Second)

	switch cause := context.Cause(ctx); {
	case err == nil:
		m.st.FinishDownload(bg, d.ID, store.DownloadCompleted, "")
		m.logf(d.ID, "info", "Download complete (%s in %s)", fmtBytes(total), took)
		m.log.Info("download completed", "model", d.Model, "id", d.ID, "took", took.String())

	case parent.Err() != nil:
		// The app is shutting down: put it back at the front of the queue
		// to resume on the next start rather than calling it a failure.
		m.st.RequeueDownload(bg, d.ID, true)
		m.logf(d.ID, "warn", "Paused: the app is shutting down. It will resume when the app restarts")

	case errors.Is(cause, errCancelled):
		m.st.FinishDownload(bg, d.ID, store.DownloadCancelled, "")
		m.logf(d.ID, "warn", "Cancelled at %s of %s. Downloaded data is kept, so a retry resumes", fmtBytes(completed), fmtBytes(total))
		m.log.Info("download cancelled", "model", d.Model, "id", d.ID)

	default:
		msg := err.Error()
		if errors.Is(cause, errStalled) {
			msg = errStalled.Error()
		}
		m.st.FinishDownload(bg, d.ID, store.DownloadFailed, msg)
		m.logf(d.ID, "error", "Failed: %s", msg)
		m.log.Warn("download failed", "model", d.Model, "id", d.ID, "error", msg)
	}
}

func (m *Manager) logf(id int64, level, format string, args ...any) {
	if err := m.st.AddDownloadLog(context.Background(), id, level, fmt.Sprintf(format, args...)); err != nil {
		m.log.Warn("failed to write download log", "id", id, "error", err)
	}
}

// describeStatus turns a status line into a log message, adding the layer
// size when there is one: "pulling 6a0746a1ec1a (4.1 GB)".
func describeStatus(p ollama.PullProgress) string {
	if p.Total > 0 {
		return fmt.Sprintf("%s (%s)", p.Status, fmtBytes(p.Total))
	}
	return p.Status
}

func fmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func fmtSpeed(bps float64) string {
	if bps <= 0 {
		return "speed unknown"
	}
	return fmtBytes(int64(bps)) + "/s"
}
