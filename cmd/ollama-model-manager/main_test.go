package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultDBPathIsBesideTheBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	if got, want := defaultDBPath(), filepath.Join(filepath.Dir(exe), "omm.db"); got != want {
		t.Errorf("defaultDBPath() = %s, want %s", got, want)
	}
}

func TestDescribeEnvHidesTokenAndReportsSources(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("OLLAMA_HOST", "http://gpu-box:11434")
	t.Setenv("MODELS_DIR", "")
	t.Setenv("ALLOW_MODEL_DELETE", "maybe")
	t.Setenv("HF_TOKEN", "hf_supersecret")
	t.Setenv("TRUSTED_PROXIES", "172.18.0.0/16, nonsense")
	t.Setenv("TZ", "")

	env := describeEnv("8080", "http://gpu-box:11434", "/data/omm.db", "", true, true, true)
	got := map[string][2]string{}
	for _, e := range env {
		if strings.Contains(e.Value, "hf_") {
			t.Errorf("%s exposes the token: %q", e.Name, e.Value)
		}
		got[e.Name] = [2]string{e.Value, e.Source}
	}
	for name, want := range map[string][2]string{
		"PORT":               {"8080", "default"},
		"OLLAMA_HOST":        {"http://gpu-box:11434", "set"},
		"MODELS_DIR":         {"", "not found"},
		"ALLOW_MODEL_DELETE": {"true", "invalid, using default"},
		"HF_TOKEN":           {"set", "set"},
		"TRUSTED_PROXIES":    {"172.18.0.0/16, nonsense", "invalid entries ignored"},
	} {
		if got[name] != want {
			t.Errorf("%s = %v, want %v", name, got[name], want)
		}
	}
	if tz := got["TZ"]; tz[1] != "not set" || !strings.Contains(tz[0], "(UTC") {
		t.Errorf("TZ = %v, want the zone in effect and \"not set\"", tz)
	}
}

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	if got := healthcheck(u.Port()); got != 0 {
		t.Errorf("healthy app: exit %d, want 0", got)
	}
	status = http.StatusServiceUnavailable
	if got := healthcheck(u.Port()); got != 1 {
		t.Errorf("unhealthy app: exit %d, want 1", got)
	}
	srv.Close()
	if got := healthcheck(u.Port()); got != 1 {
		t.Errorf("app not running: exit %d, want 1", got)
	}
}
