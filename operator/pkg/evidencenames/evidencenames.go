/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package evidencenames finds resource names an answer asserts that no tool
// result of the run contained. It is deterministic: the answer's names are read
// from name positions (list items, table first cells, bold, single-token code
// spans, ns/name pairs, a kind word followed by a name) and each is looked up,
// as a whole token, in the set of tokens the run's tool results held.
//
// The check is a flag, not a score: it favors a missed invention over a false
// accusation, so a token counts as a name only when it is shaped like one.
package evidencenames

import (
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxTokens bounds the evidence set one run may hold. A run whose results
	// hold more distinct tokens is reported as truncated and is not checked,
	// because absence from a cut set proves nothing.
	MaxTokens = 50000

	// maxLabel is the longest DNS-1123 label.
	maxLabel = 63
)

const kindWords = `nodes?|pods?|namespaces?|deployments?|statefulsets?|daemonsets?|` +
	`services?|cronjobs?|jobs?|configmaps?|secrets?`

var (
	// runPattern is a maximal run of characters a Kubernetes name is built from.
	runPattern = regexp.MustCompile(`[A-Za-z0-9._-]+`)
	// labelPattern is a DNS-1123 label.
	labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	// versionPattern is a version, a size, a duration or a count: starts with a
	// digit and holds only digits, separators and a short unit.
	versionPattern = regexp.MustCompile(`^v?[0-9][0-9._-]*[a-z]{0,3}$`)
	placeholder    = regexp.MustCompile(`<[^<>\n]*>|\{\{[^{}\n]*\}\}|\$\{?[A-Za-z_][A-Za-z0-9_]*\}?`)
	escapedSpace   = strings.NewReplacer(`\n`, " ", `\t`, " ", `\r`, " ")

	listItem  = regexp.MustCompile(`^\s*(?:[-*+]|[0-9]+[.)])\s+(.*)$`)
	tableCell = regexp.MustCompile(`^\s*\|\s*([^|]*?)\s*\|`)
	codeSpan  = regexp.MustCompile("`([^`\n]+)`")
	boldSpan  = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	kindWord  = regexp.MustCompile(`(?i)\b(?:` + kindWords + `)\s+(?:named\s+|called\s+)?` +
		"[`*\"']*" + `([A-Za-z0-9._/-]+)`)
	leadMarkup = regexp.MustCompile("^[`*_\"'(\\[]+")
	pairSplit  = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)$`)
)

// wellKnown are names every cluster has or words that are shaped like a name
// and are not one. A name here is never reported.
var wellKnown = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true,
	"control-plane": true, "ephemeral-storage": true, "read-only": true,
	"built-in": true, "multi-agent": true, "non-zero": true, "up-to-date": true,
	"k8s": true, "arm64": true, "amd64": true, "x86": true, "ipv4": true,
	"ipv6": true, "http2": true, "utf8": true, "oom-killed": true,
	"crash-loop": true, "x-yaml": true, "x-www-form-urlencoded": true,
}

// Evidence is the set of tokens a run's tool results held.
type Evidence struct {
	tokens    map[string]struct{}
	truncated bool
}

// NewEvidence returns an empty evidence set.
func NewEvidence() *Evidence { return &Evidence{tokens: map[string]struct{}{}} }

// EvidenceFromTokens rebuilds an evidence set from a stored token list.
func EvidenceFromTokens(tokens []string) *Evidence {
	e := NewEvidence()
	for _, t := range tokens {
		e.tokens[t] = struct{}{}
	}
	return e
}

// Add reads every name-shaped token out of text: each maximal run of name
// characters, lowercased, and its dot-separated and underscore-separated parts
// (a hostname holds its labels, a key holds its words).
func (e *Evidence) Add(text string) {
	for _, run := range runPattern.FindAllString(escapedSpace.Replace(text), -1) {
		e.addRun(strings.ToLower(strings.Trim(run, "._-")))
	}
}

func (e *Evidence) addRun(run string) {
	e.put(run)
	for _, part := range strings.FieldsFunc(run, func(r rune) bool { return r == '.' || r == '_' }) {
		e.put(strings.Trim(part, "-"))
	}
}

func (e *Evidence) put(tok string) {
	if !isLabel(tok) {
		return
	}
	if _, ok := e.tokens[tok]; ok {
		return
	}
	if len(e.tokens) >= MaxTokens {
		e.truncated = true
		return
	}
	e.tokens[tok] = struct{}{}
}

// Truncated reports whether the set hit MaxTokens and lost tokens.
func (e *Evidence) Truncated() bool { return e.truncated }

// Tokens returns the set sorted, for storing.
func (e *Evidence) Tokens() []string {
	out := make([]string, 0, len(e.tokens))
	for t := range e.tokens {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Has reports whether the whole token is in the set.
func (e *Evidence) Has(tok string) bool {
	_, ok := e.tokens[strings.ToLower(tok)]
	return ok
}

// isLabel is true for a DNS-1123 label that holds a letter.
func isLabel(tok string) bool {
	return len(tok) <= maxLabel && labelPattern.MatchString(tok) && strings.ContainsAny(tok, "abcdefghijklmnopqrstuvwxyz")
}

// nameShaped is true for a label that reads as a resource name rather than a
// word: it holds a letter and a hyphen or a digit, and is not a version, a
// size or a well-known term.
func nameShaped(tok string) bool {
	if !isLabel(tok) || wellKnown[tok] || versionPattern.MatchString(tok) {
		return false
	}
	return strings.ContainsAny(tok, "-0123456789")
}

// Claimed returns the distinct names the answer asserts, in order of first
// appearance. known reports whether a bare namespace-like left side of an
// ns/name pair is a real token (so "text/x-yaml" is not read as a pair).
func Claimed(answer string, known func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(tok string) {
		if nameShaped(tok) && !seen[tok] {
			seen[tok] = true
			out = append(out, tok)
		}
	}
	for _, cand := range candidates(placeholder.ReplaceAllString(answer, " ")) {
		for _, tok := range splitCandidate(cand, known) {
			add(tok)
		}
	}
	return out
}

// Invented returns the answer's names that are in neither the evidence nor the
// ignore set. ignore holds tokens of the question, the reference, the crew's
// own names and anything else the run is allowed to say.
func Invented(answer string, ev *Evidence, ignore *Evidence) []string {
	if ev == nil {
		return nil
	}
	known := func(s string) bool { return ev.Has(s) || (ignore != nil && ignore.Has(s)) }
	var out []string
	for _, tok := range Claimed(answer, known) {
		if !known(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// candidates returns the strings in name positions: list item leads, table
// first cells, bold and code spans, kind-word followers, and bare-token lines
// of a fenced block.
func candidates(answer string) []string {
	var out []string
	fenced := false
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		if fenced {
			out = append(out, bareLine(line)...)
			continue
		}
		out = append(out, lineCandidates(line)...)
	}
	return out
}

// bareLine returns the line when it is one token (a name per line in a block).
func bareLine(line string) []string {
	t := strings.TrimSpace(line)
	if m := listItem.FindStringSubmatch(t); m != nil {
		t = strings.TrimSpace(m[1])
	}
	if t != "" && !strings.ContainsAny(t, " \t") {
		return []string{t}
	}
	return nil
}

func lineCandidates(line string) []string {
	var out []string
	if m := listItem.FindStringSubmatch(line); m != nil {
		out = append(out, leadToken(m[1]))
	}
	if m := tableCell.FindStringSubmatch(line); m != nil {
		out = append(out, leadToken(m[1]))
	}
	for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	for _, m := range boldSpan.FindAllStringSubmatch(line, -1) {
		out = append(out, strings.TrimSpace(m[1]+m[2]))
	}
	for _, m := range kindWord.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// leadToken is the first token of s after leading markup, cut where a name ends.
func leadToken(s string) string {
	s = leadMarkup.ReplaceAllString(strings.TrimSpace(s), "")
	if i := strings.IndexAny(s, " \t`*:,;)(\"'|"); i >= 0 {
		s = s[:i]
	}
	return s
}

// splitCandidate turns one candidate string into the name tokens it holds: the
// token itself, or both sides of an ns/name pair when the pair reads as one.
func splitCandidate(cand string, known func(string) bool) []string {
	cand = strings.Trim(cand, "._-:,;")
	if cand == "" || strings.ContainsAny(cand, " \t") {
		return nil
	}
	if m := pairSplit.FindStringSubmatch(cand); m != nil {
		if nameShaped(m[1]) || known(m[1]) {
			return []string{m[1], m[2]}
		}
		return nil
	}
	if strings.ContainsAny(cand, "/.:") {
		return nil
	}
	return []string{cand}
}
