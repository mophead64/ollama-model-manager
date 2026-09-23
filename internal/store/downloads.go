package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Download statuses. queued -> downloading -> completed | failed | cancelled;
// failed/cancelled can be retried (back to queued).
const (
	DownloadQueued      = "queued"
	DownloadDownloading = "downloading"
	DownloadCompleted   = "completed"
	DownloadFailed      = "failed"
	DownloadCancelled   = "cancelled"
)

const downloadsSchema = `
CREATE TABLE IF NOT EXISTS downloads (
    id              INTEGER PRIMARY KEY,
    model           TEXT NOT NULL,
    status          TEXT NOT NULL,
    requested_by    TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL,
    queued_at       DATETIME NOT NULL,   -- queue order; reset on retry
    started_at      DATETIME,
    finished_at     DATETIME,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    completed_bytes INTEGER NOT NULL DEFAULT 0,
    attempts        INTEGER NOT NULL DEFAULT 0,
    error           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS downloads_status ON downloads(status, queued_at);

CREATE TABLE IF NOT EXISTS download_logs (
    id          INTEGER PRIMARY KEY,
    download_id INTEGER NOT NULL REFERENCES downloads(id) ON DELETE CASCADE,
    at          DATETIME NOT NULL,
    level       TEXT NOT NULL DEFAULT 'info',   -- info | warn | error
    message     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS download_logs_download ON download_logs(download_id, id);
`

type Download struct {
	ID             int64
	Model          string
	Status         string
	RequestedBy    string
	CreatedAt      time.Time
	QueuedAt       time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	TotalBytes     int64
	CompletedBytes int64
	Attempts       int
	Error          string
}

// Active reports whether the download is still queued or running.
func (d Download) Active() bool {
	return d.Status == DownloadQueued || d.Status == DownloadDownloading
}

// Retryable reports whether the download ended without succeeding.
func (d Download) Retryable() bool {
	return d.Status == DownloadFailed || d.Status == DownloadCancelled
}

type DownloadLog struct {
	At      time.Time
	Level   string
	Message string
}

var ErrAlreadyQueued = errors.New("that model is already queued or downloading")

const downloadCols = `id, model, status, requested_by, created_at, queued_at, started_at, finished_at,
	total_bytes, completed_bytes, attempts, error`

func scanDownload(row interface{ Scan(...any) error }) (*Download, error) {
	var d Download
	var started, finished sql.NullTime
	if err := row.Scan(&d.ID, &d.Model, &d.Status, &d.RequestedBy, &d.CreatedAt, &d.QueuedAt,
		&started, &finished, &d.TotalBytes, &d.CompletedBytes, &d.Attempts, &d.Error); err != nil {
		return nil, err
	}
	if started.Valid {
		d.StartedAt = &started.Time
	}
	if finished.Valid {
		d.FinishedAt = &finished.Time
	}
	return &d, nil
}

