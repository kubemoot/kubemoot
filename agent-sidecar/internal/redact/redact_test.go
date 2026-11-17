package redact

import (
	"strings"
	"testing"
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
