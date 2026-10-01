package web

import (
	"cmp"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
	"github.com/mophead64/ollama-model-manager/internal/store"
)

// Limits on a model test, so one can't tie Ollama up for days.
const (
	testMaxModels  = 10
	testMaxRepeats = 20
	testMaxPrompt  = 20000 // characters
	testMaxName    = 100

	testDefaultRepeats = 3
)

// testForm is the New test form's values, kept when it's sent back with an error.
type testForm struct {
	Name    string
	Prompt  string
	Models  []string
	Repeats int
}

// testModelOption is a model in the New test form's picker.
type testModelOption struct {
	Name     string // what's sent
	Short    string // the model itself, e.g. "Qwen3-8B-GGUF" from hf.co/owner/Qwen3-8B-GGUF:Q4_K_M
	Source   string // where it's from, e.g. "hf.co/owner"; "" for the ollama.com library
	Tag      string
	Family   string
	Params   string
	Size     int64
	EstMS    float64 // average time per run in earlier tests; 0 if never tested
	Fit      fit     // only shown when it won't fit fully in VRAM
	Selected bool
}

// Sub is the picker's second line for the model: where it's from, its tag
// and its family, whichever are known.
func (o testModelOption) Sub() []string {
	var out []string
	for _, p := range []string{o.Source, o.Tag, o.Family} {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// testModelOptions lists the models that can chat for the picker, grouped by
// family and smallest first within each, so comparable models sit together.
func (s *Server) testModelOptions(r *http.Request, all []ollama.Model, selected []string) []testModelOption {
	times, err := s.st.ModelRunTimes(r.Context())
	if err != nil {
		s.log.Warn("read model run times failed", "error", err)
	}
	snap := s.sys.Latest()
	var out []testModelOption
	for _, m := range chatModels(all) {
		o := testModelOption{Name: m.Name, Short: m.Name, Family: m.Details.Family, Params: m.Details.ParameterSize,
			Size: m.Size, EstMS: times[m.Name], Selected: slices.Contains(selected, m.Name)}
		if ref, err := ollama.ParseName(m.Name); err == nil {
			o.Short, o.Tag = ref.Model, ref.Tag
			switch {
			case ref.Host == "hf.co":
				o.Source = "hf.co/" + ref.Namespace
			case ref.Host != "registry.ollama.ai":
				o.Source = ref.Host + "/" + ref.Namespace
			case ref.Namespace != "library":
				o.Source = ref.Namespace
			}
		}
		if f := estimateFit(m.Size, snap); f.Level != "unknown" && f.Level != "gpu" {
			o.Fit = f
		}
		out = append(out, o)
	}
	slices.SortStableFunc(out, func(a, b testModelOption) int {
		if c := strings.Compare(strings.ToLower(a.Family), strings.ToLower(b.Family)); c != 0 {
			return c
		}
		return cmp.Compare(a.Size, b.Size)
	})
	return out
}

// handleTests is the models section's Testing tab: the tests so far, and a
// form to start a new one. ?template= fills the form in from a template, and
// ?model= ticks models and ?name= names the test (e.g. linked from the
// Models page's "Compare in a test").
func (s *Server) handleTests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	form := testForm{Repeats: testDefaultRepeats, Models: q["model"], Name: strings.TrimSpace(q.Get("name"))}
	if len([]rune(form.Name)) > testMaxName {
		form.Name = string([]rune(form.Name)[:testMaxName])
	}
	var notice string
	if id, err := strconv.ParseInt(q.Get("template"), 10, 64); err == nil {
		switch t, err := s.st.GetModelTestTemplate(r.Context(), id); {
		case err != nil:
			s.log.Error("read model test template failed", "error", err)
		case t != nil:
			form = testForm{Name: t.Name, Prompt: t.Prompt, Models: t.Models, Repeats: t.Repeats}
			notice = "Filled in from the template " + t.Name + "."
			if q.Get("saved") != "" {
				notice = "Saved the template " + t.Name + ". It's filled in below; queue it, or pick it again any time."
			}
		}
	}
	s.renderTests(w, r, form, "", notice, http.StatusOK)
}

func (s *Server) renderTests(w http.ResponseWriter, r *http.Request, form testForm, formErr, notice string, status int) {
	q := r.URL.Query()
	data := map[string]any{"Form": form, "FormError": formErr, "Notice": notice, "Deleted": q.Get("deleted"),
		"TemplateDeleted": q.Get("template_deleted"), "MaxModels": testMaxModels, "MaxRepeats": testMaxRepeats,
		"DefaultRepeats": testDefaultRepeats, "MaxPrompt": testMaxPrompt, "Presets": []int{1, 3, 5, 10}}
	if all, err := s.ol.List(r.Context()); err != nil {
		s.log.Error("list models failed", "error", err)
		data["Error"] = err.Error()
	} else {
		s.addOverview(r, all, data)
		opts := s.testModelOptions(r, all, form.Models)
		data["ModelOptions"] = opts
		// Models the form (or a template) names that aren't here any more.
		var missing []string
		for _, m := range form.Models {
			if !slices.ContainsFunc(opts, func(o testModelOption) bool { return o.Name == m }) {
				missing = append(missing, m)
			}
		}
		data["Missing"] = missing
	}
	tests, err := s.st.ModelTests(r.Context())
	if err != nil {
		s.log.Error("read model tests failed", "error", err)
		data["TestsErr"] = err.Error()
	}
	data["Tests"] = tests
	data["AnyActive"] = slices.ContainsFunc(tests, func(t store.ModelTest) bool { return t.Active() })
	templates, err := s.st.ModelTestTemplates(r.Context())
	if err != nil {
		s.log.Error("read model test templates failed", "error", err)
	}
	data["Templates"] = templates
	w.WriteHeader(status)
	s.render(w, r, "tests.html", data)
}

// parseTestForm reads the New test form.
func parseTestForm(r *http.Request) testForm {
	form := testForm{
		Name:   strings.TrimSpace(r.FormValue("name")),
		Prompt: strings.TrimSpace(r.FormValue("prompt")),
	}
	for _, m := range r.Form["model"] {
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(form.Models, m) {
			form.Models = append(form.Models, m)
		}
	}
	form.Repeats, _ = strconv.Atoi(r.FormValue("repeats"))
	return form
}

// handleCreateTest queues a new test from the form, then shows it.
func (s *Server) handleCreateTest(w http.ResponseWriter, r *http.Request) {
	form := parseTestForm(r)
	if msg := s.checkTestForm(r, form, true); msg != "" {
		s.renderTests(w, r, form, msg, "", http.StatusBadRequest)
		return
	}
	name := form.Name
	if name == "" {
		name = promptTitle(form.Prompt)
	}
	s.queueTest(w, r, name, form.Prompt, form.Models, form.Repeats, func(err error) {
		s.renderTests(w, r, form, "Couldn't save the test: "+err.Error(), "", http.StatusInternalServerError)
	})
}

// queueTest creates a test and shows it, or calls failed.
func (s *Server) queueTest(w http.ResponseWriter, r *http.Request, name, prompt string, models []string, repeats int, failed func(error)) {
	id, err := s.st.CreateModelTest(r.Context(), name, prompt, models, repeats, currentUser(r).Username)
	if err != nil {
		s.log.Error("create model test failed", "error", err)
		failed(err)
		return
	}
	s.log.Info("model test queued", "test", id, "models", models, "repeats", repeats, "by", currentUser(r).Username)
	s.mt.Wake()
	http.Redirect(w, r, fmt.Sprintf("/models/testing/%d?queued=1", id), http.StatusSeeOther)
}

// handleSaveTemplate saves the New test form as a template (its "Save as
// template" button), then shows the form filled in from it.
func (s *Server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	form := parseTestForm(r)
	if msg := s.checkTestForm(r, form, false); msg != "" {
		s.renderTests(w, r, form, msg, "", http.StatusBadRequest)
		return
	}
	s.saveTemplate(w, r, store.ModelTestTemplate{Name: form.Name, Prompt: form.Prompt, Models: form.Models, Repeats: form.Repeats})
}

// handleSaveTestAsTemplate saves an existing test's settings as a template.
func (s *Server) handleSaveTestAsTemplate(w http.ResponseWriter, r *http.Request) {
	t := s.testFromPath(w, r)
	if t == nil {
		return
	}
	s.saveTemplate(w, r, store.ModelTestTemplate{Name: t.Name, Prompt: t.Prompt, Models: t.Models, Repeats: t.Repeats})
}

func (s *Server) saveTemplate(w http.ResponseWriter, r *http.Request, t store.ModelTestTemplate) {
	if t.Name == "" {
		t.Name = promptTitle(t.Prompt)
	}
	t.CreatedBy = currentUser(r).Username
	id, err := s.st.SaveModelTestTemplate(r.Context(), t)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("model test template saved", "template", id, "by", t.CreatedBy)
	http.Redirect(w, r, fmt.Sprintf("/models/testing?template=%d&saved=1", id), http.StatusSeeOther)
}

func (s *Server) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	t := s.templateFromPath(w, r)
	if t == nil {
		return
	}
	if err := s.st.DeleteModelTestTemplate(r.Context(), t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("model test template deleted", "template", t.ID, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/testing?template_deleted="+url.QueryEscape(t.Name), http.StatusSeeOther)
}

func (s *Server) templateFromPath(w http.ResponseWriter, r *http.Request) *store.ModelTestTemplate {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad template id"))
		return nil
	}
	t, err := s.st.GetModelTestTemplate(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	if t == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "There's no such template; it may have been deleted."})
		return nil
	}
	return t
}

