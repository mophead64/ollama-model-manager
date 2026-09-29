package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Model test statuses: queued -> running -> completed | failed | cancelled.
// Each test is a set of runs (one per model per repetition), which go
// pending -> running -> completed | failed | cancelled.
const (
	TestQueued    = "queued"
	TestRunning   = "running"
	TestCompleted = "completed"
	TestFailed    = "failed"
	TestCancelled = "cancelled"

	RunPending = "pending"
)

// A model test sends one prompt to one or more models, a number of times
// each, to compare their replies and speed. model_test_runs holds each
// attempt and its result; position orders the models as they were picked.
const modelTestsSchema = `
CREATE TABLE IF NOT EXISTS model_tests (
    id          INTEGER PRIMARY KEY,
    name        TEXT NOT NULL,
    prompt      TEXT NOT NULL,
    repeats     INTEGER NOT NULL,          -- runs per model
    status      TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    started_at  DATETIME,
    finished_at DATETIME
);
CREATE INDEX IF NOT EXISTS model_tests_status ON model_tests(status, id);

CREATE TABLE IF NOT EXISTS model_test_runs (
    id            INTEGER PRIMARY KEY,
    test_id       INTEGER NOT NULL REFERENCES model_tests(id) ON DELETE CASCADE,
    model         TEXT NOT NULL,
    position      INTEGER NOT NULL,
    iteration     INTEGER NOT NULL,        -- 1..repeats
    status        TEXT NOT NULL,
    response      TEXT NOT NULL DEFAULT '',
    thinking      TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    tokens        INTEGER NOT NULL DEFAULT 0,
    load_ms       INTEGER NOT NULL DEFAULT 0,
    prompt_ms     INTEGER NOT NULL DEFAULT 0,
    eval_ms       INTEGER NOT NULL DEFAULT 0,
    total_ms      INTEGER NOT NULL DEFAULT 0,
    started_at    DATETIME,
    finished_at   DATETIME
);
CREATE INDEX IF NOT EXISTS model_test_runs_test ON model_test_runs(test_id, position, iteration);
CREATE INDEX IF NOT EXISTS model_test_runs_model ON model_test_runs(model, status);

-- Saved tests, for starting the same comparison again. models is one name per line.
CREATE TABLE IF NOT EXISTS model_test_templates (
    id         INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    prompt     TEXT NOT NULL,
    models     TEXT NOT NULL,
    repeats    INTEGER NOT NULL,
    created_by TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
`

type ModelTest struct {
	ID         int64
	Name       string
	Prompt     string
	Repeats    int
	Status     string
	CreatedBy  string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time

	Models []string // in the order picked
	Done   int      // runs finished, whatever the outcome
	Total  int      // runs in all
}

// Active reports whether the test is still to run or running.
func (t ModelTest) Active() bool { return t.Status == TestQueued || t.Status == TestRunning }

// Percent is how far through its runs the test is, 0-100.
func (t ModelTest) Percent() float64 {
	if t.Total == 0 {
		return 0
	}
	return 100 * float64(t.Done) / float64(t.Total)
}

