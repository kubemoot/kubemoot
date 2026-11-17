package shim

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactRemovesCredentialShapes(t *testing.T) {
	cases := map[string]string{
		"Authorization: Bearer abcdefgh12345678":        "abcdefgh12345678",
		"bearer abcdefgh12345678 more":                  "abcdefgh12345678",
		"OPENAI_API_KEY=sk-abcdefghijklmnop12345678":    "abcdefghijklmnop12345678",
		`password: "hunter2hunter2"`:                    "hunter2hunter2",
		"token=abc123":                                  "abc123",
		"gh token ghp_abcdefghijklmnopqrstuvwxyz0123":   "ghp_abcdefghijklmnop",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0.sig": "eyJhbGciOiJIUzI1NiJ9",
		"key AKIAABCDEFGHIJKLMNOP end":                  "AKIAABCDEFGHIJKLMNOP",
		"id 0123456789abcdef0123456789abcdef":           "0123456789abcdef0123456789abcdef",
		"github_pat_abcdefghijklmnopqrstuvwxyz":         "github_pat_abcdefghijklmnop",
	}
	for in, secret := range cases {
		got := Redact(in)
		if strings.Contains(got, secret) || !strings.Contains(got, redacted) {
			t.Errorf("%q -> %q still shows %q", in, got, secret)
		}
	}
}

func TestRedactKeepsOrdinaryText(t *testing.T) {
	for _, in := range []string{"", "Error: the model door said no", "exit status 1: tokens used 12", "a 1234 b"} {
		if got := Redact(in); got != in {
			t.Errorf("%q changed to %q", in, got)
		}
	}
}

func TestRedactRemovesAGivenSecretWhateverItsShape(t *testing.T) {
	got := Redact("failed with key kmt_turn_Zq81xx here", "kmt_turn_Zq81xx")
	if strings.Contains(got, "Zq81xx") {
		t.Fatalf("got %q", got)
	}
	if got := Redact("short abc", "abc"); got != "short abc" {
		t.Fatalf("a very short secret is not matched blindly: %q", got)
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
