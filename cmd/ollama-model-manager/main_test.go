package main

import (
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

	env := describeEnv("8080", "http://gpu-box:11434", "/data/omm.db", "", true, true)
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
	} {
		if got[name] != want {
			t.Errorf("%s = %v, want %v", name, got[name], want)
		}
	}
}
