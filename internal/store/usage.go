package store

import (
	"context"
	"database/sql"
	"time"
)

// model_usage keeps one row per model: roughly when it was last used, as the
// usage tracker saw it. Deliberately just a timestamp, not a history.
const usageSchema = `
CREATE TABLE IF NOT EXISTS model_usage (
    model        TEXT PRIMARY KEY COLLATE NOCASE,
    last_used_at DATETIME NOT NULL
);

-- Per model per day (the app's local date, YYYY-MM-DD): roughly how long it
-- was in use, from the tracker's polls, and how many times it was loaded.
CREATE TABLE IF NOT EXISTS model_usage_daily (
    model          TEXT NOT NULL COLLATE NOCASE,
    day            TEXT NOT NULL,
    active_seconds INTEGER NOT NULL DEFAULT 0,
    loads          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (model, day)
);
`

// MarkModelUsed records that model was in use at the given time.
func (s *Store) MarkModelUsed(ctx context.Context, model string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_usage (model, last_used_at) VALUES (?, ?)
		ON CONFLICT(model) DO UPDATE SET last_used_at = excluded.last_used_at`,
		model, at.UTC())
	return err
}

// ModelsLastUsed returns when each model was last seen in use, keyed by the
// model's name as Ollama lists it. Models never seen in use aren't included.
func (s *Store) ModelsLastUsed(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, last_used_at FROM model_usage`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var model string
		var at time.Time
		if err := rows.Scan(&model, &at); err != nil {
			return nil, err
		}
		out[model] = at
	}
	return out, rows.Err()
}

// AddModelUsage adds to model's usage on day (YYYY-MM-DD): seconds it was in
// use, and times it was loaded.
func (s *Store) AddModelUsage(ctx context.Context, model, day string, activeSeconds, loads int) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_usage_daily (model, day, active_seconds, loads) VALUES (?, ?, ?, ?)
		ON CONFLICT(model, day) DO UPDATE SET
			active_seconds = active_seconds + excluded.active_seconds, loads = loads + excluded.loads`,
		model, day, activeSeconds, loads)
	return err
}

// ModelLoadCounts is how many times each model has been seen loaded, in all.
// Models with no usage recorded aren't included.
func (s *Store) ModelLoadCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT model, SUM(loads) FROM model_usage_daily GROUP BY model COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var m string
		var n int
		if err := rows.Scan(&m, &n); err != nil {
			return nil, err
		}
		out[m] = n
	}
	return out, rows.Err()
}

type DailyUsage struct {
	Day           string // YYYY-MM-DD
	ActiveSeconds int
	Loads         int
}

// ModelDailyUsage is model's usage from day from (YYYY-MM-DD) on, by day.
// Days with none aren't included.
func (s *Store) ModelDailyUsage(ctx context.Context, model, from string) ([]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, active_seconds, loads FROM model_usage_daily WHERE model = ? AND day >= ? ORDER BY day`, model, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Day, &d.ActiveSeconds, &d.Loads); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ModelUsageTotals is model's usage in all, and the first day any was
// recorded ("" if none has been).
func (s *Store) ModelUsageTotals(ctx context.Context, model string) (activeSeconds, loads int, since string, err error) {
	var first sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(active_seconds), 0), COALESCE(SUM(loads), 0), MIN(day) FROM model_usage_daily WHERE model = ?`, model).
		Scan(&activeSeconds, &loads, &first)
	return activeSeconds, loads, first.String, err
}
