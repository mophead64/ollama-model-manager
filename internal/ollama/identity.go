package ollama

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var (
	// ErrKeyUnavailable means Ollama didn't offer its public key: it's
	// signed in to ollama.com already, or too old to have sign-in.
	ErrKeyUnavailable = errors.New("ollama didn't report its public key")
)

// PublicKey returns the public half of the key Ollama signs its registry
// requests with (~/.ollama/id_ed25519.pub on its machine), e.g.
// "ssh-ed25519 AAAA…". Hugging Face accepts it as an SSH key, which is how
// Ollama gets access to gated and private repos there.
//
// There's no API for the key as such. Ollama's POST /api/me answers a
// signed-out request with a link to connect it to ollama.com, and that link
// carries the key; so this works until Ollama is signed in to ollama.com (or
// on versions without sign-in), when it returns ErrKeyUnavailable.
func (c *Client) PublicKey(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/me", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("contact ollama at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	var body struct {
		SigninURL string `json:"signin_url"`
	}
	if resp.StatusCode != http.StatusUnauthorized ||
		json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body) != nil || body.SigninURL == "" {
		return "", ErrKeyUnavailable
	}
	return keyFromSigninURL(body.SigninURL)
}

// keyFromSigninURL decodes the key parameter of an ollama.com connect link:
// the public key line, base64-encoded without padding.
func keyFromSigninURL(link string) (string, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", ErrKeyUnavailable
	}
	enc := strings.TrimRight(u.Query().Get("key"), "=")
	for _, e := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := e.DecodeString(enc); err == nil {
			key := strings.TrimSpace(string(b))
			if strings.HasPrefix(key, "ssh-") && len(strings.Fields(key)) >= 2 {
				return key, nil
			}
		}
	}
	return "", ErrKeyUnavailable
}

// HFTokenTransport adds a Hugging Face access token to requests to
// huggingface.co (its website API, where the token is honoured), and to
// nothing else. A nil Base uses http.DefaultTransport.
type HFTokenTransport struct {
	Token string
	Base  http.RoundTripper
}

func (t HFTokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.Token != "" && r.URL.Hostname() == "huggingface.co" && r.Header.Get("Authorization") == "" {
		r = r.Clone(r.Context())
		r.Header.Set("Authorization", "Bearer "+t.Token)
	}
	return base.RoundTrip(r)
}
