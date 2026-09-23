package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHuggingFaceSection(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	if acct := get(h, "/account", false).Body.String(); !strings.Contains(acct, `id="huggingface"`) || !strings.Contains(acct, `hx-get="/account/huggingface"`) {
		t.Error("admin's account page has no Hugging Face section")
	}
	body := get(h, "/account/huggingface", true).Body.String()
	for _, want := range []string{`value="ssh-ed25519 AAAAtestkey"`, `data-copy="ollama-key"`, "https://huggingface.co/settings/keys", "set it as <code>HF_TOKEN</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("section missing %q:\n%s", want, body)
		}
	}

	// Anyone but the first account: no section, and no key.
	ctx := context.Background()
	other, err := testStore.CreateUser(ctx, "someone", "password-123")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := testStore.CreateSession(ctx, other.ID)
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	if acct := do(h, "GET", "/account", nil, cookie).Body.String(); strings.Contains(acct, `id="huggingface"`) {
		t.Error("non-admin sees the Hugging Face section")
	}
	if rec := do(h, "GET", "/account/huggingface", nil, cookie, "HX-Request", "true"); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "AAAAtestkey") {
		t.Errorf("non-admin got %d:\n%s", rec.Code, rec.Body)
	}
}

func TestHuggingFaceSectionWithToken(t *testing.T) {
	hf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/whoami-v2" && r.Header.Get("Authorization") == "Bearer hf_good" {
			w.Write([]byte(`{"name":"josh"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer hf.Close()

	for token, want := range map[string]string{
		"hf_good": "Hugging Face token for <strong>josh</strong>",
		"hf_bad":  "Hugging Face rejected the token",
	} {
		h := newTestServer(t, fakeOllama(t, 1).URL, func(c *Config) { c.HFURL, c.HFToken = hf.URL, token })
		if body := get(h, "/account/huggingface", true).Body.String(); !strings.Contains(body, want) {
			t.Errorf("token %s: missing %q:\n%s", token, want, body)
		}
	}
}

func TestHuggingFaceSectionOllamaSignedIn(t *testing.T) {
	// Ollama signed in to ollama.com doesn't offer its key: fall back to
	// telling the admin where to find it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"name":"someone"}`))
	}))
	defer srv.Close()
	h := newTestServer(t, srv.URL)
	if body := get(h, "/account/huggingface", true).Body.String(); !strings.Contains(body, "cat ~/.ollama/id_ed25519.pub") {
		t.Errorf("no fallback:\n%s", body)
	}
}
