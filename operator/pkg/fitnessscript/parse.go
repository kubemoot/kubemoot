/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package fitnessscript parses fitness scenarios (ADL or prose Markdown) and
// evaluates their deterministic assertions against a finished run. It is the one
// assertion engine shared by the fitness runner (live runs) and the operator
// (re-judging a stored transcript), so both agree on what every ASSERT means.
package fitnessscript

import (
	"bufio"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// AssertionKind classifies a parsed ASSERT(...) line
type AssertionKind int

const (
	KindPostReturns200 AssertionKind = iota
	KindSseEmits
	KindSseEmitsWithin
	KindCompletesWithin
	KindMinSpecialistAgrees
	KindCoordinatorSynthesizes
	KindSynthesisContains
	KindSynthesisNotContains
	KindSynthesisNonEmpty
	// KindSynthesisMatchCount — synthesis matches "<regex>" at least N times. A
	// drift-tolerant numeric-bound fact check: a correct full listing has many
	// matching entries, so a gross under-report (e.g. 4 of 141 deployments) falls
	// below the bound while a few added/removed entries do not.
	KindSynthesisMatchCount
	KindDeferred
	KindCustom
)

// Assertion is a parsed ASSERT(...) directive from an ADL file
type Assertion struct {
	Raw string

	Kind AssertionKind

	// KindSseEmits / KindSseEmitsWithin
	EventType string

	// KindSseEmitsWithin
	WithinSeconds int

	// KindMinSpecialistAgrees
	AgreeCount int

	// KindSynthesisContains / KindSynthesisNotContains
	Terms []string

	// KindSynthesisMatchCount — Pattern is a regex, MinCount the inclusive lower
	// bound on the number of matches in the synthesis.
	Pattern  string
	MinCount int

	// KindCustom
	CustomText string

	// KindDeferred — an extension keyword resolved post-suite to the crew that
	// declares kubemoot.ai/adl-keyword=<Keyword>, and the quoted reference passed
	// to that crew (e.g. the ground truth for REFLECTS).
	Keyword   string
	Reference string
}

// FitnessTest is the parsed representation of an ADL test file
type FitnessTest struct {
	Description string
	Constants   map[string]string
	Assertions  []Assertion
}

// ParseFitnessTest parses a fitness scenario into a FitnessTest. A scenario may
// be declared in ADL (.adl) or in prose Markdown (.md); the form is detected
// from the content. Both forms produce the SAME FitnessTest — identical question,
// inline gates, and DEFER/REFLECT deferred assertions — so the runner and the
// grade never depend on which form a crew author chose. ADL is the stronger,
// preferred form; Markdown is the gentler on-ramp.
func ParseFitnessTest(content string) FitnessTest {
	if looksLikeMarkdown(content) {
		return parseMarkdownFitnessTest(content)
	}
	return parseADLFitnessTest(content)
}

// looksLikeMarkdown decides whether a scenario is prose Markdown rather than ADL.
// A line that starts with an ADL directive (ASSERT(, DEFINE CONST, DESCRIPTION)
// is definitive: ADL. Absent those, an ATX heading or a fenced block marks
// Markdown. The directives count only at the start of a line, so Markdown prose
// that mentions one is still Markdown. Default is ADL (the original form).
func looksLikeMarkdown(content string) bool {
	markdown := false
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if isADLDirective(t) {
			return false
		}
		markdown = markdown || strings.HasPrefix(t, "# ") || strings.HasPrefix(t, "```")
	}
	return markdown
}

// isADLDirective reports whether a trimmed line opens an ADL directive.
func isADLDirective(t string) bool {
	return strings.HasPrefix(t, "ASSERT(") || strings.HasPrefix(t, "DEFINE CONST") || strings.HasPrefix(t, "DESCRIPTION ")
}

// parseADLFitnessTest parses ADL content into a FitnessTest.
// Lines starting with '#' are comments. Blank lines are ignored.
// Recognised directives:
//
//	DESCRIPTION <text>
//	DEFINE CONST <NAME> AS "<value>"
//	ASSERT(<text>)
func parseADLFitnessTest(content string) FitnessTest {
	ft := FitnessTest{
		Constants: make(map[string]string),
	}

	for _, line := range logicalLines(content) {
		if rest, ok := cutPrefix(line, "DESCRIPTION "); ok {
			ft.Description = strings.TrimSpace(rest)
			continue
		}

		if rest, ok := cutPrefix(line, "DEFINE CONST "); ok {
			// DEFINE CONST NAME AS "value"
			if name, value, found := strings.Cut(rest, " AS "); found {
				name = strings.TrimSpace(name)
				value = strings.TrimSpace(value)
				value = strings.Trim(value, `"`)
				ft.Constants[name] = value
			}
			continue
		}

		if rest, ok := cutPrefix(line, "ASSERT("); ok {
			// Strip trailing ')'
			inner := strings.TrimRight(rest, ")")
			inner = strings.TrimSpace(inner)
			a := classifyAssertion(inner)
			ft.Assertions = append(ft.Assertions, a)
			continue
		}
		// All other directives (REQUIRES, WHEN, etc.) are silently ignored by the runner
	}

	return ft
}

