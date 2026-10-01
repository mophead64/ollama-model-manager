package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// runningModels asks Ollama which models are loaded, for the "in use" panels.
// Errors are reported in the panel rather than failing the page.
func (s *Server) runningModels(r *http.Request) (models []ollama.RunningModel, errText string) {
	models, err := s.ol.Running(r.Context())
	if err != nil {
		s.log.Warn("list running models failed", "error", err)
		return nil, err.Error()
	}
	return models, ""
}

// systemData is what the system page's live section shows. It's rendered both
// as part of the full page and as the fragment htmx polls.
func (s *Server) systemData(r *http.Request) map[string]any {
	snap := s.sys.Latest()
	running, runErr := s.runningModels(r)
	vramUsed, vramTotal, hasVRAM := snap.VRAM()
	return map[string]any{
		"Snap":       snap,
		"Load":       snap.Sample(),
		"VRAMUsed":   vramUsed,
		"VRAMTotal":  vramTotal,
		"HasVRAM":    hasVRAM,
		"Running":    running,
		"RunningErr": runErr,
		"PollEvery":  pollEvery(s.sys.Interval()),
	}
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	data := s.systemData(r)
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, r, "system_live.html", data)
		return
	}
	data["OllamaURL"] = s.ol.BaseURL()
	// The latest release as last checked, against Ollama's version now; the
	// panel re-checks GitHub itself once shown, if that's due.
	data["OllamaUpdate"] = s.withRunning(r.Context())
	// This app's last check, likewise re-checked by its panel once shown.
	data["AppUpdate"] = s.updates.last()
	data["WindowMinutes"] = int(s.sys.Window() / time.Minute)
	if all, err := s.ol.List(r.Context()); err != nil {
		s.log.Warn("list models failed", "error", err)
		data["LibraryErr"] = err.Error()
	} else {
		storage := s.storage(all)
		data["Storage"] = storage
		data["Count"], data["TotalBytes"] = storage.Models, storage.Bytes
		data["Disk"] = s.diskUsage()
	}
	s.render(w, r, "system.html", data)
}

// handleSystemHistory serves the samples behind the live graphs.
func (s *Server) handleSystemHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{
		"interval_ms": s.sys.Interval().Milliseconds(),
		"window_ms":   s.sys.Window().Milliseconds(),
		"samples":     s.sys.History(),
	})
}

// handleSystemLoad is the dashboard's system-load tile, polled by htmx.
func (s *Server) handleSystemLoad(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "load_tile", map[string]any{
		"Load":      s.sys.Latest().Sample(),
		"PollEvery": pollEvery(s.sys.Interval()),
	})
}

// handleRunningModels is the dashboard's and models page's "loaded now"
// panel, polled by htmx. show=empty keeps it visible when nothing's loaded.
func (s *Server) handleRunningModels(w http.ResponseWriter, r *http.Request) {
	running, runErr := s.runningModels(r)
	s.render(w, r, "running_panel", map[string]any{
		"Running": running, "RunningErr": runErr, "Compact": true,
		"ShowEmpty": r.URL.Query().Get("show") == "empty",
	})
}

// pollEvery is an htmx trigger interval matching the sampler, e.g. "2s".
func pollEvery(d time.Duration) string {
	return time.Duration(max(d, time.Second)).Round(time.Second).String()
}
