package redact

import (
	"encoding/json"
	"os"
	"testing"
)

// redactCases is the fixture both redaction lists are held to: harness-shim/internal/shim and
// agent-sidecar/internal/redact load the same file, so a change to one list fails the other's test.
type redactCase struct {
	Name     string   `json:"name"`
	Input    string   `json:"input"`
	Secrets  []string `json:"secrets"`
	Expected string   `json:"expected"`
}

func loadRedactCases(t *testing.T) []redactCase {
	t.Helper()
	raw, err := os.ReadFile("../../../testdata/redact-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []redactCase
	if err := json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("redact-cases.json: %v (%d cases)", err, len(cases))
	}
	return cases
}

func TestRedactMatchesTheSharedCases(t *testing.T) {
	for _, c := range loadRedactCases(t) {
		if got := Redact(c.Input, c.Secrets...); got != c.Expected {
			t.Errorf("%s: %q -> %q, want %q", c.Name, c.Input, got, c.Expected)
		}
	}
}
