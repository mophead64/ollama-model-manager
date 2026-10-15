package web

import (
	"strings"
	"testing"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

func TestBuildWeekUsage(t *testing.T) {
	today := time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local)
	day := func(ago int) string { return today.AddDate(0, 0, -ago).Format(time.DateOnly) }
	use := func(model string, ago, secs, loads int) store.ModelDayUsage {
		return store.ModelDayUsage{Model: model, DailyUsage: store.DailyUsage{Day: day(ago), ActiveSeconds: secs, Loads: loads}}
	}
	w := buildWeekUsage([]store.ModelDayUsage{
		use("a", 0, 3600, 2), use("a", 1, 1800, 1),
		use("b", 0, 1200, 1),
		use("c", 2, 900, 1),
		use("d", 3, 600, 1),
		use("e", 0, 300, 1), use("f", 0, 120, 1), // the two quietest: Other
		use("old", 9, 99999, 9), // before the week
	}, today)

	if len(w.Days) != 7 || !w.Days[6].Today || w.Days[6].Date.Format(time.DateOnly) != day(0) || w.Days[0].Date.Format(time.DateOnly) != day(6) {
		t.Fatalf("days: %+v", w.Days)
	}
	var legend []string
	for _, s := range w.Series {
		legend = append(legend, s.Name)
	}
	if strings.Join(legend, ",") != "a,b,c,d,Other" || w.Series[0].Slot != 1 || w.Series[3].Slot != 4 || w.Series[4].Slot != 0 || w.Series[4].Models != 2 {
		t.Errorf("legend: %+v", w.Series)
	}
	if w.Seconds != 3600+1800+1200+900+600+300+120 || w.Loads != 8 || w.Max != 3600+1200+300+120 {
		t.Errorf("totals: %d s, %d loads, max %d", w.Seconds, w.Loads, w.Max)
	}
	todayBar := w.Days[6]
	if todayBar.Height != 100 || len(todayBar.Segments) != 3 || todayBar.Segments[0].Name != "a" || todayBar.Segments[2].Name != "Other" || todayBar.Segments[2].Seconds != 420 {
		t.Errorf("today's bar: %+v", todayBar.Segments)
	}
	if len(todayBar.Models) != 4 || todayBar.Models[0].Name != "a" || todayBar.Models[3].Name != "f" || todayBar.Models[3].Slot != 0 {
		t.Errorf("today's tooltip: %+v", todayBar.Models)
	}
	if len(w.Models) != 6 || w.Models[0].Days != 2 {
		t.Errorf("table: %+v", w.Models)
	}

	// One model past the fourth keeps its own name rather than "Other".
	w = buildWeekUsage([]store.ModelDayUsage{use("a", 0, 500, 1), use("b", 0, 400, 1), use("c", 0, 300, 1), use("d", 0, 200, 1), use("e", 0, 100, 1)}, today)
	if last := w.Series[len(w.Series)-1]; last.Name != "e" || last.Slot != 0 {
		t.Errorf("a lone fifth model: %+v", last)
	}

	if w := buildWeekUsage(nil, today); w.Seconds != 0 || len(w.Series) != 0 || len(w.Days) != 7 {
		t.Errorf("a quiet week: %+v", w)
	}
}
