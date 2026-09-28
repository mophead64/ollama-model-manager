package store

import (
	"context"
	"time"
)

// model_blacklist records models judged not worth keeping, and why. The
// model itself is usually deleted at the same time, so what it was (family,
// size, quantisation...) is copied here rather than looked up from Ollama.
// Blacklisting a model again replaces its entry.
const blacklistSchema = `
CREATE TABLE IF NOT EXISTS model_blacklist (
    model          TEXT PRIMARY KEY COLLATE NOCASE,
    reason         TEXT NOT NULL,
    family         TEXT NOT NULL,
    parameter_size TEXT NOT NULL,
    quantization   TEXT NOT NULL,
    size           INTEGER NOT NULL,
    digest         TEXT NOT NULL,
    blacklisted_by TEXT NOT NULL,
    blacklisted_at DATETIME NOT NULL
);
`

type BlacklistEntry struct {
	Model         string
	Reason        string
	Family        string
	ParameterSize string
	Quantization  string
	Size          int64
	Digest        string
	By            string // username
	At            time.Time
}

// BlacklistModel adds e to the blacklist, replacing any entry for the same model.
func (s *Store) BlacklistModel(ctx context.Context, e BlacklistEntry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO model_blacklist (model, reason, family, parameter_size, quantization, size, digest, blacklisted_by, blacklisted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(model) DO UPDATE SET
			reason = excluded.reason, family = excluded.family, parameter_size = excluded.parameter_size,
			quantization = excluded.quantization, size = excluded.size, digest = excluded.digest,
			blacklisted_by = excluded.blacklisted_by, blacklisted_at = excluded.blacklisted_at`,
		e.Model, e.Reason, e.Family, e.ParameterSize, e.Quantization, e.Size, e.Digest, e.By, e.At.UTC())
	return err
}

// Blacklist returns every blacklisted model, most recently blacklisted first.
func (s *Store) Blacklist(ctx context.Context) ([]BlacklistEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, reason, family, parameter_size, quantization, size, digest, blacklisted_by, blacklisted_at
		FROM model_blacklist ORDER BY blacklisted_at DESC, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlacklistEntry
	for rows.Next() {
		var e BlacklistEntry
		if err := rows.Scan(&e.Model, &e.Reason, &e.Family, &e.ParameterSize, &e.Quantization, &e.Size, &e.Digest, &e.By, &e.At); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpdateBlacklistReason changes the reason model is blacklisted for. ok is
// false if it isn't on the blacklist.
func (s *Store) UpdateBlacklistReason(ctx context.Context, model, reason string) (ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `UPDATE model_blacklist SET reason = ? WHERE model = ?`, reason, model)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UnblacklistModel removes model's entry, if it has one.
func (s *Store) UnblacklistModel(ctx context.Context, model string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_blacklist WHERE model = ?`, model)
	return err
}