// checkTestForm returns what's wrong with form, or "". A template can be
// saved without models (needModels false), to pick them when it's run.
func (s *Server) checkTestForm(r *http.Request, form testForm, needModels bool) string {
	switch {
	case needModels && len(form.Models) == 0:
		return "Pick at least one model."
	case len(form.Models) > testMaxModels:
		return fmt.Sprintf("Pick at most %d models.", testMaxModels)
	case form.Prompt == "":
		return "Enter a prompt."
	case len([]rune(form.Prompt)) > testMaxPrompt:
		return fmt.Sprintf("The prompt can be at most %d characters.", testMaxPrompt)
	case len([]rune(form.Name)) > testMaxName:
		return fmt.Sprintf("The name can be at most %d characters.", testMaxName)
	case form.Repeats < 1 || form.Repeats > testMaxRepeats:
		return fmt.Sprintf("Run each model between 1 and %d times.", testMaxRepeats)
	}
	all, err := s.ol.List(r.Context())
	if err != nil {
		return "Couldn't list models from Ollama: " + err.Error()
	}
	usable := chatModels(all)
	for _, m := range form.Models {
		if !slices.ContainsFunc(usable, func(u ollama.Model) bool { return u.Name == m }) {
			return m + " isn't a model on this Ollama server that can chat."
		}
	}
	return ""
}