// parseMarkdownFitnessTest parses a prose Markdown scenario into a FitnessTest.
// The convention mirrors ADL one-for-one:
//
//	# <description>                         → DESCRIPTION
//	<first paragraph>  (or "Question: ...") → DEFINE CONST QUESTION
//	- <gate>                                → ASSERT(<gate>)  (inline assertion)
//	```<keyword>                            → ASSERT(DEFER synthesis <KEYWORD> "...")
//	<reference, possibly multi-line>           the fenced block body is the
//	```                                        reference; the info string is the
//	                                           deferred keyword (e.g. ```reflects
//	                                           → REFLECTS, resolved post-suite to
//	                                           the crew labelled adl-keyword=REFLECTS).
//
// Gate list items are classified by the SAME classifyAssertion the ADL path uses,
// so "completes within 300 seconds", "synthesis CONTAINS \"x\"", etc. behave
// identically. A completes-within gate also seeds MAX_DURATION so the runner's
// wall-clock deadline matches.
func parseMarkdownFitnessTest(content string) FitnessTest {
	p := &markdownParser{ft: FitnessTest{Constants: make(map[string]string)}}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.consumeLine(scanner.Text())
	}

	p.finalize()
	return p.ft
}

// markdownParser holds the mutable state threaded through the line-by-line scan
// of a prose Markdown scenario. Splitting parseMarkdownFitnessTest's loop body
// across small methods keeps each step simple while preserving the exact order
// of side effects.
type markdownParser struct {
	ft            FitnessTest
	questionLines []string
	gotQuestion   bool
	inFence       bool
	fenceKeyword  string
	fenceLines    []string
}

// consumeLine processes one physical line, dispatching to the fence handler when
// inside a fenced block or to the structural-line handler otherwise.
func (p *markdownParser) consumeLine(raw string) {
	t := strings.TrimSpace(raw)
	if p.inFence {
		p.consumeFenceLine(raw, t)
		return
	}
	if strings.HasPrefix(t, "```") {
		p.openFence(t)
		return
	}
	p.consumeContentLine(t)
}

// openFence begins a fenced block, recording its info-string keyword.
func (p *markdownParser) openFence(t string) {
	p.inFence = true
	p.fenceKeyword = strings.TrimSpace(strings.TrimPrefix(t, "```"))
	p.fenceLines = nil
}

// consumeFenceLine collects body lines until the closing fence, then emits the
// deferred assertion.
func (p *markdownParser) consumeFenceLine(raw, t string) {
	if strings.HasPrefix(t, "```") {
		p.closeFence()
		return
	}
	p.fenceLines = append(p.fenceLines, raw)
}

// closeFence emits the deferred (DEFER synthesis) assertion for the just-ended
// fenced block and resets the fence state. The keyword is the first word of the
// info string; a block whose info string is not a keyword declares nothing.
func (p *markdownParser) closeFence() {
	fields := strings.Fields(p.fenceKeyword)
	if len(fields) > 0 && validDeferKeyword(fields[0]) {
		// Raw MUST be the canonical ADL DEFER form: the stored transcript carries
		// only Raw (not Keyword/Reference), and the operator's post-suite judge
		// re-parses Raw to route the score to the keyword's crew. Classifying the
		// formatted Raw keeps the fields exactly what that re-parse yields, so the
		// .md and .adl forms store the same.
		ref := collapseSpaces(strings.Join(p.fenceLines, " "))
		raw := FormatDefer(strings.ToUpper(fields[0]), ref)
		p.ft.Assertions = append(p.ft.Assertions, classifyAssertion(raw))
	}
	p.inFence, p.fenceKeyword, p.fenceLines = false, "", nil
}

