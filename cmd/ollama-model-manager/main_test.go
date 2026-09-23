package main

import (
	"os"
	"path/filepath"
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
