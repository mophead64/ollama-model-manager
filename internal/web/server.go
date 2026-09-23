// Package web serves the model browser UI, server-rendered with htmx.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

type Server struct {
	ol        *ollama.Client
	modelsDir string // where Ollama stores models, as seen by this process; "" if unknown
	log       *slog.Logger

	tmpl    *template.Template
	updates *updateChecker
}

func NewServer(ol *ollama.Client, modelsDir string, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{ol: ol, modelsDir: modelsDir, log: log, tmpl: tmpl, updates: newUpdateChecker()}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/models", http.StatusFound)
	})

	mux.HandleFunc("GET /version/check", s.handleVersionCheck)

	mux.HandleFunc("GET /models", s.handleModels)
	// Model names can contain "/" (e.g. "user/model:tag"), hence the wildcard.
	mux.HandleFunc("GET /models/{name...}", s.handleModelDetail)

	return mux
}

// render executes a template, injecting the app version every page's footer
// needs when data is a map.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	if m, ok := data.(map[string]any); ok {
		if _, set := m["Version"]; !set {
			m["Version"] = version.Version
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

type errMsg string

func (e errMsg) Error() string { return string(e) }

func (s *Server) badRequest(w http.ResponseWriter, err error) {
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, "error_fragment.html", map[string]any{"Error": err.Error()})
}
