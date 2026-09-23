// Command ollama-model-manager runs the Ollama Model Manager web application
// against the Ollama server at OLLAMA_HOST (default http://localhost:11434),
// keeping its own state in a SQLite database at DB_PATH (default /data/omm.db).
//
// "ollama-model-manager reset-password" gives the admin user a new random
// password, for when it's been forgotten.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
	"github.com/mophead64/ollama-model-manager/internal/version"
	"github.com/mophead64/ollama-model-manager/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dbPath := getenv("DB_PATH", "/data/omm.db")
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-password":
			os.Exit(resetPassword(dbPath))
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (the only command is reset-password)\n", os.Args[1])
			os.Exit(2)
		}
	}

	addr := ":" + getenv("PORT", "8080")
	ol := ollama.New(getenv("OLLAMA_HOST", "http://localhost:11434"))
	modelsDir := findModelsDir()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "version", version.Version, "ollama", ol.BaseURL(), "db", dbPath)

	st, err := openStore(ctx, dbPath)
	if err != nil {
		log.Error("failed to open database", "path", dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()

	if created, pw, err := st.EnsureAdmin(ctx); err != nil {
		log.Error("failed to create admin user", "error", err)
		os.Exit(1)
	} else if created {
		printCredentials("Initial admin account created", "admin", pw)
	}
	if err := st.DeleteExpiredSessions(ctx); err != nil {
		log.Warn("failed to prune expired sessions", "error", err)
	}

	// Ollama being down isn't fatal: it may still be starting alongside us, and
	// the UI reports the error on each page until it's reachable.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if v, err := ol.Version(pingCtx); err != nil {
		log.Warn("ollama not reachable yet", "url", ol.BaseURL(), "error", err)
	} else {
		log.Info("connected to ollama", "url", ol.BaseURL(), "ollama_version", v)
	}
	cancel()

	if modelsDir == "" {
		log.Warn("models directory not found; free disk space won't be shown (set MODELS_DIR or mount it at /models)")
	} else {
		log.Info("reporting disk space for models directory", "dir", modelsDir)
	}

	allowDelete := getenvBool(log, "ALLOW_MODEL_DELETE", true)
	if !allowDelete {
		log.Info("model deletion disabled", "var", "ALLOW_MODEL_DELETE")
	}

	// The download queue runs in the background for the life of the process,
	// independent of any browser session.
	dl := downloads.New(st, ol, log)
	dlDone := make(chan struct{})
	go func() { dl.Run(ctx); close(dlDone) }()
	defer func() {
		// Let the worker record where an in-flight download got to (it's
		// requeued to resume on the next start) before the database closes.
		select {
		case <-dlDone:
		case <-time.After(5 * time.Second):
			log.Warn("download worker didn't stop in time")
		}
	}()

	srv, err := web.NewServer(ol, st, dl, web.Config{ModelsDir: modelsDir, AllowDelete: allowDelete}, log)
	if err != nil {
		log.Error("failed to initialize web server", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown failed", "error", err)
		}
	}()

	log.Info("ollama-model-manager listening", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}

func openStore(ctx context.Context, dbPath string) (*store.Store, error) {
	if dir := filepath.Dir(dbPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db directory: %w", err)
		}
	}
	return store.Open(ctx, dbPath)
}

// resetPassword implements the reset-password command, run inside the
// container (docker exec <container> /ollama-model-manager reset-password)
// while the app is running or not.
func resetPassword(dbPath string) int {
	ctx := context.Background()
	st, err := openStore(ctx, dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database %s: %v\n", dbPath, err)
		return 1
	}
	defer st.Close()
	username, pw, err := st.ResetFirstUser(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset password: %v\n", err)
		return 1
	}
	printCredentials("Password reset; all sessions signed out", username, pw)
	return 0
}

// printCredentials writes a banner to stdout (not the structured log) so the
// password is easy to spot in docker logs and copy cleanly.
func printCredentials(title, username, password string) {
	fmt.Printf(`
==============================================================
  %s
    username: %s
    password: %s
  Change these from the Account page after logging in.
  This password won't be shown again.
==============================================================

`, title, username, password)
}

// findModelsDir locates Ollama's model store for the free-space dashlet:
// MODELS_DIR if set, else the container mount point /models, else Ollama's
// default locations for a native run. Returns "" if none exist.
func findModelsDir() string {
	if v := os.Getenv("MODELS_DIR"); v != "" {
		return v
	}
	candidates := []string{"/models"}
	if v := os.Getenv("OLLAMA_MODELS"); v != "" {
		candidates = append(candidates, v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".ollama", "models"))
	}
	candidates = append(candidates, "/usr/share/ollama/.ollama/models") // Linux service install
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && fi.IsDir() {
			return c
		}
	}
	return ""
}

// getenvBool reads a true/false env var (1/0, true/false, yes/no...),
// warning and using fallback if it's set to something else.
func getenvBool(log *slog.Logger, key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	log.Warn("ignoring invalid boolean", "var", key, "value", v, "using", fallback)
	return fallback
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
