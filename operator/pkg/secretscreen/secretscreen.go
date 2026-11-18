/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package secretscreen is the one list of the shapes a credential takes. Redact
// removes them from text a component publishes (the agent-sidecar uses it) and
// is strict: anything shaped like a credential goes. Find says whether text
// holds a credential and is tighter, for places where a false match costs more,
// such as leaving a fact out of a memory export.
//
// The package is public so other modules import it; the agent-sidecar does. The
// harness-shim is the one deliberate second copy (harness-shim/internal/shim),
// because its build context holds only its own directory. That copy is held to
// the shared fixture testdata/redact-cases.json, which this package's tests
// load too, so a change to one list that the other does not follow fails a
// test. Change both together.
package secretscreen

import (
	"regexp"
	"strings"
)

const redacted = "[redacted]"

// Each pattern is the form of one kind of credential. Redact and Find share them.
var (
	reBearer = regexp.MustCompile(`(?i)\b(bearer|basic)\s+([A-Za-z0-9._~+/=-]{8,})`)
	reAssign = regexp.MustCompile(`(?i)\b([\w.-]*(?:api[_-]?key|token|secret|passw(?:or)?d|authorization)[\w.-]*)` +
		`\s*[=:]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
	reAPIKey = regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`)
	reGitHub = regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`)
	rePAT    = regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`)
	reAWS    = regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)
	reJWT    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	reHex    = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)

	// reLabelBefore ends a text with a word that names a credential.
	reLabelBefore = regexp.MustCompile(`(?i)(?:key|secret|token|passw(?:or)?d)[\w.-]*[\s=:"']{0,4}$`)
	// reDigestBefore ends a text with the name of a content digest algorithm.
	reDigestBefore = regexp.MustCompile(`(?i)\b(?:sha1|sha224|sha256|sha384|sha512|md5):$`)
)

// Redact removes credentials from text that leaves a component: each exact
// secret given, and anything shaped like a bearer token, an API key, a token or
// password assignment, or a JWT.
func Redact(text string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 8 {
			text = strings.ReplaceAll(text, s, redacted)
		}
	}
	text = reBearer.ReplaceAllString(text, "$1 "+redacted)
	text = reAssign.ReplaceAllString(text, "$1 "+redacted)
	for _, re := range []*regexp.Regexp{reAPIKey, reGitHub, rePAT, reAWS, reJWT, reHex} {
		text = re.ReplaceAllString(text, redacted)
	}
	return text
}

// screen is a named test for one kind of credential.
type screen struct {
	name string
	hit  func(string) bool
}

var screens = []screen{
	{"bearer or basic token", hitBearer},
	{"key, token, secret or password assignment", hitAssignment},
	{"API key", hitAPIKey},
	{"GitHub token", func(t string) bool { return reGitHub.MatchString(t) }},
	{"GitHub fine-grained token", func(t string) bool { return rePAT.MatchString(t) }},
	{"AWS access key id", func(t string) bool { return reAWS.MatchString(t) }},
	{"JSON Web Token", func(t string) bool { return reJWT.MatchString(t) }},
	{"long hexadecimal string", hitHex},
}

// Find returns the name of the first credential shape text holds. It never
// returns the matched text. It is tighter than Redact: a Bearer or Basic word
// needs a token that is not plain lowercase, an assignment needs a value of
// eight characters, an API key needs a digit, a content digest is not a
// credential, and a bare 40 or 64 character hex string (a commit or SHA-256)
// counts only next to a word that names a credential.
func Find(text string) (string, bool) {
	for _, s := range screens {
		if s.hit(text) {
			return s.name, true
		}
	}
	return "", false
}

func hitBearer(text string) bool {
	for _, m := range reBearer.FindAllStringSubmatch(text, -1) {
		if looksGenerated(m[2]) {
			return true
		}
	}
	return false
}

// looksGenerated says a token is not an ordinary word: it holds a digit, mixes
// upper and lower case, or is long.
func looksGenerated(tok string) bool {
	const longToken = 20
	mixedCase := strings.ToLower(tok) != tok && strings.ToUpper(tok) != tok
	return len(tok) >= longToken || mixedCase || strings.ContainsAny(tok, "0123456789")
}

func hitAssignment(text string) bool {
	const minValue = 8
	for _, m := range reAssign.FindAllStringSubmatch(text, -1) {
		if len(strings.Trim(m[2], `"'`)) >= minValue {
			return true
		}
	}
	return false
}

func hitAPIKey(text string) bool {
	for _, m := range reAPIKey.FindAllString(text, -1) {
		if strings.ContainsAny(m, "0123456789") {
			return true
		}
	}
	return false
}

func hitHex(text string) bool {
	for _, loc := range reHex.FindAllStringIndex(text, -1) {
		before := text[:loc[0]]
		if reDigestBefore.MatchString(before) {
			continue
		}
		if n := loc[1] - loc[0]; (n == 40 || n == 64) && !reLabelBefore.MatchString(before) {
			continue
		}
		return true
	}
	return false
}
