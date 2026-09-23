// Package store persists app state (users and login sessions) to SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash  TEXT PRIMARY KEY,           -- sha256 of the cookie value; the raw token is never stored
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at  DATETIME NOT NULL,
    expires_at  INTEGER NOT NULL            -- unix seconds, so expiry checks compare numbers
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
`

// Open opens (creating if necessary) the SQLite database at path and applies
// the schema.
//
// The app's write load is tiny, so a single connection serialises everything
// and sidesteps SQLite's "database is locked" between goroutines. busy_timeout
// still matters for the reset-password subcommand, which opens the same file
// from a second process.
func Open(ctx context.Context, path string) (*Store, error) {
	params := []string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=foreign_keys(1)",
		"_txlock=immediate",
	}
	db, err := sql.Open("sqlite", path+"?"+strings.Join(params, "&"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }
