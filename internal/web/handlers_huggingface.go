package web

import (
	"errors"
	"net/http"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// isAdmin reports whether the signed-in user may change settings that affect
// everyone (see store.IsAdmin).
func (s *Server) isAdmin(r *http.Request) bool {
	u := currentUser(r)
	if u == nil {
		return false
	}
	ok, err := s.st.IsAdmin(r.Context(), u.ID)
	if err != nil {
		s.log.Error("admin check failed", "error", err)
	}
	return ok
}

// handleHuggingFace is the Account page's Hugging Face section, loaded after
// the page since it asks Ollama and Hugging Face: Ollama's public key, for
// linking Ollama to a Hugging Face account, and the state of HF_TOKEN.
func (s *Server) handleHuggingFace(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		http.Error(w, "only the admin can manage Hugging Face access", http.StatusForbidden)
		return
	}
	data := map[string]any{"OllamaURL": s.ol.BaseURL(), "HasToken": s.lib.HasHFToken()}

	key, err := s.ol.PublicKey(r.Context())
	switch {
	case err == nil:
		data["Key"] = key
	case errors.Is(err, ollama.ErrKeyUnavailable):
		data["KeyUnavailable"] = true
	default:
		data["KeyErr"] = err.Error()
	}

	if s.lib.HasHFToken() {
		if name, err := s.lib.HFWhoAmI(r.Context()); err != nil {
			data["TokenErr"] = err.Error()
		} else {
			data["TokenUser"] = name
		}
	}
	s.render(w, r, "hf_connect", data)
}
