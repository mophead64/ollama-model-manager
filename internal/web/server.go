// Package web serves the model browser UI, server-rendered with htmx.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/downloads"
	"github.com/mophead64/ollama-model-manager/internal/library"
	"github.com/mophead64/ollama-model-manager/internal/modeltest"
	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
	"github.com/mophead64/ollama-model-manager/internal/sysinfo"
	"github.com/mophead64/ollama-model-manager/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Config holds the deployment settings the web layer needs.
type Config struct {
	ModelsDir   string   // where Ollama stores models, as seen by this process; "" if unknown
	AllowDelete bool     // whether users may delete models (ALLOW_MODEL_DELETE)
	LibraryURL  string   // the ollama.com model library; "" for the real one
	HFURL       string   // Hugging Face; "" for the real one
	HFToken     string   // optional Hugging Face access token (HF_TOKEN)
	Env         []EnvVar // the environment settings in effect, for the Settings page
}

// EnvVar describes one environment variable the app reads, as it took effect.
type EnvVar struct {
	Name   string
	Value  string // the effective value; for a secret, only whether it's set
	Source string // "set", "default", "auto-detected", or why a set value was ignored
	About  string
	Secret bool
}

type Server struct {
	ol  *ollama.Client
	st  *store.Store
	dl  *downloads.Manager
	mt  *modeltest.Runner
	sys *sysinfo.Sampler
	lib *library.Client
	cfg Config
	log *slog.Logger

	tmpl    *template.Template
	updates *updateChecker
	// Ollama's own updates; kept fresh by RunOllamaUpdateChecks.
	ollamaUp *ollamaUpdates
	logins   *loginLimiter
}

