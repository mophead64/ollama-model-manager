package usage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

type fakeOllama struct {
	running []ollama.RunningModel
	err     error
}

func (f *fakeOllama) Running(context.Context) ([]ollama.RunningModel, error) { return f.running, f.err }

type fakeStore struct {
	marks []string
	usage []string // "model day +seconds loads"
}

func (f *fakeStore) MarkModelUsed(_ context.Context, model string, at time.Time) error {
	f.marks = append(f.marks, model+"@"+at.Format("15:04:05"))
	return nil
}

func (f *fakeStore) AddModelUsage(_ context.Context, model, day string, activeSeconds, loads int) error {
	f.usage = append(f.usage, fmt.Sprintf("%s %s +%ds %d", model, day, activeSeconds, loads))
	return nil
}

func TestTracker(t *testing.T) {
	ol, st := &fakeOllama{}, &fakeStore{}
	tr := New(ol, st, 15*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	loaded := func(name string, expires time.Time) ollama.RunningModel {
		return ollama.RunningModel{Name: name, ExpiresAt: expires}
	}

	steps := []struct {
		desc    string
		running []ollama.RunningModel
		err     error
		want    []string // marks made by this poll
	}{
		{"startup: already loaded, but when it was used is unknown",
			[]ollama.RunningModel{loaded("a", at(300))}, nil, nil},
		{"unchanged expiry: idle",
			[]ollama.RunningModel{loaded("a", at(300))}, nil, nil},
		{"expiry pushed back: used",
			[]ollama.RunningModel{loaded("a", at(330))}, nil, []string{"a@10:00:30"}},
		{"newly loaded: used",
			[]ollama.RunningModel{loaded("a", at(330)), loaded("b", at(345))}, nil, []string{"b@10:00:45"}},
		{"unloaded: nothing to record", nil, nil, nil},
		{"loaded again: used",
			[]ollama.RunningModel{loaded("a", at(375))}, nil, []string{"a@10:01:15"}},
		{"Ollama unreachable", nil, errors.New("connection refused"), nil},
		{"back after an outage, idle meanwhile",
			[]ollama.RunningModel{loaded("a", at(375))}, nil, nil},
		{"Ollama unreachable again", nil, errors.New("connection refused"), nil},
		{"back, and it was used meanwhile",
			[]ollama.RunningModel{loaded("a", at(500))}, nil, []string{"a@10:02:15"}},
	}
	for i, s := range steps {
		ol.running, ol.err, st.marks = s.running, s.err, nil
		tr.poll(context.Background(), at(15*i))
		if len(st.marks) != len(s.want) || (len(s.want) > 0 && st.marks[0] != s.want[0]) {
			t.Errorf("step %d (%s): marks = %v, want %v", i, s.desc, st.marks, s.want)
		}
	}

	// Each use adds one poll interval; a load is when it wasn't loaded at the
	// last poll (not at startup, and not after an outage it stayed loaded through).
	day := base.Local().Format(time.DateOnly)
	want := []string{"a " + day + " +15s 0", "b " + day + " +15s 1", "a " + day + " +15s 1", "a " + day + " +15s 0"}
	if fmt.Sprint(st.usage) != fmt.Sprint(want) {
		t.Errorf("usage = %v\nwant %v", st.usage, want)
	}
}
