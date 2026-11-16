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
// as a whole token, in the set of tokens the run's tool results held. A name
// also counts as held when a held token is that name followed by one or two
// generated segments, the owner prefix of a pod, job or ReplicaSet name.
//
// The check is a flag, not a score: it favors a missed invention over a false
// accusation, so a token counts as a name only when it is shaped like one.
package evidencenames

import (
	"encoding/json"
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

	listItem  = regexp.MustCompile(`^\s*(?:[-*+]|[0-9]+[.)])\s+(.*)$`)
	tableCell = regexp.MustCompile(`^\s*\|\s*([^|]*?)\s*\|`)
	codeSpan  = regexp.MustCompile("`([^`\n]+)`")
	boldSpan  = regexp.MustCompile(`\*\*([^*\n]+)\*\*|__([^_\n]+)__`)
	kindWord  = regexp.MustCompile(`(?i)\b(?:` + kindWords + `)\s+(?:named\s+|called\s+)?` +
		"[`*\"']*" + `([A-Za-z0-9._/-]+)`)
	// unambiguousState is a pod or node state, in kubectl's spelling, that is not an
	// English word.
	unambiguousState = regexp.MustCompile(`\b(?:ImagePullBackOff|ErrImagePull|CrashLoopBackOff|OOMKilled|` +
		`CreateContainerConfigError|NotReady|Unschedulable)\b`)
	// statusState is Pending, Terminating or Evicted where the text gives a status: right
	// after a colon that follows the name, after "status:", or as a table cell. In prose
	// ("the fix is pending review", "keep terminating gracefully") they are English.
	statusState = regexp.MustCompile(`^[*_)\]]*\s*:\s*(?:Pending|Terminating|Evicted)\b|` +
		`(?i:\bstatus)\s*[:=]\s*(?:Pending|Terminating|Evicted)\b|\|\s*(?:Pending|Terminating|Evicted)\s*\|`)
	clauseEnd  = regexp.MustCompile(`;|[.!?](?:\s|$)|\s\*\*`)
	leadMarkup = regexp.MustCompile("^[`*_\"'(\\[]+")
	pairSplit  = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)/([a-z0-9][a-z0-9-]*)$`)
)

// wellKnown are names every cluster has or words that are shaped like a name
// and are not one. A name here is never reported.
var wellKnown = map[string]bool{
	"kube-system": true, "kube-public": true, "kube-node-lease": true, "control-plane": true,
	"ephemeral-storage": true, "read-only": true, "built-in": true, "multi-agent": true,
	"non-zero": true, "up-to-date": true, "k8s": true, "arm64": true, "amd64": true, "x86": true,
	"ipv4": true, "ipv6": true, "http2": true, "utf8": true, "oom-killed": true, "crash-loop": true,
	"x-yaml": true, "x-www-form-urlencoded": true, "x86-64": true, "out-of-memory": true,
	"high-memory": true, "in-flight": true, "cluster-wide": true, "read-write": true,
	"read-heavy": true, "write-heavy": true, "non-root": true, "per-node": true, "per-pod": true,
	"multi-node": true, "single-node": true, "long-running": true, "short-lived": true,
	"long-lived": true, "node-level": true, "pod-level": true, "cluster-level": true,
	"memory-bound": true, "cpu-bound": true, "resource-constrained": true, "image-related": true,
	"pre-existing": true, "cross-namespace": true,
}

// Evidence is the set of tokens a run's tool results held.
type Evidence struct {
	tokens    map[string]struct{}
	owners    map[string]struct{} // names that a held token extends with generated segments
	truncated bool
}

// NewEvidence returns an empty evidence set.
func NewEvidence() *Evidence {
	return &Evidence{tokens: map[string]struct{}{}, owners: map[string]struct{}{}}
}

// EvidenceFromTokens rebuilds an evidence set from a stored token list.
func EvidenceFromTokens(tokens []string) *Evidence {
	e := NewEvidence()
	for _, t := range tokens {
		e.tokens[t] = struct{}{}
		e.indexOwner(t)
	}
	return e
}

// Add reads every name-shaped token out of text: each maximal run of name
// characters, lowercased, and its dot-separated and underscore-separated parts
// (a hostname holds its labels, a key holds its words).
func (e *Evidence) Add(text string) {
	for _, run := range runPattern.FindAllString(text, -1) {
		e.addRun(strings.ToLower(strings.Trim(run, "._-")))
	}
}

// AddJSON reads a JSON document as JSON: every key and every string value is
// added as it decodes, so an escape such as \n separates words instead of
// joining its letter to the next word. It returns false, adding nothing, when
// data is not JSON.
func (e *Evidence) AddJSON(data []byte) bool {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return false
	}
	e.addValue(v)
	return true
}

func (e *Evidence) addValue(v any) {
	switch x := v.(type) {
	case string:
		e.Add(x)
	case []any:
		for _, item := range x {
			e.addValue(item)
		}
	case map[string]any:
		for k, item := range x {
			e.Add(k)
			e.addValue(item)
		}
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
	e.indexOwner(tok)
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

// generatedAlphabet is the characters Kubernetes draws a generated name suffix from: no
// vowels and none of 0, 1 and 3, so a suffix never spells a word.
const generatedAlphabet = "bcdfghjklmnpqrstvwxz2456789"

// generatedSegment is true for what a controller appends to its owner's name: digits only (a
// StatefulSet ordinal, a CronJob's scheduled minute), or 5 to 10 characters of the generated
// alphabet (a pod suffix, a ReplicaSet hash).
func generatedSegment(seg string) bool {
	if seg == "" {
		return false
	}
	if strings.Trim(seg, "0123456789") == "" {
		return true
	}
	return len(seg) >= 5 && len(seg) <= 10 && strings.Trim(seg, generatedAlphabet) == ""
}

// indexOwner records the owner name of a held token: the token without its last one or two
// segments when every removed segment is generated, such as "gpu-swap-monitor" for
// "gpu-swap-monitor-29914110-vmnfv".
func (e *Evidence) indexOwner(tok string) {
	segs := strings.Split(tok, "-")
	for cut := 1; cut <= 2 && cut < len(segs); cut++ {
		if !generatedSegment(segs[len(segs)-cut]) {
			return
		}
		e.owners[strings.Join(segs[:len(segs)-cut], "-")] = struct{}{}
	}
}

// HasOwnerOf reports whether a token in the set is tok followed by one or two generated
// segments (see generatedSegment): tok is the owner of a pod, job or ReplicaSet name. A name
// that merely continues ("web-front" in "web-front-end-7d9f"), or a token with no hyphen after
// it ("node-1" in "node-12345"), does not match.
func (e *Evidence) HasOwnerOf(tok string) bool {
	_, ok := e.owners[strings.ToLower(tok)]
	return ok
}

// heldBy reports whether any of the sets holds tok whole, or, with owner, as the owner of a
// held name. A nil set holds nothing.
func heldBy(tok string, owner bool, sets ...*Evidence) bool {
	for _, set := range sets {
		if set != nil && (set.Has(tok) || (owner && set.HasOwnerOf(tok))) {
			return true
		}
	}
	return false
}

// isLabel is true for a DNS-1123 label that holds a letter.
func isLabel(tok string) bool {
	return len(tok) <= maxLabel && labelPattern.MatchString(tok) && strings.ContainsAny(tok, "abcdefghijklmnopqrstuvwxyz")
}

// nameShaped is true for a label that reads as a resource name rather than a
// word: it holds a letter, is not a version, a size or a well-known term, and
// holds a digit or, in a strong position, a hyphen. English compounds
// ("high-memory", "in-flight") are hyphenated and have no digit, so a weak
// position (a list lead, bold text, the token after a kind word) needs a digit.
func nameShaped(tok string, strong bool) bool {
	if !isLabel(tok) || wellKnown[tok] || versionPattern.MatchString(tok) {
		return false
	}
	if strong && strings.Contains(tok, "-") {
		return true
	}
	return strings.ContainsAny(tok, "0123456789")
}

// candidate is a string in a name position and how strongly the position says
// the string is a name: a strong position (a table's first cell, a code span of
// one token, an ns/name pair of two name-shaped sides) accepts a hyphenated
// name; a weak one needs a digit.
type candidate struct {
	text   string
	strong bool
}

// Claimed returns the distinct names the answer asserts, in order of first
// appearance. known reports whether a bare namespace-like left side of an
// ns/name pair is a real token (so "text/x-yaml" is not read as a pair).
func Claimed(answer string, known func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, cand := range candidates(placeholder.ReplaceAllString(answer, " ")) {
		for _, tok := range splitCandidate(cand, known) {
			if !seen[tok] {
				seen[tok] = true
				out = append(out, tok)
			}
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
	known := func(s string) bool { return heldBy(s, false, ev, ignore) }
	seen := func(s string) bool { return heldBy(s, true, ev, ignore) }
	var out []string
	for _, tok := range Claimed(answer, known) {
		if !seen(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// candidates returns the strings in name positions: list item leads, table
// first cells, bold and code spans, kind-word followers, and bare-token lines
// of a fenced block.
func candidates(answer string) []candidate {
	var out []candidate
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
func bareLine(line string) []candidate {
	t := strings.TrimSpace(line)
	if m := listItem.FindStringSubmatch(t); m != nil {
		t = strings.TrimSpace(m[1])
	}
	if t != "" && !strings.ContainsAny(t, " \t") {
		return []candidate{{text: t}}
	}
	return nil
}

func lineCandidates(line string) []candidate {
	var out []candidate
	if m := listItem.FindStringSubmatch(line); m != nil {
		tok, rest := leadSplit(m[1])
		out = append(out, candidate{text: tok, strong: stateFollows(rest)})
	}
	if m := tableCell.FindStringSubmatch(line); m != nil {
		out = append(out, candidate{text: leadToken(m[1]), strong: true})
	}
	for _, m := range codeSpan.FindAllStringSubmatch(line, -1) {
		out = append(out, candidate{text: strings.TrimSpace(m[1]), strong: true})
	}
	for _, m := range boldSpan.FindAllStringSubmatchIndex(line, -1) {
		out = append(out, candidate{text: strings.TrimSpace(groupText(line, m)), strong: stateFollows(line[m[1]:])})
	}
	for _, m := range kindWord.FindAllStringSubmatchIndex(line, -1) {
		out = append(out, candidate{text: line[m[2]:m[3]], strong: stateFollows(line[m[3]:])})
	}
	return out
}

// groupText is the text of the first capture group of a match that took part.
func groupText(line string, m []int) string {
	for g := 2; g+1 < len(m); g += 2 {
		if m[g] >= 0 {
			return line[m[g]:m[g+1]]
		}
	}
	return ""
}

// stateFollows is true when the clause that continues after a name gives it a
// pod or node state. The state word belongs to the name before it: a clause ends
// at a semicolon or a sentence end.
func stateFollows(rest string) bool {
	if loc := clauseEnd.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	return unambiguousState.MatchString(rest) || statusState.MatchString(rest)
}

// leadSplit is the first token of s after leading markup, cut where a name ends,
// and what follows it.
func leadSplit(s string) (tok, rest string) {
	s = leadMarkup.ReplaceAllString(strings.TrimSpace(s), "")
	if i := strings.IndexAny(s, " \t`*:,;)(\"'|"); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// leadToken is the first token of s after leading markup.
func leadToken(s string) string {
	tok, _ := leadSplit(s)
	return tok
}

// splitCandidate turns one candidate into the name tokens it holds: the token
// itself when its position accepts it, or the sides of an ns/name pair. A pair
// of two name-shaped sides is strong; a pair whose left side is a real token is
// weak, so each side needs a digit.
func splitCandidate(c candidate, known func(string) bool) []string {
	text := strings.Trim(c.text, "._-:,;")
	if text == "" || strings.ContainsAny(text, " \t") {
		return nil
	}
	if m := pairSplit.FindStringSubmatch(text); m != nil {
		strong := nameShaped(m[1], true) && nameShaped(m[2], true)
		if !strong && !known(m[1]) {
			return nil
		}
		return keepNames([]string{m[1], m[2]}, strong)
	}
	if strings.ContainsAny(text, "/.:") {
		return nil
	}
	return keepNames([]string{text}, c.strong)
}

// keepNames returns the tokens that are name-shaped for the position strength.
func keepNames(toks []string, strong bool) []string {
	var out []string
	for _, t := range toks {
		if nameShaped(t, strong) {
			out = append(out, t)
		}
	}
	return out
}