// consumeContentLine handles a non-fence line: headings, list items (inline
// assertions), an explicit question label, blank lines, and body paragraphs.
func (p *markdownParser) consumeContentLine(t string) {
	if rest, ok := cutPrefix(t, "# "); ok {
		if p.ft.Description == "" {
			p.ft.Description = strings.TrimSpace(rest)
		}
		p.gotQuestion = p.gotQuestion || len(p.questionLines) > 0
		return
	}
	if strings.HasPrefix(t, "#") { // sub-headings are structural, not content
		return
	}

	if item, ok := markdownListItem(t); ok {
		p.appendListItem(item)
		return
	}

	if rest, ok := cutQuestionLabel(t); ok {
		p.ft.Constants["QUESTION"] = strings.TrimSpace(rest)
		p.gotQuestion = true
		return
	}

	if t == "" {
		if len(p.questionLines) > 0 {
			p.gotQuestion = true
		}
		return
	}
	// First body paragraph (after the H1, before any list) is the question.
	if !p.gotQuestion {
		p.questionLines = append(p.questionLines, t)
	}
}

// appendListItem classifies a list item as an inline assertion and seeds
// MAX_DURATION when it is a completes-within gate.
func (p *markdownParser) appendListItem(item string) {
	a := classifyAssertion(item)
	p.ft.Assertions = append(p.ft.Assertions, a)
	if a.Kind == KindCompletesWithin {
		if secs := extractFirstInt(strings.ToLower(item)); secs > 0 {
			p.ft.Constants["MAX_DURATION"] = fmt.Sprintf("%d seconds", secs)
		}
	}
}

// finalize fills QUESTION from the accumulated body paragraph when no explicit
// label set it.
func (p *markdownParser) finalize() {
	if _, ok := p.ft.Constants["QUESTION"]; !ok && len(p.questionLines) > 0 {
		p.ft.Constants["QUESTION"] = collapseSpaces(strings.Join(p.questionLines, " "))
	}
}

