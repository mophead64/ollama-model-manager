package downloads

import (
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// tracker turns Ollama's per-layer progress lines into whole-model totals
// and a smoothed download speed.
//
// A model is several layers (weights, template, license...). Ollama reports
// each by digest with its own total/completed, and may fetch them in parallel,
// so the overall figure is the sum across every layer seen so far.
type tracker struct {
	layers   map[string][2]int64 // digest -> {completed, total}
	statuses map[string]bool     // status lines already logged
	samples  []sample
}

type sample struct {
	at    time.Time
	bytes int64
}

func newTracker() *tracker {
	return &tracker{layers: map[string][2]int64{}, statuses: map[string]bool{}}
}

// update records a progress line and reports whether its status is one not
// seen before (and so worth a log line).
func (t *tracker) update(p ollama.PullProgress, now time.Time) (newStatus bool) {
	if p.Digest != "" && p.Total > 0 {
		t.layers[p.Digest] = [2]int64{p.Completed, p.Total}
	}
	completed, _ := t.bytes()
	t.samples = append(t.samples, sample{now, completed})
	cutoff := now.Add(-speedWindow)
	i := 0
	for i < len(t.samples)-2 && t.samples[i+1].at.Before(cutoff) {
		i++
	}
	t.samples = t.samples[i:]

	if p.Status == "" || t.statuses[p.Status] {
		return false
	}
	t.statuses[p.Status] = true
	return true
}

func (t *tracker) bytes() (completed, total int64) {
	for _, l := range t.layers {
		completed += l[0]
		total += l[1]
	}
	return completed, total
}

// speed is bytes/sec across the sample window, or 0 until there's at least a
// second of data to go on. Bytes resumed from an earlier attempt show up in
// the first sample, so they don't inflate the speed.
func (t *tracker) speed() float64 {
	if len(t.samples) < 2 {
		return 0
	}
	first, last := t.samples[0], t.samples[len(t.samples)-1]
	dt := last.at.Sub(first.at).Seconds()
	if dt < 1 || last.bytes < first.bytes {
		return 0
	}
	return float64(last.bytes-first.bytes) / dt
}