func (s *Store) queryDownloads(ctx context.Context, q string, args ...any) ([]Download, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Download
	for rows.Next() {
		d, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// EnqueueDownload adds a model to the back of the queue. It refuses a model
// that's already queued or downloading (compared case-insensitively, as
// Ollama treats names).
func (s *Store) EnqueueDownload(ctx context.Context, model, requestedBy string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM downloads WHERE model = ? COLLATE NOCASE AND status IN (?, ?)`,
		model, DownloadQueued, DownloadDownloading).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrAlreadyQueued
	}
	now := time.Now().UTC()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO downloads (model, status, requested_by, created_at, queued_at) VALUES (?, ?, ?, ?, ?)`,
		model, DownloadQueued, requestedBy, now, now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, tx.Commit()
}

func (s *Store) GetDownload(ctx context.Context, id int64) (*Download, error) {
	d, err := scanDownload(s.db.QueryRowContext(ctx, `SELECT `+downloadCols+` FROM downloads WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// NextQueuedDownload returns the download at the front of the queue, or nil.
func (s *Store) NextQueuedDownload(ctx context.Context) (*Download, error) {
	d, err := scanDownload(s.db.QueryRowContext(ctx,
		`SELECT `+downloadCols+` FROM downloads WHERE status = ? ORDER BY queued_at, id LIMIT 1`, DownloadQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// ActiveDownloads returns the running download (if any) then the queue, in order.
func (s *Store) ActiveDownloads(ctx context.Context) ([]Download, error) {
	return s.queryDownloads(ctx, `SELECT `+downloadCols+` FROM downloads WHERE status IN (?, ?)
		ORDER BY status = ? DESC, queued_at, id`, DownloadQueued, DownloadDownloading, DownloadDownloading)
}

func (s *Store) CountActiveDownloads(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE status IN (?, ?)`,
		DownloadQueued, DownloadDownloading).Scan(&n)
	return n, err
}

// FinishedDownloads pages through completed/failed/cancelled downloads, newest first.
func (s *Store) FinishedDownloads(ctx context.Context, page, pageSize int) ([]Download, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM downloads WHERE status NOT IN (?, ?)`,
		DownloadQueued, DownloadDownloading).Scan(&total); err != nil {
		return nil, 0, err
	}
	ds, err := s.queryDownloads(ctx, `SELECT `+downloadCols+` FROM downloads WHERE status NOT IN (?, ?)
		ORDER BY finished_at DESC, id DESC LIMIT ? OFFSET ?`,
		DownloadQueued, DownloadDownloading, pageSize, (page-1)*pageSize)
	return ds, total, err
}

func (s *Store) StartDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status = ?, started_at = ?, finished_at = NULL, error = '', attempts = attempts + 1 WHERE id = ?`,
		DownloadDownloading, time.Now().UTC(), id)
	return err
}

func (s *Store) UpdateDownloadProgress(ctx context.Context, id, completed, total int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET completed_bytes = ?, total_bytes = ? WHERE id = ?`,
		completed, total, id)
	return err
}

// FinishDownload records the outcome of a download attempt.
func (s *Store) FinishDownload(ctx context.Context, id int64, status, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE downloads SET status = ?, error = ?, finished_at = ? WHERE id = ?`,
		status, errMsg, time.Now().UTC(), id)
	return err
}

// SetDownloadModel changes which model a (finished) download fetches, for a
// retry under a corrected name. Progress from the old model is reset.
func (s *Store) SetDownloadModel(ctx context.Context, id int64, model string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET model = ?, completed_bytes = 0, total_bytes = 0 WHERE id = ? AND status NOT IN (?, ?)`,
		model, id, DownloadQueued, DownloadDownloading)
	return err
}

// RequeueDownload puts a download back in the queue: at the back for a retry,
// or at its original place (keepPosition) when it was interrupted by a restart.
func (s *Store) RequeueDownload(ctx context.Context, id int64, keepPosition bool) error {
	if keepPosition {
		_, err := s.db.ExecContext(ctx,
			`UPDATE downloads SET status = ?, finished_at = NULL, error = '' WHERE id = ?`, DownloadQueued, id)
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE downloads SET status = ?, finished_at = NULL, error = '', queued_at = ? WHERE id = ?`,
		DownloadQueued, time.Now().UTC(), id)
	return err
}

// RequeueInterrupted returns downloads left "downloading" by an unclean
// shutdown to the front of the queue. Ollama keeps partially downloaded
// blobs, so the next attempt resumes rather than starting over.
func (s *Store) RequeueInterrupted(ctx context.Context) ([]Download, error) {
	ds, err := s.queryDownloads(ctx, `SELECT `+downloadCols+` FROM downloads WHERE status = ?`, DownloadDownloading)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		if err := s.RequeueDownload(ctx, d.ID, true); err != nil {
			return nil, err
		}
	}
	return ds, nil
}

// DeleteDownload removes a finished download and its log. Active downloads
// have to be cancelled first.
func (s *Store) DeleteDownload(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE id = ? AND status NOT IN (?, ?)`,
		id, DownloadQueued, DownloadDownloading)
	return err
}

// ClearFinishedDownloads removes every finished download and its log.
func (s *Store) ClearFinishedDownloads(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM downloads WHERE status NOT IN (?, ?)`,
		DownloadQueued, DownloadDownloading)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) AddDownloadLog(ctx context.Context, id int64, level, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO download_logs (download_id, at, level, message) VALUES (?, ?, ?, ?)`,
		id, time.Now().UTC(), level, message)
	return err
}

func (s *Store) DownloadLogs(ctx context.Context, id int64) ([]DownloadLog, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT at, level, message FROM download_logs WHERE download_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DownloadLog
	for rows.Next() {
		var l DownloadLog
		if err := rows.Scan(&l.At, &l.Level, &l.Message); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