// markdownListItem returns the text of a "- " / "* " / "+ " list item, false
// otherwise. The marker and a trailing checkbox ("- [ ] ") are stripped.
func markdownListItem(t string) (string, bool) {
	for _, m := range []string{"- ", "* ", "+ "} {
		if rest, ok := cutPrefix(t, m); ok {
			rest = strings.TrimSpace(rest)
			if r2, ok := cutPrefix(rest, "[ ] "); ok {
				rest = r2
			} else if r2, ok := cutPrefix(rest, "[x] "); ok {
				rest = r2
			}
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// cutQuestionLabel pulls the question off an explicit "Question:" / "**Question:**"
// line. Returns false when the line carries no such label.
func cutQuestionLabel(t string) (string, bool) {
	for _, p := range []string{"**Question:**", "Question:", "**Question**:"} {
		if rest, ok := cutPrefix(t, p); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

// collapseSpaces trims and collapses internal whitespace runs to single spaces,
// so a multi-line fenced reference becomes one clean string like its ADL twin.
func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// logicalLines folds physical lines into logical ADL statements so any directive
// can wrap across multiple lines for readability. A statement continues while it
// has an unbalanced quote or paren (e.g. a long ASSERT(... "wrapped reference"))
// OR the next physical line is indented (a plain wrapped DESCRIPTION/etc.).
// Continuations are joined with a single space; blank/comment lines between
// statements are dropped.
func logicalLines(content string) []string {
	var out []string
	var buf strings.Builder
	flush := func() {
		if s := strings.TrimSpace(buf.String()); s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		raw := scanner.Text()
		t := strings.TrimSpace(raw)
		if buf.Len() == 0 {
			startStatement(&buf, t)
			continue
		}
		if continuesStatement(buf.String(), raw, t) {
			buf.WriteString(" ")
			buf.WriteString(t)
			continue
		}
		// Current statement is complete; this line begins the next (or is blank).
		flush()
		startStatement(&buf, t)
	}
	flush()
	return out
}

// startStatement begins a new logical statement in buf with t, unless t is a
// blank or comment line (which is skipped). Pre: buf is empty.
func startStatement(buf *strings.Builder, t string) {
	if t == "" || strings.HasPrefix(t, "#") {
		return
	}
	buf.WriteString(t)
}

// continuesStatement reports whether the physical line (raw, with t its trimmed
// form) continues the statement already in buf: either buf has an unbalanced
// quote/paren, or the physical line is indented and non-empty.
func continuesStatement(buf, raw, t string) bool {
	indented := len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') && t != ""
	return !balanced(buf) || indented
}

// balanced reports whether a partial statement has all quotes and parens closed.
// Parens are counted only outside double-quoted spans.
func balanced(s string) bool {
	inQuote := false
	depth := 0
	for _, r := range s {
		switch r {
		case '"':
			inQuote = !inQuote
		case '(':
			if !inQuote {
				depth++
			}
		case ')':
			if !inQuote {
				depth--
			}
		}
	}
	return !inQuote && depth <= 0
}

// cutPrefix is strings.CutPrefix (Go 1.20+) inlined for compatibility
func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// assertionClassifier inspects an assertion's original text and its lowercased
// form, returning a recognised Assertion and true, or false to fall through.
type assertionClassifier func(text, lower string) (Assertion, bool)

// classifyAssertion routes an assertion's text through the ordered list of
// classifiers, returning the first match. DEFER is checked first so it never
// matches a built-in; the custom fallback is last.
// assertionClassifiers is the ordered classifier chain, package-level so it is not
// reallocated on every classifyAssertion call (a hot path during parsing).
var assertionClassifiers = []assertionClassifier{
	classifyDefer,
	classifyPostReturns200,
	classifyCompletesWithin,
	classifySseEmit,
	classifyMinSpecialistAgrees,
	classifyCoordinatorSynthesizes,
	classifySynthesis,
}

func classifyAssertion(text string) Assertion {
	lower := strings.ToLower(text)
	for _, c := range assertionClassifiers {
		if a, ok := c(text, lower); ok {
			return a
		}
	}

	// Fallback — custom / manual review
	return Assertion{Raw: text, Kind: KindCustom, CustomText: text}
}

// classifyDefer matches a DEFER synthesis <KEYWORD> "<reference>" assertion - a
// deferred (post-suite) assertion not evaluated inline; the keyword resolves to
// a judge crew after the suite (kubemoot.ai/adl-keyword). A malformed DEFER is
// still deferred, with no keyword or reference.
func classifyDefer(text, _ string) (Assertion, bool) {
	if !isDefer(text) {
		return Assertion{}, false
	}
	kw, ref, _ := ParseDefer(text)
	return Assertion{Raw: text, Kind: KindDeferred, Keyword: kw, Reference: ref}, true
}

// classifyPostReturns200 matches a "POST ... returns 200" assertion.
func classifyPostReturns200(text, lower string) (Assertion, bool) {
	if strings.Contains(lower, "post") && strings.Contains(lower, "returns 200") {
		return Assertion{Raw: text, Kind: KindPostReturns200}, true
	}
	return Assertion{}, false
}

// classifyCompletesWithin matches a "completes within MAX_DURATION / N seconds"
// assertion.
func classifyCompletesWithin(text, lower string) (Assertion, bool) {
	if strings.Contains(lower, "completes") && strings.Contains(lower, "within") {
		return Assertion{Raw: text, Kind: KindCompletesWithin}, true
	}
	return Assertion{}, false
}

// classifyMinSpecialistAgrees matches "N specialist(s) ... agree" (also handles
// "0 specialists").
func classifyMinSpecialistAgrees(text, lower string) (Assertion, bool) {
	if strings.Contains(lower, "specialist") && strings.Contains(lower, "agree") {
		count := extractFirstInt(lower)
		return Assertion{Raw: text, Kind: KindMinSpecialistAgrees, AgreeCount: count}, true
	}
	return Assertion{}, false
}

// classifyCoordinatorSynthesizes matches a "coordinator produces synthesis"
// assertion.
func classifyCoordinatorSynthesizes(text, lower string) (Assertion, bool) {
	if strings.Contains(lower, "coordinator") && strings.Contains(lower, "synthesis") {
		return Assertion{Raw: text, Kind: KindCoordinatorSynthesizes}, true
	}
	return Assertion{}, false
}

// isDefer reports whether an assertion's text is a DEFER assertion.
func isDefer(text string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(text)), "DEFER")
}

// ParseDefer pulls the KEYWORD and REFERENCE out of a deferred assertion's raw
// text, `DEFER synthesis <KEYWORD> "<reference>"`. The reference runs from the
// first double quote to the last, so it may itself contain quotes; nothing is
// escaped. KEYWORD is the first token before the reference that is neither DEFER
// nor the subject, uppercased. ok is false when the text is not a DEFER assertion
// or lacks a keyword or a non-empty reference. It is the one parser of this form:
// the runner, the re-judge, and the post-suite judge all read Raw through it.
func ParseDefer(raw string) (keyword, reference string, ok bool) {
	t := strings.TrimSpace(raw)
	if !isDefer(t) {
		return "", "", false
	}
	q := strings.Index(t, `"`)
	if q < 0 {
		return "", "", false
	}
	rest := t[q+1:]
	end := strings.LastIndex(rest, `"`)
	if end <= 0 {
		return "", "", false
	}
	for _, f := range strings.Fields(t[:q]) {
		if strings.EqualFold(f, "DEFER") || strings.EqualFold(f, "synthesis") {
			continue
		}
		return strings.ToUpper(f), rest[:end], true
	}
	return "", "", false
}

// FormatDefer renders the canonical deferred assertion ParseDefer reads:
// `DEFER synthesis <KEYWORD> "<reference>"`, the reference written verbatim.
func FormatDefer(keyword, reference string) string {
	return "DEFER synthesis " + keyword + ` "` + reference + `"`
}

// deferKeywordPattern is a Kubernetes label value: a DEFER keyword resolves to the
// crew labelled kubemoot.ai/adl-keyword=<KEYWORD>, so no other keyword can route.
var deferKeywordPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,61}[A-Za-z0-9])?$`)

// validDeferKeyword reports whether s can name a DEFER keyword: a label value
// that is not one of the form's own words.
func validDeferKeyword(s string) bool {
	return deferKeywordPattern.MatchString(s) && !strings.EqualFold(s, "DEFER") && !strings.EqualFold(s, "synthesis")
}

// classifySseEmit checks if the assertion is an SSE emit or emit-within variant.
func classifySseEmit(text, lower string) (Assertion, bool) {
	emitsEvent := strings.Contains(lower, "event") && strings.Contains(lower, "emits")
	if !strings.Contains(lower, "sse stream emits") && !emitsEvent {
		return Assertion{}, false
	}
	eventType := extractFirstQuoted(text)
	if strings.Contains(lower, "within") {
		secs := extractFirstInt(lower)
		if secs == 0 {
			secs = 30
		}
		return Assertion{Raw: text, Kind: KindSseEmitsWithin, EventType: eventType, WithinSeconds: secs}, true
	}
	return Assertion{Raw: text, Kind: KindSseEmits, EventType: eventType}, true
}

// classifySynthesis checks if the assertion is a synthesis-related kind.
func classifySynthesis(text, lower string) (Assertion, bool) {
	if !strings.Contains(lower, "synthesis") {
		return Assertion{}, false
	}

	// synthesis is non-empty
	if strings.Contains(lower, "non-empty") {
		return Assertion{Raw: text, Kind: KindSynthesisNonEmpty}, true
	}

	// synthesis matches "<regex>" at least N times — a numeric-bound count check.
	if strings.Contains(lower, "matches") && strings.Contains(lower, "times") {
		pattern := extractFirstQuoted(text)
		// Read the bound AFTER the quoted regex so a digit inside the pattern is
		// never mistaken for the count.
		after := text
		if q := strings.LastIndex(text, `"`); q >= 0 {
			after = text[q+1:]
		}
		minCount := extractFirstInt(strings.ToLower(after))
		return Assertion{Raw: text, Kind: KindSynthesisMatchCount, Pattern: pattern, MinCount: minCount}, true
	}

	// synthesis does NOT CONTAIN — must be checked before CONTAINS
	if strings.Contains(lower, "not contain") {
		terms := extractAllQuoted(text)
		return Assertion{Raw: text, Kind: KindSynthesisNotContains, Terms: terms}, true
	}

	// synthesis CONTAINS "term" [AND "term" ...]
	if strings.Contains(lower, "contains") {
		terms := extractAllQuoted(text)
		return Assertion{Raw: text, Kind: KindSynthesisContains, Terms: terms}, true
	}

	return Assertion{}, false
}

