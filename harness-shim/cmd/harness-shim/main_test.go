package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServeRefusesAMissingOrInvalidConfig(t *testing.T) {
	if err := serve(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("a missing config was accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte(`{"argv":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := serve(bad); err == nil {
		t.Error("a config with no command was accepted")
	}
}
