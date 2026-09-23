package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestPragmasApplied(t *testing.T) {
	st := openTest(t)
	var mode string
	var fk int
	st.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	st.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Errorf("journal_mode=%q foreign_keys=%d, want wal/1", mode, fk)
	}
}

func TestEnsureAdmin(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	created, pw, err := st.EnsureAdmin(ctx)
	if err != nil || !created || len(pw) != 24 {
		t.Fatalf("first EnsureAdmin = %v, %q, %v", created, pw, err)
	}
	if _, err := st.Authenticate(ctx, "admin", pw); err != nil {
		t.Fatalf("generated password should work: %v", err)
	}
	if _, err := st.Authenticate(ctx, "ADMIN", pw); err != nil {
		t.Errorf("usernames should be case-insensitive: %v", err)
	}

	created, pw, err = st.EnsureAdmin(ctx)
	if err != nil || created || pw != "" {
		t.Errorf("second EnsureAdmin should do nothing, got %v, %q, %v", created, pw, err)
	}
}

func TestAuthenticateFailures(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	st.CreateUser(ctx, "josh", "correct horse")
	for _, c := range [][2]string{{"josh", "wrong password"}, {"nobody", "correct horse"}} {
		if _, err := st.Authenticate(ctx, c[0], c[1]); !errors.Is(err, ErrBadCredentials) {
			t.Errorf("Authenticate(%q, %q) err = %v, want ErrBadCredentials", c[0], c[1], err)
		}
	}
}

func TestSessionsAndPasswordChange(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	u, _ := st.CreateUser(ctx, "admin", "first-password")

	a, _ := st.CreateSession(ctx, u.ID)
	b, _ := st.CreateSession(ctx, u.ID)
	if got, _ := st.SessionUser(ctx, a); got == nil || got.ID != u.ID {
		t.Fatalf("session a should resolve to the user, got %+v", got)
	}
	if got, _ := st.SessionUser(ctx, "bogus"); got != nil {
		t.Error("unknown token should resolve to nil")
	}

	if err := st.ChangePassword(ctx, u.ID, "short", a); err == nil {
		t.Error("short password should be rejected")
	}
	if err := st.ChangePassword(ctx, u.ID, "second-password", a); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SessionUser(ctx, a); got == nil {
		t.Error("the session that changed the password should survive")
	}
	if got, _ := st.SessionUser(ctx, b); got != nil {
		t.Error("other sessions should be signed out by a password change")
	}
	if _, err := st.Authenticate(ctx, "admin", "first-password"); err == nil {
		t.Error("old password should no longer work")
	}

	st.DeleteSession(ctx, a)
	if got, _ := st.SessionUser(ctx, a); got != nil {
		t.Error("deleted session should be gone")
	}
}

func TestChangeUsername(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	a, _ := st.CreateUser(ctx, "admin", "password-a")
	st.CreateUser(ctx, "other", "password-b")

	if err := st.ChangeUsername(ctx, a.ID, "  josh  "); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Authenticate(ctx, "josh", "password-a"); err != nil {
		t.Errorf("renamed user should log in with the new name: %v", err)
	}
	if err := st.ChangeUsername(ctx, a.ID, "Other"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("taken username err = %v", err)
	}
	if err := st.ChangeUsername(ctx, a.ID, "   "); err == nil {
		t.Error("blank username should be rejected")
	}
}

func TestResetFirstUser(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	if _, _, err := st.ResetFirstUser(ctx); err == nil {
		t.Error("reset with no users should fail")
	}
	u, _ := st.CreateUser(ctx, "admin", "forgotten-pw")
	tok, _ := st.CreateSession(ctx, u.ID)

	name, pw, err := st.ResetFirstUser(ctx)
	if err != nil || name != "admin" {
		t.Fatalf("ResetFirstUser = %q, %v", name, err)
	}
	if _, err := st.Authenticate(ctx, "admin", pw); err != nil {
		t.Errorf("reset password should work: %v", err)
	}
	if got, _ := st.SessionUser(ctx, tok); got != nil {
		t.Error("reset should sign out every session")
	}
}

func TestIsAdmin(t *testing.T) {
	st := openTest(t)
	ctx := context.Background()
	first, _ := st.CreateUser(ctx, "first", "password-1")
	second, _ := st.CreateUser(ctx, "second", "password-2")
	if ok, err := st.IsAdmin(ctx, first.ID); !ok || err != nil {
		t.Errorf("first user: %v, %v", ok, err)
	}
	if ok, _ := st.IsAdmin(ctx, second.ID); ok {
		t.Error("second user is admin")
	}
}
