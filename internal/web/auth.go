package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

const sessionCookie = "omm_session"

type ctxKey int

const userKey ctxKey = iota

type sessionInfo struct {
	user  *store.User
	token string
}

func currentUser(r *http.Request) *store.User {
	if si, ok := r.Context().Value(userKey).(sessionInfo); ok {
		return si.user
	}
	return nil
}

func currentToken(r *http.Request) string {
	if si, ok := r.Context().Value(userKey).(sessionInfo); ok {
		return si.token
	}
	return ""
}

// requireAuth lets requests with a valid session through (with the user in the
// context) and sends everything else to the login page. The login page and
// static assets are public.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		var token string
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		u, err := s.st.SessionUser(r.Context(), token)
		if err != nil {
			s.log.Error("session lookup failed", "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if u == nil {
			if token != "" {
				clearSessionCookie(w, r)
			}
			loginURL := "/login"
			if r.Method == http.MethodGet && r.URL.Path != "/" {
				loginURL += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			if r.Header.Get("HX-Request") == "true" {
				// htmx would otherwise swap the login page into a fragment.
				w.Header().Set("HX-Redirect", loginURL)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), userKey, sessionInfo{user: u, token: token})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if u, _ := s.st.SessionUser(r.Context(), c.Value); u != nil {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
	}
	s.render(w, r, "login.html", map[string]any{"Next": r.URL.Query().Get("next")})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username, password := r.FormValue("username"), r.FormValue("password")
	next := r.FormValue("next")
	fail := func(status int, msg string) {
		w.WriteHeader(status)
		s.render(w, r, "login.html", map[string]any{"Next": next, "Username": username, "Error": msg})
	}

	ip := clientIP(r)
	if !s.logins.allow(ip) {
		fail(http.StatusTooManyRequests, "Too many failed attempts. Wait a few minutes and try again.")
		return
	}
	u, err := s.st.Authenticate(r.Context(), username, password)
	if errors.Is(err, store.ErrBadCredentials) {
		s.logins.fail(ip)
		s.log.Warn("failed login", "username", username, "ip", ip)
		fail(http.StatusUnauthorized, "Incorrect username or password.")
		return
	}
	if err != nil {
		s.log.Error("login failed", "error", err)
		fail(http.StatusInternalServerError, "Something went wrong; see the app log.")
		return
	}
	s.logins.reset(ip)

	token, err := s.st.CreateSession(r.Context(), u.ID)
	if err != nil {
		s.log.Error("create session failed", "error", err)
		fail(http.StatusInternalServerError, "Something went wrong; see the app log.")
		return
	}
	setSessionCookie(w, r, token)
	s.log.Info("login", "username", u.Username, "ip", ip)
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteSession(r.Context(), currentToken(r)); err != nil {
		s.log.Error("delete session failed", "error", err)
	}
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleAccount(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"ErrorForm": "", "IsAdmin": s.isAdmin(r), "Env": s.cfg.Env}
	switch r.URL.Query().Get("done") {
	case "username":
		data["Notice"] = "Username updated."
	case "password":
		data["Notice"] = "Password updated. Any other signed-in browsers have been signed out."
	}
	s.render(w, r, "account.html", data)
}

func (s *Server) handleChangeUsername(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	username := r.FormValue("username")
	if _, err := s.st.Authenticate(r.Context(), u.Username, r.FormValue("current_password")); err != nil {
		s.accountError(w, r, "username", "Current password is incorrect.", username)
		return
	}
	if err := s.st.ChangeUsername(r.Context(), u.ID, username); err != nil {
		s.accountError(w, r, "username", capitalise(err.Error())+".", username)
		return
	}
	s.log.Info("username changed", "from", u.Username, "to", strings.TrimSpace(username))
	http.Redirect(w, r, "/account?done=username", http.StatusSeeOther)
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if _, err := s.st.Authenticate(r.Context(), u.Username, r.FormValue("current_password")); err != nil {
		s.accountError(w, r, "password", "Current password is incorrect.", "")
		return
	}
	pw := r.FormValue("new_password")
	if pw != r.FormValue("confirm_password") {
		s.accountError(w, r, "password", "New passwords don't match.", "")
		return
	}
	if err := s.st.ChangePassword(r.Context(), u.ID, pw, currentToken(r)); err != nil {
		s.accountError(w, r, "password", capitalise(err.Error())+".", "")
		return
	}
	s.log.Info("password changed", "username", u.Username)
	http.Redirect(w, r, "/account?done=password", http.StatusSeeOther)
}

// accountError re-renders the account page with an error against one form,
// keeping what the user typed in the username field.
func (s *Server) accountError(w http.ResponseWriter, r *http.Request, form, msg, username string) {
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, r, "account.html", map[string]any{
		"ErrorForm":   form,
		"Error":       msg,
		"NewUsername": username,
	})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(store.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS reports whether the browser is talking HTTPS, directly or via a
// TLS-terminating reverse proxy, so the cookie can be marked Secure.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// safeNext only allows redirecting to a local path, so ?next= can't bounce a
// freshly logged-in user to another site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") {
		return "/"
	}
	return next
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// loginLimiter blocks an IP after too many failed logins in a window. bcrypt
// already makes each guess slow; this stops a patient script from grinding.
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	loginMaxFails = 10
	loginWindow   = 15 * time.Minute
)

func newLoginLimiter() *loginLimiter { return &loginLimiter{fails: map[string][]time.Time{}} }

// recent drops failures older than the window. Caller holds mu.
func (l *loginLimiter) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-loginWindow)
	kept := l.fails[ip][:0]
	for _, t := range l.fails[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, ip)
	} else {
		l.fails[ip] = kept
	}
	return kept
}

func (l *loginLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip)) < loginMaxFails
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[ip] = append(l.recent(ip), time.Now())
}

func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