func NewServer(ol *ollama.Client, st *store.Store, dl *downloads.Manager, mt *modeltest.Runner, sys *sysinfo.Sampler, cfg Config, log *slog.Logger) (*Server, error) {
	tmpl, err := template.New("").Funcs(templateFuncs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		ol: ol, st: st, dl: dl, mt: mt, sys: sys, lib: library.New(cfg.LibraryURL, cfg.HFURL, cfg.HFToken), cfg: cfg, log: log,
		tmpl: tmpl, updates: newUpdateChecker(), ollamaUp: newOllamaUpdates(), logins: newLoginLimiter(),
	}, nil
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /static/", staticHandler())

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /account", s.handleAccount)
	mux.HandleFunc("POST /account/username", s.handleChangeUsername)
	mux.HandleFunc("POST /account/password", s.handleChangePassword)
	mux.HandleFunc("GET /account/huggingface", s.handleHuggingFace)

	mux.HandleFunc("GET /{$}", s.handleDashboard)

	mux.HandleFunc("GET /version/check", s.handleVersionCheck)
	mux.HandleFunc("GET /version/release", s.handleRelease)

	mux.HandleFunc("GET /models", s.handleModels)
	mux.HandleFunc("GET /models/blacklist", s.handleBlacklist)
	mux.HandleFunc("POST /models/blacklist/remove", s.handleUnblacklist)
	mux.HandleFunc("POST /models/blacklist/reason", s.handleBlacklistReason)
	mux.HandleFunc("GET /models/testing", s.handleTests)
	mux.HandleFunc("POST /models/testing", s.handleCreateTest)
	mux.HandleFunc("GET /models/testing/{id}", s.handleTestDetail)
	mux.HandleFunc("POST /models/testing/{id}/cancel", s.handleCancelTest)
	mux.HandleFunc("POST /models/testing/{id}/rerun", s.handleRerunTest)
	mux.HandleFunc("POST /models/testing/{id}/delete", s.handleDeleteTest)
	mux.HandleFunc("POST /models/testing/{id}/template", s.handleSaveTestAsTemplate)
	mux.HandleFunc("POST /models/testing/templates", s.handleSaveTemplate)
	mux.HandleFunc("POST /models/testing/templates/{id}/delete", s.handleDeleteTemplate)
	// Model names can contain "/" (e.g. "user/model:tag"), hence the wildcard.
	mux.HandleFunc("GET /models/{name...}", s.handleModelDetail)
	// The name goes in the form body: {name...} has to be the last path segment,
	// so it can't be followed by /delete.
	mux.HandleFunc("POST /models/delete", s.handleDeleteModel)
	mux.HandleFunc("POST /models/load", s.handleLoadModel)
	mux.HandleFunc("POST /models/unload", s.handleUnloadModel)
	mux.HandleFunc("GET /discover", s.handleDiscover)
	mux.HandleFunc("GET /discover/tags", s.handleDiscoverTags)
	mux.HandleFunc("GET /discover/hf/files", s.handleDiscoverHFFiles)
	mux.HandleFunc("GET /discover/quants", s.handleModelQuants)
	mux.HandleFunc("GET /models/chat", s.handleChatPage)
	mux.HandleFunc("GET /chat", func(w http.ResponseWriter, r *http.Request) {
		// The chat page was here before it became a models tab.
		target := &url.URL{Path: "/models/chat", RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /chat/model", s.handleChatModelInfo)
	mux.HandleFunc("GET /chat/picker", s.handleChatPicker)
	mux.HandleFunc("POST /chat", s.handleChat)

	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /system", s.handleSystem)
	mux.HandleFunc("GET /system/history", s.handleSystemHistory)
	mux.HandleFunc("GET /system/load", s.handleSystemLoad)
	mux.HandleFunc("GET /system/running", s.handleRunningModels)
	mux.HandleFunc("GET /system/ollama", s.handleOllamaUpdate)

	mux.HandleFunc("GET /downloads", s.handleDownloads)
	mux.HandleFunc("POST /downloads", s.handleQueueDownload)
	mux.HandleFunc("POST /downloads/clear", s.handleClearDownloads)
	mux.HandleFunc("GET /downloads/{id}", s.handleDownloadDetail)
	mux.HandleFunc("POST /downloads/{id}/cancel", s.handleCancelDownload)
	mux.HandleFunc("POST /downloads/{id}/retry", s.handleRetryDownload)
	mux.HandleFunc("POST /downloads/{id}/delete", s.handleDeleteDownload)

	// Rejects cross-site POSTs (via Sec-Fetch-Site/Origin), so another page
	// can't submit forms here using the session cookie.
	return http.NewCrossOriginProtection().Handler(s.requireAuth(mux))
}

// render executes a template. For map data it also fills in what every page's
// chrome needs: the app version for the footer and the signed-in user for the nav.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any) {
	if m, ok := data.(map[string]any); ok {
		if _, set := m["Version"]; !set {
			m["Version"] = version.Version
		}
		if _, set := m["Flash"]; !set && r.Method == http.MethodGet {
			m["Flash"] = readFlash(r.URL.Query())
		}
		if _, set := m["Nav"]; !set {
			m["Nav"] = navSection(r.URL.Path)
		}
		if _, set := m["AllowDelete"]; !set {
			m["AllowDelete"] = s.cfg.AllowDelete
		}
		if _, set := m["User"]; !set {
			if u := currentUser(r); u != nil {
				m["User"] = u
				// For the count badge on the nav's Downloads link.
				if n, err := s.st.CountActiveDownloads(r.Context()); err == nil {
					m["ActiveDownloads"] = n
				}
				// For the dot on the nav's System link (the System page has its own, fresher).
				if _, set := m["OllamaUpdate"]; !set {
					m["OllamaUpdate"] = s.ollamaUp.latest()
				}
				// And for the Testing tab's badge.
				if n, err := s.st.CountActiveModelTests(r.Context()); err == nil {
					m["ActiveTests"] = n
				}
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// navSection is the nav link to highlight for a page: the first path segment,
// so detail pages (/models/x, /downloads/7) light up their section.
func navSection(path string) string {
	section, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return section
}

type errMsg string

func (e errMsg) Error() string { return string(e) }

func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	w.WriteHeader(http.StatusBadRequest)
	s.render(w, r, "error_fragment.html", map[string]any{"Error": err.Error()})
}
