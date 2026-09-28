package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// modelSource is where an installed model came from, for the "View on …"
// and "Other quants" actions on its menus.
type modelSource struct {
	Site      string // "ollama.com" or "Hugging Face"; "" if unknown
	URL       string // its page there
	QuantsURL string // the "Other quants" dialog's content; "" if it can't be listed
}

// sourceOf works out a model's source from its name: ollama.com's library
// ("qwen3:8b"), a namespace there ("user/model:tag"; its tags can't be
// listed), or a Hugging Face repo ("hf.co/owner/repo:Q4_K_M").
func sourceOf(name string) modelSource {
	ref, err := ollama.ParseName(name)
	if err != nil {
		return modelSource{}
	}
	quants := "/discover/quants?name=" + url.QueryEscape(name)
	switch {
	case ref.Host == "registry.ollama.ai" && ref.Namespace == "library":
		return modelSource{Site: "ollama.com", URL: "https://ollama.com/library/" + ref.Model, QuantsURL: quants}
	case ref.Host == "registry.ollama.ai":
		return modelSource{Site: "ollama.com", URL: "https://ollama.com/" + ref.Namespace + "/" + ref.Model}
	case ref.Host == "hf.co" || ref.Host == "huggingface.co":
		return modelSource{Site: "Hugging Face", URL: "https://huggingface.co/" + ref.Namespace + "/" + ref.Model, QuantsURL: quants}
	}
	return modelSource{}
}

// handleModelQuants lists the other tags (ollama.com) or quantisations
// (Hugging Face) of an installed model, with Download buttons, for the
// "Other quants" dialog. It's Discover's list, for that one model.
func (s *Server) handleModelQuants(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	ref, err := ollama.ParseName(name)
	src := sourceOf(name)
	if err != nil || src.QuantsURL == "" {
		s.render(w, r, "error_fragment.html", map[string]any{"Error": "Other quants can only be listed for models from the ollama.com library or Hugging Face."})
		return
	}
	q := url.Values{}
	if src.Site == "Hugging Face" {
		q.Set("repo", ref.Namespace+"/"+ref.Model)
		// For the Context column: the installed quant's, as the others share it.
		if all, err := s.ol.List(r.Context()); err == nil {
			for _, m := range all {
				if m.Name == name && m.Details.ContextLength > 0 {
					q.Set("ctx", strconv.Itoa(m.Details.ContextLength))
				}
			}
		}
		r.URL.RawQuery = q.Encode()
		s.handleDiscoverHFFiles(w, r)
		return
	}
	q.Set("model", ref.Model)
	r.URL.RawQuery = q.Encode()
	s.handleDiscoverTags(w, r)
}
