package shim

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestTailCutsOnARuneBoundaryAndKeepsTheEnd(t *testing.T) {
	s := strings.Repeat("é", 800) + "END"
	got := tail(s)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "END") || !strings.HasPrefix(got, "...") {
		t.Fatalf("tail %q", got)
	}
	if short := "  boom \n"; tail(short) != "boom" {
		t.Fatal("a short tail is trimmed whole")
	}
}

func TestAFailedTurnsReasonNeverCarriesTheTurnKey(t *testing.T) {
	_, ts := newTestServer(t, Config{Argv: sh(`echo "auth failed for $OPENAI_API_KEY" >&2; exit 3`)}, testEnv)
	profile := map[string]any{keyTurnKey: "turnkey-secret-value-1234"}
	_, r := post(t, ts.URL+"/message:send", body("q", profile))
	if strings.Contains(r.text(), "turnkey-secret-value-1234") || !strings.Contains(r.text(), "auth failed") {
		t.Fatalf("text %q", r.text())
	}
}
