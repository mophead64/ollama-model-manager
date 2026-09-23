package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Ref is a parsed model name: [host/][namespace/]model[:tag], with Ollama's
// defaults (registry.ollama.ai, library, latest) filled in.
type Ref struct {
	Host, Namespace, Model, Tag string
}

// String is the name as Ollama shows it: defaults left out.
func (r Ref) String() string {
	var b strings.Builder
	if r.Host != defaultHost {
		b.WriteString(r.Host + "/")
	}
	if r.Namespace != defaultNamespace || r.Host != defaultHost {
		b.WriteString(r.Namespace + "/")
	}
	b.WriteString(r.Model + ":" + r.Tag)
	return b.String()
}

const (
	defaultHost      = "registry.ollama.ai"
	defaultNamespace = "library"
	defaultTag       = "latest"
)

var (
	partRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hostRE = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
)

// NormalizeName tidies what a user might paste into the "download" box, e.g.
// "ollama pull qwen3:8b" or "https://ollama.com/library/qwen3", into a model
// name, then parses it.
func NormalizeName(input string) (Ref, error) {
	s := strings.TrimSpace(input)
	for _, p := range []string{"ollama pull ", "ollama run "} {
		if len(s) >= len(p) && strings.EqualFold(s[:len(p)], p) {
			s = strings.TrimSpace(s[len(p):])
		}
	}
	for _, p := range []string{"https://ollama.com/library/", "https://ollama.com/", "https://huggingface.co/", "https://hf.co/"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			rest := s[len(p):]
			if strings.Contains(p, "hugging") || strings.Contains(p, "hf.co") {
				rest = "hf.co/" + rest
			}
			s = rest
			break
		}
	}
	return ParseName(s)
}

// ParseName splits a model name into its parts.
func ParseName(name string) (Ref, error) {
	if name == "" {
		return Ref{}, errors.New("Enter a model name, e.g. llama3.2 or qwen3:8b")
	}
	if strings.ContainsAny(name, " \t\n") {
		return Ref{}, fmt.Errorf("%q isn't a model name: it contains spaces", name)
	}
	r := Ref{Host: defaultHost, Namespace: defaultNamespace, Tag: defaultTag}
	path := name
	// The tag is a ":" after the last "/" (a ":" before it is a host's port).
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		path, r.Tag = name[:i], name[i+1:]
	}
	parts := strings.Split(path, "/")
	switch len(parts) {
	case 1:
		r.Model = parts[0]
	case 2:
		r.Namespace, r.Model = parts[0], parts[1]
	case 3:
		r.Host, r.Namespace, r.Model = parts[0], parts[1], parts[2]
	default:
		return Ref{}, fmt.Errorf("%q isn't a model name: expected [host/][namespace/]model[:tag]", name)
	}
	if !hostRE.MatchString(r.Host) {
		return Ref{}, fmt.Errorf("%q isn't a valid registry host", r.Host)
	}
	for _, p := range []string{r.Namespace, r.Model, r.Tag} {
		if !partRE.MatchString(p) {
			return Ref{}, fmt.Errorf("%q isn't a model name: %q has characters Ollama doesn't allow", name, p)
		}
	}
	return r, nil
}

var (
	// ErrModelNotFound means the registry says the model (or tag) doesn't exist.
	ErrModelNotFound = errors.New("model not found in registry")
	// ErrGated means a Hugging Face repo exists but needs its terms accepted
	// (and Ollama authorised) before it can be pulled.
	ErrGated = errors.New("gated Hugging Face repo")
	// ErrPrivateOrMissing means Hugging Face won't say: it answers the same
	// for a repo that doesn't exist and one that's private.
	ErrPrivateOrMissing = errors.New("Hugging Face repo doesn't exist or is private")
)

// CheckRegistry asks the model's registry whether its manifest exists, so a
// typo is caught when it's queued rather than when the queue reaches it. A
// nil error means it exists; ErrModelNotFound means it doesn't; any other
// error means the registry couldn't be asked (offline, blocked...), which the
// caller can treat as "unknown".
func CheckRegistry(ctx context.Context, client *http.Client, r Ref) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if r.Host == "hf.co" {
		// A gated repo's files answer 401 to anonymous requests, same as a
		// missing repo, so ask the model API first: it describes gated repos
		// publicly.
		if err := checkHFRepo(ctx, client, r); err != nil {
			return err
		}
	}
	url := fmt.Sprintf("https://%s/v2/%s/%s/manifests/%s", r.Host, r.Namespace, r.Model, r.Tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	// The repo is known to exist and be public by now, so a 400 or 401 from
	// Hugging Face means the tag (quantisation) doesn't match a file in it.
	case resp.StatusCode == http.StatusNotFound,
		r.Host == "hf.co" && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusBadRequest):
		return ErrModelNotFound
	default:
		return fmt.Errorf("registry %s returned %s", r.Host, resp.Status)
	}
}

// checkHFRepo asks Hugging Face's model API about a repo: nil for a public
// one, ErrGated, ErrPrivateOrMissing, or a transport error.
func checkHFRepo(ctx context.Context, client *http.Client, r Ref) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://huggingface.co/api/models/%s/%s", r.Namespace, r.Model), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusNotFound:
		return ErrPrivateOrMissing
	default:
		return fmt.Errorf("huggingface.co returned %s", resp.Status)
	}
	var info struct {
		Gated any `json:"gated"` // false, or "auto"/"manual"
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("unexpected response from huggingface.co: %w", err)
	}
	if g, ok := info.Gated.(string); ok && g != "" {
		return ErrGated
	}
	return nil
}
