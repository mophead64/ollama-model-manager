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
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
	"github.com/mophead64/ollama-model-manager/internal/usage"
	"github.com/mophead64/ollama-model-manager/internal/version"
	"github.com/mophead64/ollama-model-manager/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	dbPath := getenv("DB_PATH", defaultDBPath())
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
		log.Error("failed to open database", "path", dbPath, "error", err,
			"hint", "if that folder isn't writable, set DB_PATH to a file path that is")
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
	// An optional Hugging Face token lets the app see private repos when
	// checking and browsing them. Ollama's own access (for the pulls
	// themselves) is separate: see the Account page.
	hfToken := strings.TrimSpace(os.Getenv("HF_TOKEN"))
	if hfToken != "" {
		dl.SetRegistryClient(&http.Client{Timeout: 10 * time.Second, Transport: ollama.HFTokenTransport{Token: hfToken}})
		log.Info("using a Hugging Face token", "var", "HF_TOKEN")
	}
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

	// CPU/memory/GPU load for the System page and the models page's load
	// tile, sampled in the background so the graphs have history.
	sys := sysinfo.New(2*time.Second, 5*time.Minute, log)
	go sys.Run(ctx)

	// Notes when each model was last used, from what Ollama has loaded.
	go usage.New(ol, st, 15*time.Second, log).Run(ctx)

	env := describeEnv(strings.TrimPrefix(addr, ":"), ol.BaseURL(), dbPath, modelsDir, allowDelete, hfToken != "")
	srv, err := web.NewServer(ol, st, dl, sys, web.Config{ModelsDir: modelsDir, AllowDelete: allowDelete, HFToken: hfToken, Env: env}, log)
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

// defaultDBPath is where the database goes when DB_PATH isn't set: next to
// the executable (symlinks followed), so a downloaded binary keeps its data
// beside it wherever it's run from. The container image sets DB_PATH to its
// /data volume instead.
func defaultDBPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "omm.db" // the working directory
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	return filepath.Join(filepath.Dir(exe), "omm.db")
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

// describeEnv lists the environment settings as they took effect, for the
// Settings page. HF_TOKEN's value is never included, only whether it's set.
func describeEnv(port, ollamaURL, dbPath, modelsDir string, allowDelete, hasHFToken bool) []web.EnvVar {
	modelsSource := envSource("MODELS_DIR")
	if modelsSource != "set" {
		modelsSource = "auto-detected"
		if modelsDir == "" {
			modelsSource = "not found"
		}
	}
	deleteSource := envSource("ALLOW_MODEL_DELETE")
	if _, ok := parseBool(os.Getenv("ALLOW_MODEL_DELETE")); deleteSource == "set" && !ok {
		deleteSource = "invalid, using default"
	}
	hfToken := ""
	if hasHFToken {
		hfToken = "set"
	}
	return []web.EnvVar{
		{Name: "PORT", Value: port, Source: envSource("PORT"), About: "Port the web UI listens on"},
		{Name: "OLLAMA_HOST", Value: ollamaURL, Source: envSource("OLLAMA_HOST"), About: "The Ollama server being managed"},
		{Name: "DB_PATH", Value: dbPath, Source: envSource("DB_PATH"), About: "This app's database: accounts and download history"},
		{Name: "MODELS_DIR", Value: modelsDir, Source: modelsSource, About: "Ollama's models folder, for the free disk space tile"},
		{Name: "OLLAMA_MODELS", Value: os.Getenv("OLLAMA_MODELS"), Source: optionalSource("OLLAMA_MODELS"), About: "Ollama's own models folder setting; checked when finding MODELS_DIR"},
		{Name: "ALLOW_MODEL_DELETE", Value: fmt.Sprint(allowDelete), Source: deleteSource, About: "Whether models can be deleted from this app"},
		{Name: "HF_TOKEN", Value: hfToken, Source: optionalSource("HF_TOKEN"), About: "Hugging Face read token, for browsing private repos", Secret: true},
	}
}

// envSource says whether a variable with a default was set or defaulted.
func envSource(key string) string {
	if os.Getenv(key) != "" {
		return "set"
	}
	return "default"
}

// optionalSource is envSource for a variable with no default.
func optionalSource(key string) string {
	if os.Getenv(key) != "" {
		return "set"
	}
	return "not set"
}

// getenvBool reads a true/false env var (1/0, true/false, yes/no...),
// warning and using fallback if it's set to something else.
func getenvBool(log *slog.Logger, key string, fallback bool) bool {
	v := os.Getenv(key)
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	if b, ok := parseBool(v); ok {
		return b
	}
	log.Warn("ignoring invalid boolean", "var", key, "value", v, "using", fallback)
	return fallback
}

// parseBool reads 1/0, true/false, yes/no or on/off, reporting whether v was one.
func parseBool(v string) (b, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
