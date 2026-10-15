package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/store"
)

// Labels: colour-coded tags for models (package store's labels). Made on the
// Labels tab, or while labelling a model; shown on the dashboard, the models
// list and each model's page.

// modelLabels is each labelled model's labels (keyed by lower-cased name).
// A failure only costs the labels, so it's logged.
func (s *Server) modelLabels(r *http.Request) map[string][]store.Label {
	m, err := s.st.ModelLabels(r.Context())
	if err != nil {
		s.log.Warn("read model labels failed", "error", err)
	}
	return m
}

// allLabels is every label, for pickers and filters. A failure is logged.
func (s *Server) allLabels(r *http.Request) []store.Label {
	labels, err := s.st.Labels(r.Context())
	if err != nil {
		s.log.Warn("read labels failed", "error", err)
	}
	return labels
}

// addModelLabelData adds a model's labels, and what its Edit labels dialog
// needs, to its page's data.
func (s *Server) addModelLabelData(r *http.Request, name string, data map[string]any) {
	mine := labelsOf(s.modelLabels(r), name)
	ids := make([]int64, len(mine))
	for i, l := range mine {
		ids[i] = l.ID
	}
	data["ModelLabels"], data["ModelLabelIDs"] = mine, ids
	data["AllLabels"], data["LabelColors"], data["LabelNameMax"] = s.allLabels(r), store.LabelColors, store.LabelNameMax
}

// labelsOf is a model's labels, from modelLabels' map (template func "labelsof").
func labelsOf(labels map[string][]store.Label, name string) []store.Label {
	return labels[strings.ToLower(name)]
}

// handleLabels is the models section's Labels tab: every label, the built-in
// ones and the user's own, with how many models have each, and a form to
// make one.
func (s *Server) handleLabels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.renderLabels(w, r, map[string]any{"Created": q.Get("created"), "Updated": q.Get("updated"), "Removed": q.Get("removed")}, http.StatusOK)
}

func (s *Server) renderLabels(w http.ResponseWriter, r *http.Request, data map[string]any, status int) {
	s.addModelsOverview(r, data)
	labels, err := s.st.Labels(r.Context())
	if err != nil {
		s.log.Error("read labels failed", "error", err)
		data["LabelsErr"] = err.Error()
	}
	data["Labels"] = labels
	data["Colors"] = store.LabelColors
	data["NameMax"] = store.LabelNameMax
	w.WriteHeader(status)
	s.render(w, r, "labels.html", data)
}

// labelError re-shows the Labels tab with an error against a form: "new",
// or a label's ID for its Edit dialog.
func (s *Server) labelError(w http.ResponseWriter, r *http.Request, form string, err error) {
	status := http.StatusBadRequest
	if !errors.Is(err, store.ErrLabelExists) && !errors.Is(err, store.ErrLabelInvalid) && !errors.Is(err, store.ErrLabelBuiltin) &&
		!strings.HasPrefix(err.Error(), "a label's name") {
		s.log.Error("save label failed", "error", err)
		status = http.StatusInternalServerError
	}
	s.renderLabels(w, r, map[string]any{"ErrorForm": form, "LabelError": capitalise(err.Error()) + ".",
		"FormName": r.FormValue("name"), "FormColor": r.FormValue("color")}, status)
}

func (s *Server) handleCreateLabel(w http.ResponseWriter, r *http.Request) {
	if _, err := s.st.CreateLabel(r.Context(), r.FormValue("name"), r.FormValue("color")); err != nil {
		s.labelError(w, r, "new", err)
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	s.log.Info("label created", "label", name, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/labels?created="+url.QueryEscape(name), http.StatusSeeOther)
}

// labelFromPath is the label named by the {id} in the path, or nil after a
// 400 or 404 has been sent.
func (s *Server) labelFromPath(w http.ResponseWriter, r *http.Request) *store.Label {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad label id"))
		return nil
	}
	l, err := s.st.GetLabel(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	if l == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "There's no such label; it may have been deleted."})
		return nil
	}
	return l
}

func (s *Server) handleUpdateLabel(w http.ResponseWriter, r *http.Request) {
	l := s.labelFromPath(w, r)
	if l == nil {
		return
	}
	if _, err := s.st.UpdateLabel(r.Context(), l.ID, r.FormValue("name"), r.FormValue("color")); err != nil {
		s.labelError(w, r, strconv.FormatInt(l.ID, 10), err)
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	s.log.Info("label updated", "label", l.Name, "now", name, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/labels?updated="+url.QueryEscape(name), http.StatusSeeOther)
}

func (s *Server) handleDeleteLabel(w http.ResponseWriter, r *http.Request) {
	l := s.labelFromPath(w, r)
	if l == nil {
		return
	}
	if err := s.st.DeleteLabel(r.Context(), l.ID); err != nil {
		s.labelError(w, r, "", err)
		return
	}
	s.log.Info("label deleted", "label", l.Name, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/labels?removed="+url.QueryEscape(l.Name), http.StatusSeeOther)
}

// handleSetModelLabels gives a model the labels ticked in its Edit labels
// dialog, plus a new one if one was named there (made then, to reuse).
func (s *Server) handleSetModelLabels(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("model"))
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, r, err)
		return
	}
	var ids []int64
	for _, v := range r.Form["label"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	if newName := strings.TrimSpace(r.FormValue("new_name")); newName != "" {
		id, err := s.st.CreateLabel(r.Context(), newName, r.FormValue("new_color"))
		if errors.Is(err, store.ErrLabelExists) {
			id, err = s.labelNamed(r, newName) // there already: use it
		}
		if err != nil {
			s.backTo(w, r, "memerror", "Couldn't make the label: "+err.Error()+".")
			return
		}
		s.log.Info("label created", "label", newName, "by", currentUser(r).Username)
		ids = append(ids, id)
	}
	if err := s.st.SetModelLabels(r.Context(), name, ids); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("model labels set", "model", name, "labels", len(ids), "by", currentUser(r).Username)
	s.backTo(w, r, "labelled", "Updated "+name+"'s labels.")
}

// labelNamed is the ID of the label with this name, whatever its case.
func (s *Server) labelNamed(r *http.Request, name string) (int64, error) {
	labels, err := s.st.Labels(r.Context())
	if err != nil {
		return 0, err
	}
	name = strings.Join(strings.Fields(name), " ")
	for _, l := range labels {
		if strings.EqualFold(l.Name, name) {
			return l.ID, nil
		}
	}
	return 0, store.ErrLabelInvalid
}

// handleBulkLabel adds a label to the selected models, or takes it off them
// (op=remove), then goes back to the list.
func (s *Server) handleBulkLabel(w http.ResponseWriter, r *http.Request) {
	names, err := bulkNames(r)
	if err != nil {
		s.badRequest(w, r, err)
		return
	}
	id, _ := strconv.ParseInt(r.FormValue("label"), 10, 64)
	l, err := s.st.GetLabel(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if l == nil {
		s.backTo(w, r, "memerror", "Pick a label to add or remove.")
		return
	}
	add := r.FormValue("op") != "remove"
	if err := s.st.LabelModels(r.Context(), l.ID, names, add); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("models labelled", "label", l.Name, "add", add, "models", len(names), "by", currentUser(r).Username)
	msg := fmt.Sprintf("Added %s to %s.", l.Name, countOf(names))
	if !add {
		msg = fmt.Sprintf("Took %s off %s.", l.Name, countOf(names))
	}
	s.backTo(w, r, "labelled", msg)
}
