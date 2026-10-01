package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mophead64/ollama-model-manager/internal/ollama"
)

// Ollama stores each file a model is made of (its weights, template, licence,
// parameters and config: "layers") once, as a blob named by its digest, and
// each tag as a manifest listing its layers. Tags with the same manifest
// digest are the same model (llama3.2:latest and llama3.2:3b), and different
// models can share layers (a variant made from another model keeps its
// weights). So adding up the tags' sizes, as /api/tags reports them, counts
// shared files more than once. libraryStorage works out the real figures
// from the manifests, when the models folder is visible here (MODELS_DIR).

// layer is one blob a model uses.
type layer struct {
	Digest string
	Size   int64
}

// libraryStorage is how the models share the disk.
type libraryStorage struct {
	Models int   // distinct models: tags with the same digest count once
	Tags   int   // as Ollama lists them
	Bytes  int64 // on disk, each layer once
	// Every model's layers were read from its manifest. Otherwise the
	// figures only allow for tags that are the same model: each unread one
	// counts as a single blob of its listed size.
	Exact bool

	keyOf  map[string]string   // tag name -> model key (its digest)
	tagsOf map[string][]string // model key -> its tags
	layers map[string][]layer  // model key -> its layers
	users  map[string]int      // layer digest -> how many models use it
}

// modelKey identifies a model: its digest, or its name when Ollama didn't
// report one.
func modelKey(m ollama.Model) string {
	if m.Digest != "" {
		return m.Digest
	}
	return "name:" + m.Name
}

// manifestCache keeps manifests' layers by digest. A manifest's digest is
// the hash of its content, so an entry can never go stale.
type manifestCache struct {
	mu     sync.Mutex
	layers map[string][]layer
}

// storage works out how the models in all use the disk.
func (s *Server) storage(all []ollama.Model) *libraryStorage {
	return computeStorage(all, func(m ollama.Model) []layer { return s.manifestLayers(m) })
}

func computeStorage(all []ollama.Model, layersOf func(ollama.Model) []layer) *libraryStorage {
	st := &libraryStorage{
		Tags: len(all), Exact: true,
		keyOf: map[string]string{}, tagsOf: map[string][]string{},
		layers: map[string][]layer{}, users: map[string]int{},
	}
	for _, m := range all {
		key := modelKey(m)
		st.keyOf[m.Name] = key
		st.tagsOf[key] = append(st.tagsOf[key], m.Name)
		if _, seen := st.layers[key]; seen {
			continue
		}
		ls := layersOf(m)
		if ls == nil {
			st.Exact = false
			ls = []layer{{Digest: "model:" + key, Size: m.Size}}
		}
		st.layers[key] = ls
		for _, l := range ls {
			if st.users[l.Digest] == 0 {
				st.Bytes += l.Size
			}
			st.users[l.Digest]++
		}
	}
	st.Models = len(st.layers)
	return st
}

// manifestLayers reads a model's layers from its manifest in the models
// folder, or nil if they can't be read (the folder isn't visible here, or
// the manifest isn't the one Ollama listed).
func (s *Server) manifestLayers(m ollama.Model) []layer {
	if s.cfg.ModelsDir == "" || m.Digest == "" {
		return nil
	}
	s.manifests.mu.Lock()
	ls, ok := s.manifests.layers[m.Digest]
	s.manifests.mu.Unlock()
	if ok {
		return ls
	}
	ls = readManifestLayers(s.cfg.ModelsDir, m)
	if ls != nil {
		s.manifests.mu.Lock()
		if s.manifests.layers == nil || len(s.manifests.layers) > 5000 { // only ever as many as there are models
			s.manifests.layers = map[string][]layer{}
		}
		s.manifests.layers[m.Digest] = ls
		s.manifests.mu.Unlock()
	}
	return ls
}

// readManifestLayers reads m's manifest, at manifests/<host>/<namespace>/
// <model>/<tag> in the models folder, checking it hashes to m's digest.
func readManifestLayers(modelsDir string, m ollama.Model) []layer {
	ref, err := ollama.ParseName(m.Name)
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(modelsDir, "manifests", ref.Host, ref.Namespace, ref.Model, ref.Tag))
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != strings.TrimPrefix(m.Digest, "sha256:") {
		return nil
	}
	var man struct {
		Config layer   `json:"config"`
		Layers []layer `json:"layers"`
	}
	if json.Unmarshal(b, &man) != nil {
		return nil
	}
	ls := man.Layers
	if man.Config.Digest != "" {
		ls = append(ls, man.Config)
	}
	return ls
}

// Freed is the space deleting all of names would free: the layers no
// remaining model uses. A tag whose model has another tag left frees nothing.
func (st *libraryStorage) Freed(names ...string) int64 {
	gone := map[string]bool{}
	for _, n := range names {
		gone[n] = true
	}
	drop := map[string]int{} // layer digest -> removed models using it
	size := map[string]int64{}
	for key, tags := range st.tagsOf {
		if slices.ContainsFunc(tags, func(t string) bool { return !gone[t] }) {
			continue // some tag of it stays
		}
		for _, l := range st.layers[key] {
			drop[l.Digest]++
			size[l.Digest] = l.Size
		}
	}
	var freed int64
	for d, n := range drop {
		if n == st.users[d] {
			freed += size[d]
		}
	}
	return freed
}

// SameAs lists the other tags of name's model (same digest).
func (st *libraryStorage) SameAs(name string) []string {
	var out []string
	for _, t := range st.tagsOf[st.keyOf[name]] {
		if t != name {
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return out
}

// DeleteEffect says what deleting names does to the disk, for the delete
// dialogs: "freeing 4.7 GB", or "freeing up to 4.7 GB" when layers couldn't
// be read, or why it frees nothing.
func (st *libraryStorage) DeleteEffect(names ...string) string {
	freed := st.Freed(names...)
	switch {
	case freed > 0 && st.Exact:
		return "freeing " + formatBytes(freed)
	case freed > 0:
		return "freeing up to " + formatBytes(freed)
	case len(names) == 1 && len(st.SameAs(names[0])) > 0:
		return fmt.Sprintf("but that frees no space: %s is the same model and keeps its files", strings.Join(st.SameAs(names[0]), ", "))
	case len(names) == 1:
		return "but that frees no space: its files are shared with other models"
	}
	return "but that frees no space: their files are shared with other models"
}
