package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// keepAliveOption is one choice in the load dialog for how long Ollama keeps
// a model loaded after its last use.
type keepAliveOption struct {
	Value string // Ollama's keep_alive: a duration, "-1" for forever, "" for its default
	Label string
}

var keepAliveOptions = []keepAliveOption{
	{"", "Ollama's default (usually 5 minutes)"}, // OLLAMA_KEEP_ALIVE, if set
	{"30m", "30 minutes"},
	{"1h", "1 hour"},
	{"24h", "24 hours"},
	{"-1", "Until it's unloaded"},
}

// loadTimeout bounds a load: big models from slow disks can take minutes.
const loadTimeout = 10 * time.Minute

// flash is the outcome of a load/unload, shown as a notice on the page the
// user came back to. It travels in the query string (see backTo).
type flash struct {
	Loaded, Unloaded, Error string
}

// flashParams are the query parameters flash is read from.
var flashParams = []string{"loaded", "unloaded", "memerror"}

func readFlash(q url.Values) *flash {
	f := flash{Loaded: q.Get("loaded"), Unloaded: q.Get("unloaded"), Error: q.Get("memerror")}
	if f == (flash{}) {
		return nil
	}
	return &f
}

// handleLoadModel loads a model into memory so its first request doesn't wait,
// then returns to the page it was loaded from. The browser waits for the load
// (its button shows progress); it carries on server-side if the tab is closed.
func (s *Server) handleLoadModel(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	keep := r.FormValue("keep_alive")
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	if !slices.ContainsFunc(keepAliveOptions, func(o keepAliveOption) bool { return o.Value == keep }) {
		s.badRequest(w, r, errMsg("unknown keep-alive choice"))
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), loadTimeout)
	defer cancel()
	start := time.Now()
	if err := s.ol.Load(ctx, name, keep); err != nil {
		s.log.Error("load model failed", "model", name, "error", err)
		s.backTo(w, r, "memerror", fmt.Sprintf("Couldn't load %s: %s", name, describeOllamaErr(err)))
		return
	}
	s.log.Info("model loaded", "model", name, "keep_alive", keep, "took", time.Since(start).Round(time.Millisecond), "by", currentUser(r).Username)
	s.backTo(w, r, "loaded", name)
}

// handleUnloadModel frees the memory a loaded model is using.
func (s *Server) handleUnloadModel(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.badRequest(w, r, errMsg("model name required"))
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Minute)
	defer cancel()
	if err := s.ol.Unload(ctx, name); err != nil {
		s.log.Error("unload model failed", "model", name, "error", err)
		s.backTo(w, r, "memerror", fmt.Sprintf("Couldn't unload %s: %s", name, describeOllamaErr(err)))
		return
	}
	s.log.Info("model unloaded", "model", name, "by", currentUser(r).Username)
	s.backTo(w, r, "unloaded", name)
}

// describeOllamaErr prefers Ollama's own message (e.g. "model requires more
// system memory...") over the wrapped status line.
func describeOllamaErr(err error) string {
	var se *ollama.StatusError
	if errors.As(err, &se) {
		return se.Message
	}
	return err.Error()
}

// backTo redirects to the page the form was submitted from (its Referer,
// reduced to a local path so it can't point off-site), or the models list,
// with key=value added for the notice there.
func (s *Server) backTo(w http.ResponseWriter, r *http.Request, key, value string) {
	target := &url.URL{Path: "/models"}
	if ref, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(ref.Path, "/") &&
		!strings.HasPrefix(ref.Path, "//") && ref.Path != "/login" {
		target = &url.URL{Path: ref.Path, RawQuery: ref.RawQuery}
	}
	q := target.Query()
	for _, p := range flashParams {
		q.Del(p)
	}
	q.Del("deleted")
	q.Set(key, value)
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}
