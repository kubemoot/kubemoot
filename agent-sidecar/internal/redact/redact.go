package redact

import (
	"regexp"
	"strings"
)

const redacted = "[redacted]"

// secretShape is a pattern and what replaces a match: a name keeps its label, a bare secret goes whole.
type secretShape struct {
	re   *regexp.Regexp
	with string
}

// secretShapes are the forms a credential takes in a command's output. The harness-shim keeps the
// same list (harness-shim/internal/shim) for what it publishes; change both together.
var secretShapes = []secretShape{
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 " + redacted},
	{regexp.MustCompile(`(?i)\b([\w.-]*(?:api[_-]?key|token|secret|passw(?:or)?d|authorization)[\w.-]*)` +
		`\s*[=:]\s*("[^"]*"|'[^']*'|[^\s,;]+)`), "$1 " + redacted},
	{regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`), redacted},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), redacted},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`), redacted},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`), redacted},
	{regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`), redacted},
}

// Redact removes credentials from text the sidecar publishes: each exact secret given, and anything
// shaped like a bearer token, an API key, a token or password assignment, or a JWT.
func Redact(text string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 8 {
			text = strings.ReplaceAll(text, s, redacted)
		}
	}
	for _, s := range secretShapes {
		text = s.re.ReplaceAllString(text, s.with)
	}
	return text
}
