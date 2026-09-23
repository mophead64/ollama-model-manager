package store

import (
	"context"
	"time"
)

// model_usage keeps one row per model: roughly when it was last used, as the
// usage tracker saw it. Deliberately just a timestamp, not a history.
const usageSchema = `
CREATE TABLE IF NOT EXISTS model_usage (
    model        TEXT PRIMARY KEY COLLATE NOCASE,
    last_used_at DATETIME NOT NULL
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