type ModelTestRun struct {
	ID           int64
	TestID       int64
	Model        string
	Position     int
	Iteration    int
	Status       string
	Response     string
	Thinking     string
	Error        string
	PromptTokens int
	Tokens       int
	LoadMS       int64
	PromptMS     int64
	EvalMS       int64
	TotalMS      int64
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// TokensPerSec is the generation speed, or 0 if unknown.
func (r ModelTestRun) TokensPerSec() float64 {
	if r.EvalMS <= 0 {
		return 0
	}
	return float64(r.Tokens) / (float64(r.EvalMS) / 1000)
}

// CreateModelTest queues a test of prompt against models, repeats times
// each, and returns its ID.
func (s *Store) CreateModelTest(ctx context.Context, name, prompt string, models []string, repeats int, by string) (int64, error) {
	if len(models) == 0 || repeats < 1 {
		return 0, errors.New("a test needs at least one model and one run")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO model_tests (name, prompt, repeats, status, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, prompt, repeats, TestQueued, by, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	for pos, m := range models {
		for i := 1; i <= repeats; i++ {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO model_test_runs (test_id, model, position, iteration, status) VALUES (?, ?, ?, ?, ?)`,
				id, m, pos, i, RunPending); err != nil {
				return 0, err
			}
		}
	}
	return id, tx.Commit()
}

const modelTestCols = `t.id, t.name, t.prompt, t.repeats, t.status, t.created_by, t.created_at, t.started_at, t.finished_at,
	(SELECT COUNT(*) FROM model_test_runs r WHERE r.test_id = t.id AND r.status NOT IN ('pending', 'running')),
	(SELECT COUNT(*) FROM model_test_runs r WHERE r.test_id = t.id)`

func (s *Store) queryModelTests(ctx context.Context, q string, args ...any) ([]ModelTest, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelTest
	for rows.Next() {
		var t ModelTest
		var started, finished sql.NullTime
		if err := rows.Scan(&t.ID, &t.Name, &t.Prompt, &t.Repeats, &t.Status, &t.CreatedBy, &t.CreatedAt,
			&started, &finished, &t.Done, &t.Total); err != nil {
			return nil, err
		}
		t.StartedAt, t.FinishedAt = timePtr(started), timePtr(finished)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if out[i].Models, err = s.modelTestModels(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) modelTestModels(ctx context.Context, id int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model FROM model_test_runs WHERE test_id = ? GROUP BY position, model ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ModelTests returns every test, newest first.
func (s *Store) ModelTests(ctx context.Context) ([]ModelTest, error) {
	return s.queryModelTests(ctx, `SELECT `+modelTestCols+` FROM model_tests t ORDER BY t.id DESC`)
}

// GetModelTest returns a test, or nil if there's no such test.
func (s *Store) GetModelTest(ctx context.Context, id int64) (*ModelTest, error) {
	ts, err := s.queryModelTests(ctx, `SELECT `+modelTestCols+` FROM model_tests t WHERE t.id = ?`, id)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// NextQueuedModelTest returns the oldest queued test, or nil.
func (s *Store) NextQueuedModelTest(ctx context.Context) (*ModelTest, error) {
	ts, err := s.queryModelTests(ctx, `SELECT `+modelTestCols+` FROM model_tests t WHERE t.status = ? ORDER BY t.id LIMIT 1`, TestQueued)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// ModelTestRuns returns a test's runs, by model then iteration.
func (s *Store) ModelTestRuns(ctx context.Context, testID int64) ([]ModelTestRun, error) {
	return s.queryRuns(ctx, `SELECT `+runCols+` FROM model_test_runs WHERE test_id = ? ORDER BY position, iteration`, testID)
}

// NextPendingRun returns a test's next run to do, or nil when there are none.
func (s *Store) NextPendingRun(ctx context.Context, testID int64) (*ModelTestRun, error) {
	rs, err := s.queryRuns(ctx, `SELECT `+runCols+` FROM model_test_runs WHERE test_id = ? AND status = ? ORDER BY position, iteration LIMIT 1`,
		testID, RunPending)
	if err != nil || len(rs) == 0 {
		return nil, err
	}
	return &rs[0], nil
}

const runCols = `id, test_id, model, position, iteration, status, response, thinking, error,
	prompt_tokens, tokens, load_ms, prompt_ms, eval_ms, total_ms, started_at, finished_at`

func (s *Store) queryRuns(ctx context.Context, q string, args ...any) ([]ModelTestRun, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelTestRun
	for rows.Next() {
		var r ModelTestRun
		var started, finished sql.NullTime
		if err := rows.Scan(&r.ID, &r.TestID, &r.Model, &r.Position, &r.Iteration, &r.Status, &r.Response, &r.Thinking, &r.Error,
			&r.PromptTokens, &r.Tokens, &r.LoadMS, &r.PromptMS, &r.EvalMS, &r.TotalMS, &started, &finished); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = timePtr(started), timePtr(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) StartModelTest(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE model_tests SET status = ?, started_at = COALESCE(started_at, ?) WHERE id = ?`,
		TestRunning, time.Now().UTC(), id)
	return err
}

// FinishModelTest records how a test ended. Any runs it didn't get to are
// marked with the same status (a cancelled test's leftovers are cancelled).
func (s *Store) FinishModelTest(ctx context.Context, id int64, status string) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx,
		`UPDATE model_test_runs SET status = ?, finished_at = ? WHERE test_id = ? AND status IN (?, ?)`,
		TestCancelled, now, id, RunPending, TestRunning); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE model_tests SET status = ?, finished_at = ? WHERE id = ?`, status, now, id)
	return err
}

func (s *Store) StartRun(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE model_test_runs SET status = ?, started_at = ? WHERE id = ?`,
		TestRunning, time.Now().UTC(), id)
	return err
}

// FinishRun records a run's outcome (its Status) and results.
func (s *Store) FinishRun(ctx context.Context, r ModelTestRun) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE model_test_runs SET status = ?, response = ?, thinking = ?, error = ?, prompt_tokens = ?, tokens = ?,
			load_ms = ?, prompt_ms = ?, eval_ms = ?, total_ms = ?, finished_at = ?
		WHERE id = ?`,
		r.Status, r.Response, r.Thinking, r.Error, r.PromptTokens, r.Tokens, r.LoadMS, r.PromptMS, r.EvalMS, r.TotalMS,
		time.Now().UTC(), r.ID)
	return err
}

// CancelQueuedModelTest cancels a test that hasn't started. ok is false if
// it isn't queued (it's running, finished or gone).
func (s *Store) CancelQueuedModelTest(ctx context.Context, id int64) (ok bool, err error) {
	t, err := s.GetModelTest(ctx, id)
	if err != nil || t == nil || t.Status != TestQueued {
		return false, err
	}
	return true, s.FinishModelTest(ctx, id, TestCancelled)
}

// RequeueInterruptedModelTests puts tests left running by an app restart
// back in the queue, at their place. The run that was going is done again.
func (s *Store) RequeueInterruptedModelTests(ctx context.Context) (int64, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE model_test_runs SET status = ?, started_at = NULL WHERE status = ?`, RunPending, TestRunning); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE model_tests SET status = ? WHERE status = ?`, TestQueued, TestRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteModelTest removes a test and its results. Active tests have to be
// cancelled first; ok is false if it's active or gone.
func (s *Store) DeleteModelTest(ctx context.Context, id int64) (ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM model_tests WHERE id = ? AND status NOT IN (?, ?)`, id, TestQueued, TestRunning)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountActiveModelTests is how many tests are queued or running.
func (s *Store) CountActiveModelTests(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_tests WHERE status IN (?, ?)`, TestQueued, TestRunning).Scan(&n)
	return n, err
}

// ModelRunTimes is each model's average time per completed test run, in
// milliseconds, for estimating how long a new test will take. Models never
// tested aren't included.
func (s *Store) ModelRunTimes(ctx context.Context) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT model, AVG(total_ms) FROM model_test_runs WHERE status = ? AND total_ms > 0 GROUP BY model`, TestCompleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var m string
		var ms float64
		if err := rows.Scan(&m, &ms); err != nil {
			return nil, err
		}
		out[m] = ms
	}
	return out, rows.Err()
}

