package web

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// healthTimeout bounds each of the health check's probes, so a monitor gets
// an answer even when Ollama hangs.
const healthTimeout = 3 * time.Second

// handleHealth reports whether the app can do its job: its database answers
// and Ollama is reachable. 200 when both are, 503 otherwise, with which one
// failed. It's served without a session, so it says no more than that; the
// details go to the log.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	check := func(name string, probe func(context.Context) error) string {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		if err := probe(ctx); err != nil {
			s.log.Warn("health check failed", "check", name, "error", err)
			return "unreachable"
		}
		return "ok"
	}
	out := map[string]string{
		"database": check("database", s.st.Ping),
		"ollama": check("ollama", func(ctx context.Context) error {
			_, err := s.ol.Version(ctx)
			return err
		}),
	}
	status := http.StatusOK
	out["status"] = "ok"
	if out["database"] != "ok" || out["ollama"] != "ok" {
		status = http.StatusServiceUnavailable
		out["status"] = "unhealthy"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(out)
}
