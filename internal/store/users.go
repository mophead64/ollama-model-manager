package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID        int64
	Username  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

var (
	ErrBadCredentials = errors.New("incorrect username or password")
	ErrUsernameTaken  = errors.New("that username is already taken")
)

const (
	MinPasswordLen = 8
	// bcrypt ignores everything past 72 bytes, so longer passwords would be
	// silently truncated; reject them instead.
	maxPasswordBytes = 72
	maxUsernameLen   = 64
	SessionTTL       = 30 * 24 * time.Hour
)

// ValidateUsername trims and checks a username, returning the cleaned value.
func ValidateUsername(u string) (string, error) {
	u = strings.TrimSpace(u)
	switch {
	case u == "":
		return "", errors.New("username can't be empty")
	case utf8.RuneCountInString(u) > maxUsernameLen:
		return "", fmt.Errorf("username can be at most %d characters", maxUsernameLen)
	case strings.ContainsFunc(u, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", errors.New("username can't contain control characters")
	}
	return u, nil
}

func ValidatePassword(p string) error {
	switch {
	case utf8.RuneCountInString(p) < MinPasswordLen:
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	case len(p) > maxPasswordBytes:
		return fmt.Errorf("password can be at most %d bytes", maxPasswordBytes)
	}
	return nil
}

// RandomPassword returns a 24-character URL-safe random password (144 bits).
func RandomPassword() string {
	b := make([]byte, 18)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashPassword(p string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	return string(h), err
}

// dummyHash is compared against when a username doesn't exist, so a login
// attempt takes the same time whether or not the user is real.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(ctx context.Context, username, password string) (*User, error) {
	username, err := ValidateUsername(username)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		username, hash, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username, CreatedAt: now, UpdatedAt: now}, nil
}

// EnsureAdmin creates the initial "admin" user with a random password if there
// are no users yet. created reports whether it did; password is only set then.
func (s *Store) EnsureAdmin(ctx context.Context) (created bool, password string, err error) {
	n, err := s.CountUsers(ctx)
	if err != nil || n > 0 {
		return false, "", err
	}
	password = RandomPassword()
	if _, err := s.CreateUser(ctx, "admin", password); err != nil {
		return false, "", err
	}
	return true, password, nil
}

// IsAdmin reports whether a user administers the app: may change settings
// that affect everyone, such as Ollama's Hugging Face access. There are no
// roles yet, so it's the first account, the one EnsureAdmin creates; the app
// has no way to add others.
func (s *Store) IsAdmin(ctx context.Context, userID int64) (bool, error) {
	var first int64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(id) FROM users`).Scan(&first); err != nil {
		return false, err
	}
	return userID == first, nil
}

// Authenticate checks a username/password pair.
func (s *Store) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var (
		u    User
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at, updated_at FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &hash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return nil, ErrBadCredentials
	}
	return &u, nil
}

func (s *Store) ChangeUsername(ctx context.Context, userID int64, username string) error {
	username, err := ValidateUsername(username)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET username = ?, updated_at = ? WHERE id = ?`,
		username, time.Now().UTC(), userID)
	if isUniqueViolation(err) {
		return ErrUsernameTaken
	}
	return err
}

// ChangePassword sets a new password and signs the user out everywhere except
// keepToken (the session making the change; "" to sign out everywhere).
func (s *Store) ChangePassword(ctx context.Context, userID int64, password, keepToken string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, time.Now().UTC(), userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`,
		userID, tokenHash(keepToken)); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetFirstUser gives the longest-standing user a new random password and
// ends all their sessions: the recovery path for a forgotten password.
func (s *Store) ResetFirstUser(ctx context.Context) (username, password string, err error) {
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id, username FROM users ORDER BY id LIMIT 1`).Scan(&id, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("no users exist yet; start the app once to create the admin user")
	}
	if err != nil {
		return "", "", err
	}
	password = RandomPassword()
	return username, password, s.ChangePassword(ctx, id, password, "")
}

// CreateSession starts a login session and returns the token for the cookie.
func (s *Store) CreateSession(ctx context.Context, userID int64) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	token := base64.RawURLEncoding.EncodeToString(b)
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash(token), userID, now, now.Add(SessionTTL).Unix())
	return token, err
}

// SessionUser returns the user a live session token belongs to, or nil.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	if token == "" {
		return nil, nil
	}
	var u User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.created_at, u.updated_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`,
		tokenHash(token), time.Now().Unix()).Scan(&u.ID, &u.Username, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash(token))
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
