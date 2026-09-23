package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeChatOllama streams a two-part reply, or an error for "broken".
func fakeChatOllama(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			w.Write([]byte(`{"models":[]}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"model":"broken"`) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"model 'broken' not found"}`))
			return
		}
		for _, line := range []string{
			`{"message":{"role":"assistant","content":"","thinking":"hmm"},"done":false}`,
			`{"message":{"role":"assistant","content":"Hello"},"done":false}`,
			`{"message":{"role":"assistant","content":" there"},"done":false}`,
			`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","total_duration":2000000000,"load_duration":1500000000,"prompt_eval_count":12,"eval_count":20,"eval_duration":500000000}`,
		} {
			w.Write([]byte(line + "\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func chat(h http.Handler, body string) (int, []chatEvent) {
	req := httptest.NewRequest("POST", "/chat", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(testSession)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var evs []chatEvent
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var ev chatEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			evs = append(evs, ev)
		}
	}
	return rec.Code, evs
}

func TestChatStreamsReply(t *testing.T) {
	h := newTestServer(t, fakeChatOllama(t).URL)

	code, evs := chat(h, `{"model":"llama3.2:latest","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK || len(evs) != 4 {
		t.Fatalf("status %d, events %+v", code, evs)
	}
	if evs[0].Thinking != "hmm" || evs[1].Content != "Hello" || evs[2].Content != " there" {
		t.Errorf("reply pieces = %+v", evs[:3])
	}
	st := evs[3].Stats
	if !evs[3].Done || st == nil || st.Tokens != 20 || st.TokensPerSec != 40 || st.LoadMS != 1500 || st.TotalMS != 2000 || st.PromptTokens != 12 {
		t.Errorf("final event = %+v %+v", evs[3], st)
	}

	// Ollama's reason for refusing is passed on.
	_, evs = chat(h, `{"model":"broken","messages":[{"role":"user","content":"hi"}]}`)
	if len(evs) != 1 || evs[0].Error != "model 'broken' not found" {
		t.Errorf("error events = %+v", evs)
	}

	// Malformed requests are refused before reaching Ollama.
	for _, bad := range []string{`not json`, `{"model":"","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","messages":[]}`, `{"model":"m","messages":[{"role":"system","content":"x"}]}`} {
		if code, _ := chat(h, bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", bad, code)
		}
	}
}

func TestChatPage(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)

	// Every model that can chat is in the picker, with its parameters and size.
	page := get(h, "/chat", false).Body.String()
	for _, want := range []string{
		`data-value="model-000:latest"`, `data-value="user/custom:v1"`,
		`<span class="item-meta">2.0 KB`, "Choose a model…", "Pick a model first",
		`<a href="/chat" class="active" aria-current="page">Chat</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("chat page missing %q", want)
		}
	}

	// ?model= picks one: the toggle shows it, and its properties are shown.
	page = get(h, "/chat?model=user/custom:v1", false).Body.String()
	for _, want := range []string{
		`data-model="user/custom:v1"`, `aria-selected="true"`, `id="chat-model-info" hx-get="/chat/model?name=user/custom:v1"`,
		"<dt>Family</dt><dd>qwen</dd>", "50%/50% CPU/GPU", "Unload from memory",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("chat page with a model picked missing %q", want)
		}
	}

	// The properties panel on its own, as the picker and live refreshes fetch it.
	frag := get(h, "/chat/model?name=model-000:latest", true).Body.String()
	if !strings.Contains(frag, `<h2 style="margin:0" class="mono">model-000:latest</h2>`) || strings.Contains(frag, "<!doctype html>") {
		t.Errorf("properties fragment wrong:\n%s", frag)
	}
	if frag := get(h, "/chat/model?name=gone:latest", true).Body.String(); !strings.Contains(frag, "on this Ollama server any more") {
		t.Error("a model that's gone should say so")
	}
}

func TestChatLinks(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	list := get(h, "/models", true).Body.String()
	for _, want := range []string{`href="/chat?model=model-000%3alatest">Chat</a>`, `href="/chat?model=user%2fcustom%3av1">Chat</a>`} {
		if !strings.Contains(list, want) {
			t.Errorf("models list missing %s", want)
		}
	}
	detail := get(h, "/models/user/custom:v1", false).Body.String()
	if !strings.Contains(detail, `href="/chat?model=user%2fcustom%3av1">Chat</a>`) || strings.Contains(detail, "chat.js") {
		t.Error("the model page should link to the Chat page rather than embed a chat")
	}

	for caps, want := range map[string]bool{"": true, "completion,tools": true, "embedding": false} {
		var list []string
		if caps != "" {
			list = strings.Split(caps, ",")
		}
		if got := canChat(list); got != want {
			t.Errorf("canChat(%q) = %v, want %v", caps, got, want)
		}
	}
}

func TestUnloadInPlace(t *testing.T) {
	h := newTestServer(t, fakeOllama(t, 1).URL)
	// From the Chat page (htmx): no redirect, just a nudge to refresh.
	rec := do(h, "POST", "/models/unload", url.Values{"name": {"model-000:latest"}}, testSession, "HX-Request", "true")
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Trigger") != "state-changed" {
		t.Errorf("htmx unload = %d %q", rec.Code, rec.Header().Get("HX-Trigger"))
	}
	rec = do(h, "POST", "/models/load", url.Values{"name": {"fail-me"}}, testSession, "HX-Request", "true")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `class="error-box"`) {
		t.Errorf("htmx failure should come back as an error box: %d %s", rec.Code, rec.Body)
	}
}