type ModelTestTemplate struct {
	ID        int64
	Name      string
	Prompt    string
	Models    []string
	Repeats   int
	CreatedBy string
	CreatedAt time.Time
}

// SaveModelTestTemplate stores a template and returns its ID.
func (s *Store) SaveModelTestTemplate(ctx context.Context, t ModelTestTemplate) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO model_test_templates (name, prompt, models, repeats, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.Name, t.Prompt, strings.Join(t.Models, "\n"), t.Repeats, t.CreatedBy, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ModelTestTemplates returns every template, by name.
func (s *Store) ModelTestTemplates(ctx context.Context) ([]ModelTestTemplate, error) {
	return s.queryTemplates(ctx, `SELECT id, name, prompt, models, repeats, created_by, created_at FROM model_test_templates ORDER BY name COLLATE NOCASE, id`)
}

// GetModelTestTemplate returns a template, or nil if there's no such one.
func (s *Store) GetModelTestTemplate(ctx context.Context, id int64) (*ModelTestTemplate, error) {
	ts, err := s.queryTemplates(ctx, `SELECT id, name, prompt, models, repeats, created_by, created_at FROM model_test_templates WHERE id = ?`, id)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

func (s *Store) queryTemplates(ctx context.Context, q string, args ...any) ([]ModelTestTemplate, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelTestTemplate
	for rows.Next() {
		var t ModelTestTemplate
		var models string
		if err := rows.Scan(&t.ID, &t.Name, &t.Prompt, &models, &t.Repeats, &t.CreatedBy, &t.CreatedAt); err != nil {
			return nil, err
		}
		if models != "" {
			t.Models = strings.Split(models, "\n")
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteModelTestTemplate removes a template; tests made from it are kept.
func (s *Store) DeleteModelTestTemplate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_test_templates WHERE id = ?`, id)
	return err
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
