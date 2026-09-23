package ollama

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicKey(t *testing.T) {
	signedIn := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/me" {
			http.NotFound(w, r)
			return
		}
		if signedIn {
			w.Write([]byte(`{"name":"someone"}`))
			return
		}
		// A real response, from Ollama 0.34.
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized","signin_url":"https://ollama.com/connect?name=host.local&key=c3NoLWVkMjU1MTkgQUFBQUMzTnphQzFsWkRJMU5URTVBQUFBSU1rVmhwVW9qeU0ydWQ3YlBHVURjU0xIUHIwZkFKOGJwRlNPWE13VDFCaVk"}`))
	}))
	defer srv.Close()
	c := New(srv.URL)

	key, err := c.PublicKey(t.Context())
	if want := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMkVhpUojyM2ud7bPGUDcSLHPr0fAJ8bpFSOXMwT1BiY"; err != nil || key != want {
		t.Errorf("key = %q, %v; want %q", key, err, want)
	}
	signedIn = true
	if _, err := c.PublicKey(t.Context()); !errors.Is(err, ErrKeyUnavailable) {
		t.Errorf("signed in: err = %v, want ErrKeyUnavailable", err)
	}
	if _, err := keyFromSigninURL("https://ollama.com/connect?key=bm90IGEga2V5"); !errors.Is(err, ErrKeyUnavailable) {
		t.Errorf("non-key payload accepted: %v", err)
	}
}

func TestHFTokenTransport(t *testing.T) {
	var got []string
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = append(got, r.URL.Host+"="+r.Header.Get("Authorization"))
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	hc := &http.Client{Transport: HFTokenTransport{Token: "hf_x", Base: base}}
	for _, u := range []string{"https://huggingface.co/api/models", "https://hf.co/v2/a/b/manifests/c", "https://ollama.com/"} {
		resp, err := hc.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	want := []string{"huggingface.co=Bearer hf_x", "hf.co=", "ollama.com="}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d: %s, want %s", i, got[i], want[i])
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