// extractFirstQuoted returns the content of the first double-quoted substring in s.
func extractFirstQuoted(s string) string {
	start := strings.Index(s, `"`)
	if start < 0 {
		return ""
	}
	rest := s[start+1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// extractAllQuoted returns all double-quoted substrings in s.
func extractAllQuoted(s string) []string {
	var terms []string
	for {
		start := strings.Index(s, `"`)
		if start < 0 {
			break
		}
		rest := s[start+1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			break
		}
		term := rest[:end]
		if term != "" {
			terms = append(terms, term)
		}
		s = rest[end+1:]
	}
	return terms
}

// extractFirstInt finds the first all-digit token in text and returns it. A
// number too large for an int saturates at math.MaxInt rather than wrapping
// negative, so an oversized bound stays an unreachable bound. Returns 0 if no
// integer is found.
func extractFirstInt(text string) int {
	for _, word := range strings.Fields(text) {
		if !allDigits(word) {
			continue
		}
		n, err := strconv.Atoi(word)
		if err != nil { // all digits, so the only failure is out of range
			return math.MaxInt
		}
		return n
	}
	return 0
}

// allDigits reports whether word is non-empty and only ASCII digits.
func allDigits(word string) bool {
	for i := 0; i < len(word); i++ {
		if word[i] < '0' || word[i] > '9' {
			return false
		}
	}
	return word != ""
}
