package web

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

// The dashboard's usage chart: a stacked bar per day for the last week, of
// how long each model was in use (from package usage).

const (
	weekDays   = 7
	weekSeries = 4 // models with a colour of their own; the rest are "Other"
)

// weekSegment is one model's (or "Other"'s) share of a day.
type weekSegment struct {
	Name    string
	Slot    int // its colour, 1 to weekSeries; 0 for Other
	Seconds int
	Height  float64 // of the day's bar, as a percentage of the busiest day's
}

type weekDay struct {
	Date     time.Time
	Today    bool
	Seconds  int
	Loads    int
	Height   float64       // the bar's, as a percentage of the busiest day's
	Segments []weekSegment // the bar, bottom to top, in the legend's order
	Models   []weekSegment // every model used, busiest first, for the tooltip
}

// weekSeriesInfo is one entry in the legend, with its total for the week.
type weekSeriesInfo struct {
	Name    string
	Slot    int
	Seconds int
	Loads   int
	Days    int // days it was used
	Models  int // for Other, how many models it stands for
}

type weekUsage struct {
	Days    []weekDay
	Series  []weekSeriesInfo // the legend, busiest first, Other last
	Models  []weekSeriesInfo // every model used, busiest first, for the table
	Seconds int
	Loads   int
	Max     int // seconds, on the busiest day: the top of the scale
}

// weekUsage is the last week's use, for the dashboard. A failure only costs
// the chart, so it's logged.
func (s *Server) weekUsage(r *http.Request) *weekUsage {
	today := time.Now()
	rows, err := s.st.DailyUsageSince(r.Context(), today.AddDate(0, 0, -(weekDays-1)).Format(time.DateOnly))
	if err != nil {
		s.log.Warn("read the week's model usage failed", "error", err)
		return nil
	}
	return buildWeekUsage(rows, today)
}

// buildWeekUsage lays out rows (each model's use per day) as the chart:
// weekDays days ending today, oldest first.
func buildWeekUsage(rows []store.ModelDayUsage, today time.Time) *weekUsage {
	w := &weekUsage{}
	start := today.AddDate(0, 0, -(weekDays - 1))
	index := map[string]int{} // YYYY-MM-DD -> day
	for i := range weekDays {
		d := start.AddDate(0, 0, i)
		index[d.Format(time.DateOnly)] = i
		w.Days = append(w.Days, weekDay{Date: d, Today: i == weekDays-1})
	}

	// Each model's week, to rank them: the busiest get colours of their own.
	totals := map[string]*weekSeriesInfo{}
	for _, r := range rows {
		i, ok := index[r.Day]
		if !ok || r.ActiveSeconds+r.Loads == 0 {
			continue
		}
		t := totals[r.Model]
		if t == nil {
			t = &weekSeriesInfo{Name: r.Model}
			totals[r.Model] = t
		}
		t.Seconds += r.ActiveSeconds
		t.Loads += r.Loads
		t.Days++
		d := &w.Days[i]
		d.Seconds += r.ActiveSeconds
		d.Loads += r.Loads
		if r.ActiveSeconds > 0 {
			d.Models = append(d.Models, weekSegment{Name: r.Model, Seconds: r.ActiveSeconds})
		}
	}
	for _, t := range totals {
		w.Models = append(w.Models, *t)
		w.Seconds += t.Seconds
		w.Loads += t.Loads
	}
	if w.Seconds == 0 {
		return w
	}
	slices.SortFunc(w.Models, func(a, b weekSeriesInfo) int {
		return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Name, b.Name))
	})
	slot := map[string]int{}
	var other weekSeriesInfo
	for _, m := range w.Models {
		if m.Seconds == 0 {
			continue
		}
		if len(w.Series) < weekSeries {
			m.Slot = len(w.Series) + 1
			slot[m.Name] = m.Slot
			w.Series = append(w.Series, m)
			continue
		}
		other.Seconds += m.Seconds
		other.Loads += m.Loads
		other.Models++
	}
	if other.Models == 1 {
		// Folding one model into "Other" hides its name for nothing.
		for _, m := range w.Models {
			if m.Seconds > 0 && slot[m.Name] == 0 {
				other.Name = m.Name
			}
		}
	} else if other.Models > 1 {
		other.Name = "Other"
	}
	if other.Name != "" {
		w.Series = append(w.Series, other)
	}

	for i := range w.Days {
		w.Max = max(w.Max, w.Days[i].Seconds)
	}
	for i := range w.Days {
		d := &w.Days[i]
		slices.SortFunc(d.Models, func(a, b weekSegment) int {
			return cmp.Or(cmp.Compare(b.Seconds, a.Seconds), cmp.Compare(a.Name, b.Name))
		})
		byName := map[string]int{}
		for _, m := range d.Models {
			byName[m.Name] = m.Seconds
		}
		for _, s := range w.Series {
			secs := byName[s.Name]
			if s.Slot == 0 && s.Name == "Other" {
				secs = 0
				for _, m := range d.Models {
					if slot[m.Name] == 0 {
						secs += m.Seconds
					}
				}
			}
			if secs > 0 {
				d.Segments = append(d.Segments, weekSegment{Name: s.Name, Slot: s.Slot, Seconds: secs, Height: 100 * float64(secs) / float64(w.Max)})
			}
		}
		for j := range d.Models {
			d.Models[j].Slot = slot[d.Models[j].Name]
		}
		d.Height = 100 * float64(d.Seconds) / float64(w.Max)
	}
	return w
}