// promptTitle names a test after the start of its prompt.
func promptTitle(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	if r := []rune(line); len(r) > 60 {
		return strings.TrimSpace(string(r[:60])) + "…"
	}
	return line
}

// testSummary is how one model did across a test's runs.
type testSummary struct {
	Model      string
	Runs       []store.ModelTestRun
	OK, Failed int
	AvgTPS     float64 // tokens/s, over completed runs
	AvgTokens  float64
	AvgTotalMS float64
	MaxLoadMS  int64 // usually the first run, when the model had to be loaded
}

func summarise(runs []store.ModelTestRun) []testSummary {
	var out []testSummary
	for _, run := range runs {
		if len(out) == 0 || out[len(out)-1].Model != run.Model || out[len(out)-1].Runs[0].Position != run.Position {
			out = append(out, testSummary{Model: run.Model})
		}
		sum := &out[len(out)-1]
		sum.Runs = append(sum.Runs, run)
		switch run.Status {
		case store.TestCompleted:
			sum.OK++
			sum.AvgTPS += run.TokensPerSec()
			sum.AvgTokens += float64(run.Tokens)
			sum.AvgTotalMS += float64(run.TotalMS)
			sum.MaxLoadMS = max(sum.MaxLoadMS, run.LoadMS)
		case store.TestFailed:
			sum.Failed++
		}
	}
	for i := range out {
		if n := float64(out[i].OK); n > 0 {
			out[i].AvgTPS /= n
			out[i].AvgTokens /= n
			out[i].AvgTotalMS /= n
		}
	}
	return out
}

// handleTestDetail shows a test's results, refreshing itself while it runs.
func (s *Server) handleTestDetail(w http.ResponseWriter, r *http.Request) {
	t := s.testFromPath(w, r)
	if t == nil {
		return
	}
	runs, err := s.st.ModelTestRuns(r.Context(), t.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	data := map[string]any{"Test": t, "Summary": summarise(runs), "Cancelled": q.Get("cancelled") != "", "Queued": q.Get("queued") != ""}
	s.addModelsOverview(r, data)
	s.render(w, r, "test_detail.html", data)
}

// testFromPath is the test named by the {id} in the path, or nil after a
// 400 or 404 has been sent.
func (s *Server) testFromPath(w http.ResponseWriter, r *http.Request) *store.ModelTest {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, r, errMsg("bad test id"))
		return nil
	}
	t, err := s.st.GetModelTest(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return nil
	}
	if t == nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "There's no such test; it may have been deleted."})
		return nil
	}
	return t
}

func (s *Server) handleCancelTest(w http.ResponseWriter, r *http.Request) {
	t := s.testFromPath(w, r)
	if t == nil {
		return
	}
	if err := s.mt.Cancel(r.Context(), t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.log.Info("model test cancelled", "test", t.ID, "by", currentUser(r).Username)
	http.Redirect(w, r, fmt.Sprintf("/models/testing/%d?cancelled=1", t.ID), http.StatusSeeOther)
}

// handleRerunTest queues a copy of a test, to run it again.
func (s *Server) handleRerunTest(w http.ResponseWriter, r *http.Request) {
	t := s.testFromPath(w, r)
	if t == nil {
		return
	}
	s.queueTest(w, r, t.Name, t.Prompt, t.Models, t.Repeats, func(err error) { s.serverError(w, r, err) })
}

func (s *Server) handleDeleteTest(w http.ResponseWriter, r *http.Request) {
	t := s.testFromPath(w, r)
	if t == nil {
		return
	}
	ok, err := s.st.DeleteModelTest(r.Context(), t.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.badRequest(w, r, errMsg("Cancel the test before deleting it."))
		return
	}
	s.log.Info("model test deleted", "test", t.ID, "by", currentUser(r).Username)
	http.Redirect(w, r, "/models/testing?deleted="+url.QueryEscape(t.Name), http.StatusSeeOther)
}
