// Command ollama-model-manager runs the Ollama Model Manager web application
// against the Ollama server at OLLAMA_HOST (default http://localhost:11434).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/version"
	"github.com/mophead64/ollama-model-manager/internal/web"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))

	addr := ":" + getenv("PORT", "8080")
	ol := ollama.New(getenv("OLLAMA_HOST", "http://localhost:11434"))
	modelsDir := findModelsDir()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info("starting", "version", version.Version, "ollama", ol.BaseURL())

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

	srv, err := web.NewServer(ol, modelsDir, log)
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
