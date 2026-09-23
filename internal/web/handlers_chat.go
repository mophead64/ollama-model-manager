package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// The Chat page, for trying models out (static/chat.js). The conversation
// lives in the browser and is sent whole each turn; nothing is stored.

const (
	chatMaxBody     = 1 << 20 // a long conversation, pasted text and all
	chatMaxMessages = 200
)

// handleChatPage shows the chat, with a picker of every model that can chat.
// ?model= preselects one (the Chat buttons elsewhere link here with it).
func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{}
	all, err := s.ol.List(r.Context())
	if err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
		s.render(w, r, "chat.html", data)
		return
	}
	loaded := map[string]bool{}
	if running, err := s.ol.Running(r.Context()); err == nil {
		for _, m := range running {
			loaded[m.Name] = true
		}
	}
	var models []ollama.Model
	for _, m := range all {
		if canChat(m.Capabilities) {
			models = append(models, m)
		}
	}
	slices.SortFunc(models, func(a, b ollama.Model) int { return strings.Compare(a.Name, b.Name) })
	data["Models"] = models
	data["LoadedNames"] = loaded

	if want := r.URL.Query().Get("model"); want != "" {
		if i := slices.IndexFunc(models, func(m ollama.Model) bool { return m.Name == want }); i >= 0 {
			data["Selected"] = models[i]
			data["Info"] = s.chatModelInfo(r, models[i])
		}
	}
	s.render(w, r, "chat.html", data)
}

// handleChatModelInfo is the picked model's properties panel, fetched when
// the picker changes and kept live as the model's state changes.
func (s *Server) handleChatModelInfo(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	all, err := s.ol.List(r.Context())
	if err != nil {
		s.render(w, r, "chat_model_info", map[string]any{"Error": err.Error()})
		return
	}
	i := slices.IndexFunc(all, func(m ollama.Model) bool { return m.Name == name })
	if i < 0 {
		s.render(w, r, "chat_model_info", map[string]any{"Error": fmt.Sprintf("%s isn't on this Ollama server any more.", name)})
		return
	}
	s.render(w, r, "chat_model_info", s.chatModelInfo(r, all[i]))
}

// chatModelInfo gathers what the properties panel shows about a model.
func (s *Server) chatModelInfo(r *http.Request, m ollama.Model) map[string]any {
	data := map[string]any{"Model": m, "Name": m.Name}
	s.addMemoryState(r, m.Name, data)
	return data
}

// chatEvent is one line of the stream sent to the browser: a piece of the
// reply, then either the stats or an error.
type chatEvent struct {
	Content  string     `json:"content,omitempty"`
	Thinking string     `json:"thinking,omitempty"`
	Done     bool       `json:"done,omitempty"`
	Stats    *chatStats `json:"stats,omitempty"`
	Error    string     `json:"error,omitempty"`
}

type chatStats struct {
	Tokens       int     `json:"tokens"`         // generated
	TokensPerSec float64 `json:"tokens_per_sec"` // generation speed
	PromptTokens int     `json:"prompt_tokens"`
	LoadMS       int64   `json:"load_ms"`  // loading the model, if it wasn't already
	TotalMS      int64   `json:"total_ms"` // the whole request
}

// handleChat streams a model's reply as newline-delimited JSON. Closing the
// page or pressing Stop cancels the request, which stops Ollama generating.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string               `json:"model"`
		Messages []ollama.ChatMessage `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, chatMaxBody)).Decode(&req); err != nil {
		http.Error(w, "bad chat request", http.StatusBadRequest)
		return
	}
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" || len(req.Messages) == 0 || len(req.Messages) > chatMaxMessages {
		http.Error(w, "bad chat request", http.StatusBadRequest)
		return
	}
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			http.Error(w, "bad chat request", http.StatusBadRequest)
			return
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // don't let a reverse proxy hold the stream back
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	send := func(ev chatEvent) {
		enc.Encode(ev)
		rc.Flush()
	}

	err := s.ol.Chat(r.Context(), req.Model, req.Messages, func(ch ollama.ChatChunk) {
		if ch.Message.Content != "" || ch.Message.Thinking != "" {
			send(chatEvent{Content: ch.Message.Content, Thinking: ch.Message.Thinking})
		}
		if ch.Done {
			send(chatEvent{Done: true, Stats: chatStatsFrom(ch)})
		}
	})
	switch {
	case err == nil:
	case r.Context().Err() != nil:
		// The browser stopped it; nobody's listening for an error.
	default:
		s.log.Warn("chat failed", "model", req.Model, "error", err)
		msg := err.Error()
		var se *ollama.StatusError
		if errors.As(err, &se) {
			msg = se.Message
		}
		send(chatEvent{Error: msg})
	}
}

// canChat reports whether a model with these capabilities can hold a chat:
// embedding-only models can't. Older Ollama versions don't report
// capabilities; those are given the benefit of the doubt.
func canChat(caps []string) bool {
	return len(caps) == 0 || slices.Contains(caps, "completion")
}

func chatStatsFrom(ch ollama.ChatChunk) *chatStats {
	st := &chatStats{
		Tokens:       ch.EvalCount,
		PromptTokens: ch.PromptEvalCount,
		LoadMS:       time.Duration(ch.LoadDuration).Milliseconds(),
		TotalMS:      time.Duration(ch.TotalDuration).Milliseconds(),
	}
	if ch.EvalDuration > 0 {
		st.TokensPerSec = float64(ch.EvalCount) / time.Duration(ch.EvalDuration).Seconds()
	}
	return st
}
