package web

import (
	"net/http"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// dashboardRecent is how many recently used models the dashboard lists, most
// recent first; the full list is on the models page.
const dashboardRecent = 10

// handleDashboard is the landing page: disk and load at a glance, what's in
// memory, and the models used most recently.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Nav":       "dashboard",
		"OllamaURL": s.ol.BaseURL(),
		"Deleted":   r.URL.Query().Get("deleted"),
		"Load":      s.sys.Latest().Sample(),
		"PollEvery": pollEvery(s.sys.Interval()),
	}

	all, err := s.ol.List(r.Context())
	if err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
	} else {
		running := s.addOverview(r, all, data)
		loaded := make(map[string]bool, len(running))
		for _, m := range running {
			loaded[m.Name] = true
		}
		data["LoadedNames"] = loaded
		data["ShowEmpty"] = true // "no models loaded" is worth saying here
		lastUsed := s.lastUsed(r)
		sortModels(all, "used", true, lastUsed)
		var recent []ollama.Model
		for _, m := range all[:min(dashboardRecent, len(all))] {
			if _, ok := lastUsed[m.Name]; ok { // unused models sort last
				recent = append(recent, m)
			}
		}
		data["Recent"] = recent
		data["LastUsed"] = lastUsed
	}

	s.render(w, r, "dashboard.html", data)
}

// addOverview adds what the "overview" template shows, shared by the dashboard
// and the models page: library totals, disk space and the models loaded in
// memory (also returned).
func (s *Server) addOverview(r *http.Request, all []ollama.Model, data map[string]any) []ollama.RunningModel {
	var totalBytes int64
	for _, m := range all {
		totalBytes += m.Size
	}
	data["Count"] = len(all)
	data["TotalBytes"] = totalBytes
	data["Disk"] = s.diskUsage()
	running, runErr := s.runningModels(r)
	data["Running"], data["RunningErr"] = running, runErr
	data["Compact"] = true
	return running
}
