/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package secretscreen says whether a piece of text holds a credential. It is the
// operator module's copy of the secret shapes the agent-sidecar
// (agent-sidecar/internal/redact) and the harness-shim (harness-shim/internal/shim)
// redact. Go does not let one module import another's internal package, and
// moving the list into a fourth module would add a dependency to three
// otherwise independent modules for eight patterns. The three lists are held to
// the one fixture, testdata/redact-cases.json, which each module's tests load,
// so a change to one list that the others do not follow fails a test. Change all
// three together.
package secretscreen

import "regexp"

// shape is a named pattern a credential takes. The name is what a report may
// say; the matched text never is.
type shape struct {
	name string
	re   *regexp.Regexp
}

var shapes = []shape{
	{"bearer or basic token", regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`)},
	{"key, token, secret or password assignment", regexp.MustCompile(
		`(?i)\b([\w.-]*(?:api[_-]?key|token|secret|passw(?:or)?d|authorization)[\w.-]*)` +
			`\s*[=:]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)},
	{"API key", regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`)},
	{"GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`)},
	{"GitHub fine-grained token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`)},
	{"AWS access key id", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"JSON Web Token", regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)},
	{"long hexadecimal string", regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)},
}

// Find returns the name of the first secret shape that text holds. It never
// returns the matched text.
func Find(text string) (string, bool) {
	for _, s := range shapes {
		if s.re.MatchString(text) {
			return s.name, true
		}
	}
	return "", false
}
