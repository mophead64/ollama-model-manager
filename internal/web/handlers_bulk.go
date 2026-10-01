package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// Acting on several models at once, from the Models page's selection
// (static/models.js): unloading them, or deleting them, optionally
// blacklisting them as they go.

// bulkMax caps how many models one request acts on.
const bulkMax = 500

// bulkNames is the request's model names (name=...), trimmed, without
// duplicates.
func bulkNames(r *http.Request) ([]string, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	var out []string
	for _, n := range r.Form["name"] {
		if n = strings.TrimSpace(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	switch {
	case len(out) == 0:
		return nil, errMsg("select at least one model")
	case len(out) > bulkMax:
		return nil, errMsg(fmt.Sprintf("select at most %d models at once", bulkMax))
	}
	return out, nil
}

// countOf is "3 models", or the model's name when there's just the one.
func countOf(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%d models", len(names))
}

// handleBulkConfirm is the bulk delete dialog's content: the selected models
// Ollama still has, and what deleting them frees.
func (s *Server) handleBulkConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowDelete {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "Deleting models is disabled on this server (ALLOW_MODEL_DELETE=false)."})
		return
	}
	names, err := bulkNames(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	all, err := s.ol.List(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "Couldn't list models from Ollama: " + err.Error()})
		return
	}
	var models []ollama.Model
	var missing, found []string
	for _, n := range names {
		if i := slices.IndexFunc(all, func(m ollama.Model) bool { return m.Name == n }); i >= 0 {
			models = append(models, all[i])
			found = append(found, n)
		} else {
			missing = append(missing, n)
		}
	}
	s.render(w, r, "bulk_delete_body", map[string]any{
		"Models":  models,
		"Missing": missing,
		"Effect":  s.storage(all).DeleteEffect(found...),
		"Return":  r.FormValue("return"),
	})
}

// handleBulkDelete deletes the selected models, blacklisting each first if
// asked (blacklist=on, with the one reason for them all), then goes back to
// the list with what was done and what couldn't be.
func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowDelete {
		w.WriteHeader(http.StatusForbidden)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "Deleting models is disabled on this server (ALLOW_MODEL_DELETE=false)."})
		return
	}
	names, err := bulkNames(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	blacklist := r.FormValue("blacklist") == "on"
	reason, err := blacklistReason(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	var all []ollama.Model
	if blacklist {
		if all, err = s.ol.List(r.Context()); err != nil {
			s.log.Warn("list models for blacklist entries failed", "error", err)
		}
	}

	// Carried on if the browser gives up waiting: a long list can take a while.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	var deleted, failed []string
	var firstErr error
	for _, name := range names {
		var entry store.BlacklistEntry
		if blacklist {
			entry = s.blacklistEntry(r, all, name, reason)
		}
		if err := s.deleteModel(ctx, name); err != nil {
			s.log.Error("delete model failed", "model", name, "error", err)
			failed = append(failed, name)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.log.Info("model deleted", "model", name, "by", currentUser(r).Username)
		deleted = append(deleted, name)
		if blacklist {
			if err := s.st.BlacklistModel(ctx, entry); err != nil {
				s.log.Error("blacklist model failed", "model", name, "error", err)
				failed = append(failed, name+" (deleted, but not blacklisted)")
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			s.log.Info("model blacklisted", "model", name, "by", entry.By)
		}
	}

	var problem string
	if len(failed) > 0 {
		problem = fmt.Sprintf("Couldn't delete %s: %s", strings.Join(failed, ", "), describeOllamaErr(firstErr))
	}
	var done string
	if len(deleted) > 0 {
		done = countOf(deleted)
	}
	s.afterDelete(w, r, done, blacklist && len(deleted) > 0, problem)
}

// handleBulkUnload unloads whichever of the selected models are loaded.
func (s *Server) handleBulkUnload(w http.ResponseWriter, r *http.Request) {
	names, err := bulkNames(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	running, err := s.ol.Running(r.Context())
	if err != nil {
		s.backTo(w, r, "memerror", "Couldn't ask Ollama which models are loaded: "+describeOllamaErr(err))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	var unloaded, failed []string
	var firstErr error
	for _, name := range names {
		// Only the loaded ones: asking Ollama to unload one that isn't would
		// load it first.
		if !slices.ContainsFunc(running, func(m ollama.RunningModel) bool { return m.Name == name }) {
			continue
		}
		if err := s.ol.Unload(ctx, name); err != nil {
			s.log.Error("unload model failed", "model", name, "error", err)
			failed = append(failed, name)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		s.log.Info("model unloaded", "model", name, "by", currentUser(r).Username)
		unloaded = append(unloaded, name)
	}
	switch {
	case len(failed) > 0:
		msg := fmt.Sprintf("Couldn't unload %s: %s", strings.Join(failed, ", "), describeOllamaErr(firstErr))
		if len(unloaded) > 0 {
			msg = fmt.Sprintf("Unloaded %s, but couldn't unload %s: %s", countOf(unloaded), strings.Join(failed, ", "), describeOllamaErr(firstErr))
		}
		s.backTo(w, r, "memerror", msg)
	case len(unloaded) == 0:
		s.backTo(w, r, "memerror", "None of the selected models were loaded.")
	default:
		s.backTo(w, r, "unloaded", countOf(unloaded))
	}
}
