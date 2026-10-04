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

package fitnessscript

import (
	"math"
	"strings"
	"testing"
)

// kwReflects is the DEFER keyword the reference crews use.
const kwReflects = "REFLECTS"

func TestParseFitnessTest_DiscussionHealth(t *testing.T) {
	content := `DESCRIPTION Verify discussion system is healthy end-to-end
REQUIRES deployed crew with discussion.enabled

DEFINE CONST QUESTION AS "Hello, are you there?"
DEFINE CONST MAX_DURATION AS 90 seconds

# Gateway is reachable
ASSERT(POST to discussion endpoint returns 200 with conversationId)

# SSE stream connects and finds the thread
ASSERT(SSE stream emits "connected" event)
ASSERT(SSE stream emits "thread_found" event within 30 seconds)

# Discussion lifecycle completes
ASSERT(at least 1 "phase" event with status=triaging is emitted)
ASSERT(discussion completes with "done" event within MAX_DURATION)

# Coordinator produces a response
ASSERT(synthesis is non-empty)
`

	ft := ParseFitnessTest(content)

	if ft.Description != "Verify discussion system is healthy end-to-end" {
		t.Errorf("unexpected description: %q", ft.Description)
	}
	expectConst(t, ft, "QUESTION", "Hello, are you there?")
	expectConst(t, ft, "MAX_DURATION", "90 seconds")
	// assertion[3] is a phase event assertion, classified as Custom (no "emits").
	expectKinds(t, ft, KindPostReturns200, KindSseEmits, KindSseEmitsWithin, KindCustom,
		KindCompletesWithin, KindSynthesisNonEmpty)
	if ft.Assertions[1].EventType != evConnected {
		t.Errorf("assertion[1]: expected event connected, got %+v", ft.Assertions[1])
	}
	if a := ft.Assertions[2]; a.EventType != "thread_found" || a.WithinSeconds != 30 {
		t.Errorf("assertion[2]: expected thread_found within 30, got %+v", a)
	}
}

func TestParseFitnessTest_GeneralKnowledge(t *testing.T) {
	content := `DESCRIPTION Verify crew answers general knowledge questions
REQUIRES deployed crew with discussion.enabled

DEFINE CONST QUESTION AS "What are the three states of matter?"
DEFINE CONST MAX_DURATION AS 120 seconds

ASSERT(discussion completes within MAX_DURATION)
ASSERT(at least 1 specialist contributes with signal=agree)
ASSERT(coordinator produces synthesis)
ASSERT(synthesis CONTAINS reference to "solid" AND "liquid" AND "gas")
ASSERT(synthesis does NOT CONTAIN agent names or signal terminology)
`

	ft := ParseFitnessTest(content)

	expectKinds(t, ft, KindCompletesWithin, KindMinSpecialistAgrees, KindCoordinatorSynthesizes,
		KindSynthesisContains, KindSynthesisNotContains)
	if ft.Assertions[1].AgreeCount != 1 {
		t.Errorf("assertion[1]: expected agree count 1, got %+v", ft.Assertions[1])
	}
	if terms := ft.Assertions[3].Terms; strings.Join(terms, ",") != "solid,liquid,gas" {
		t.Errorf("assertion[3]: unexpected terms %v", terms)
	}
}

func TestParseFitnessTest_StandAside(t *testing.T) {
	content := `DESCRIPTION Verify specialist stands aside on nonsense input
DEFINE CONST QUESTION AS "asdf jkl qwerty"
DEFINE CONST MAX_DURATION AS 60 seconds

ASSERT(discussion completes within MAX_DURATION)
ASSERT(0 specialists contribute with signal=agree)
ASSERT(coordinator produces synthesis)
ASSERT(synthesis is a polite clarification request, NOT a fabricated answer)
`

	ft := ParseFitnessTest(content)

	if len(ft.Assertions) != 4 {
		t.Errorf("expected 4 assertions, got %d", len(ft.Assertions))
	}
	if ft.Assertions[1].Kind != KindMinSpecialistAgrees || ft.Assertions[1].AgreeCount != 0 {
		t.Errorf("assertion[1]: expected KindMinSpecialistAgrees{0}, got %+v", ft.Assertions[1])
	}
	if ft.Assertions[3].Kind != KindCustom {
		t.Errorf("assertion[3]: expected KindCustom, got %v", ft.Assertions[3].Kind)
	}
}

func TestParseFitnessTest_CommentsAndBlanks(t *testing.T) {
	content := `# This is a comment
DESCRIPTION Test with comments

# Another comment
DEFINE CONST QUESTION AS "test"

ASSERT(synthesis is non-empty)
`

	ft := ParseFitnessTest(content)

	if ft.Description != "Test with comments" {
		t.Errorf("description: %q", ft.Description)
	}
	if len(ft.Assertions) != 1 {
		t.Errorf("expected 1 assertion, got %d", len(ft.Assertions))
	}
}

func TestExtractFirstQuoted(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{`SSE stream emits "connected" event`, evConnected},
		{`synthesis CONTAINS "solid"`, termSolid},
		{"no quotes here", ""},
	}
	for _, c := range cases {
		got := extractFirstQuoted(c.input)
		if got != c.expected {
			t.Errorf("extractFirstQuoted(%q) = %q, want %q", c.input, got, c.expected)
		}
	}
}

func TestExtractAllQuoted(t *testing.T) {
	input := `synthesis CONTAINS reference to "solid" AND "liquid" AND "gas"`
	got := extractAllQuoted(input)
	if len(got) != 3 || got[0] != termSolid || got[1] != "liquid" || got[2] != "gas" {
		t.Errorf("extractAllQuoted: got %v", got)
	}
}

func TestEvaluateAssertion_PostReturns200(t *testing.T) {
	a := Assertion{Raw: "POST returns 200", Kind: KindPostReturns200}

	pass := evaluateAssertion(a, true, nil, "", false)
	if !pass.Passed {
		t.Error("expected pass when postOK=true")
	}

	fail := evaluateAssertion(a, false, nil, "", false)
	if fail.Passed {
		t.Error("expected fail when postOK=false")
	}
}

func TestEvaluateAssertion_SseEmits(t *testing.T) {
	a := Assertion{Raw: `SSE stream emits "connected" event`, Kind: KindSseEmits, EventType: evConnected}
	events := []SignalEvent{
		{Type: evConnected},
		{Type: "thread_found"},
	}

	pass := evaluateAssertion(a, true, events, "", false)
	if !pass.Passed {
		t.Error("expected pass when event present")
	}

	a2 := Assertion{Raw: `SSE stream emits "done" event`, Kind: KindSseEmits, EventType: evDone}
	fail := evaluateAssertion(a2, true, events, "", false)
	if fail.Passed {
		t.Error("expected fail when event absent")
	}
}

func TestEvaluateAssertion_MinSpecialistAgrees(t *testing.T) {
	a := Assertion{Raw: "at least 1 specialist agrees", Kind: KindMinSpecialistAgrees, AgreeCount: 1}
	events := []SignalEvent{
		{Type: evFinding, Signal: sigAgree, Agent: "specialist-a"},
	}

	pass := evaluateAssertion(a, true, events, "", false)
	if !pass.Passed {
		t.Errorf("expected pass, got: %s", pass.Message)
	}

	// "at least 2" with only 1 agree should fail
	a2 := Assertion{Raw: "at least 2 specialists agree", Kind: KindMinSpecialistAgrees, AgreeCount: 2}
	fail2 := evaluateAssertion(a2, true, events, "", false)
	if fail2.Passed {
		t.Error("expected fail: only 1 agree but need 2")
	}

	// "at least 0" (i.e. 0 count) always passes since count >= 0
	a0 := Assertion{Raw: "0 specialists agree", Kind: KindMinSpecialistAgrees, AgreeCount: 0}
	pass0 := evaluateAssertion(a0, true, events, "", false)
	if !pass0.Passed {
		t.Errorf("expected pass: count=0 means at-least-0 which any event set satisfies: %s", pass0.Message)
	}

	// No events — "at least 1" fails
	failEmpty := evaluateAssertion(a, true, nil, "", false)
	if failEmpty.Passed {
		t.Error("expected fail: no agree signals")
	}
}

func TestEvaluateAssertion_SynthesisContains(t *testing.T) {
	a := Assertion{
		Raw:   `synthesis CONTAINS "solid" AND "liquid"`,
		Kind:  KindSynthesisContains,
		Terms: []string{termSolid, "liquid"},
	}

	synthesis := "Matter exists as solid, liquid, and gas."

	pass := evaluateAssertion(a, true, nil, synthesis, false)
	if !pass.Passed {
		t.Errorf("expected pass, got: %s", pass.Message)
	}

	fail := evaluateAssertion(a, true, nil, "only liquid here", false)
	if fail.Passed {
		t.Error("expected fail: solid not in synthesis")
	}
}

func TestEvaluateAssertion_SynthesisNotContains(t *testing.T) {
	a := Assertion{
		Raw:   `synthesis does NOT CONTAIN "agent"`,
		Kind:  KindSynthesisNotContains,
		Terms: []string{"agent"},
	}

	pass := evaluateAssertion(a, true, nil, "Matter exists as solid, liquid, and gas.", false)
	if !pass.Passed {
		t.Errorf("expected pass, got: %s", pass.Message)
	}

	fail := evaluateAssertion(a, true, nil, "The agent replied with an answer.", false)
	if fail.Passed {
		t.Error("expected fail: prohibited term found")
	}
}

func TestEvaluateAssertion_CompletesWithin(t *testing.T) {
	a := Assertion{Raw: "discussion completes within MAX_DURATION", Kind: KindCompletesWithin}

	events := []SignalEvent{{Type: evDone}}
	pass := evaluateAssertion(a, true, events, "", false)
	if !pass.Passed {
		t.Errorf("expected pass when done event present and not timed out")
	}

	timeout := evaluateAssertion(a, true, events, "", true)
	if timeout.Passed {
		t.Error("expected fail when timed out")
	}

	noEvents := evaluateAssertion(a, true, nil, "", false)
	if noEvents.Passed {
		t.Error("expected fail when no done event")
	}
}

func TestEvaluateAssertion_Custom(t *testing.T) {
	a := Assertion{
		Raw:        "synthesis is a polite clarification request",
		Kind:       KindCustom,
		CustomText: "synthesis is a polite clarification request",
	}
	result := evaluateAssertion(a, true, nil, "some synthesis", false)
	if !result.Passed {
		t.Error("custom assertions should pass with manual review advisory")
	}
	if result.Message != "Custom assertion - manual review recommended" {
		t.Errorf("unexpected message: %q", result.Message)
	}
}

func TestFindSynthesis(t *testing.T) {
	events := []SignalEvent{
		{Type: evSignal, Signal: sigAgree},
		{Type: "synthesis", Content: "Matter exists in three states."},
		{Type: evDone},
	}

	got := FindSynthesis(events)
	if got != "Matter exists in three states." {
		t.Errorf("unexpected synthesis: %q", got)
	}

	// done event with content as fallback
	events2 := []SignalEvent{
		{Type: evSignal, Signal: sigAgree},
		{Type: evDone, Content: "Fallback content."},
	}
	got2 := FindSynthesis(events2)
	if got2 != "Fallback content." {
		t.Errorf("unexpected synthesis from done: %q", got2)
	}

	// no synthesis
	events3 := []SignalEvent{{Type: evSignal}, {Type: evDone}}
	got3 := FindSynthesis(events3)
	if got3 != "" {
		t.Errorf("expected empty synthesis, got %q", got3)
	}
}

func TestClassifyDeferred(t *testing.T) {
	content := `DEFINE CONST QUESTION AS "List namespaces"
ASSERT(synthesis CONTAINS "namespace")
ASSERT(DEFER synthesis REFLECTS "There are 28 namespaces including kube-system, kubemoot, observability.")`
	ft := ParseFitnessTest(content)
	var def *Assertion
	for i := range ft.Assertions {
		if ft.Assertions[i].Kind == KindDeferred {
			def = &ft.Assertions[i]
		}
	}
	if def == nil {
		t.Fatal("expected a KindDeferred assertion")
	}
	if def.Keyword != kwReflects {
		t.Errorf("keyword not extracted: %q", def.Keyword)
	}
	if def.Reference != "There are 28 namespaces including kube-system, kubemoot, observability." {
		t.Errorf("reference not extracted: %q", def.Reference)
	}
	// A DEFER assertion must defer (provisional pass), never fail the run inline.
	r := evaluateAssertion(*def, true, nil, "any answer", false)
	if !r.Passed {
		t.Error("DEFER should pass provisionally (deferred to post-suite judge crew)")
	}
}

func TestMultilineLogicalLines(t *testing.T) {
	// A DEFER assertion whose reference is wrapped across indented lines, plus a
	// wrapped DESCRIPTION, must parse to the same logical statements as single-line.
	content := `DESCRIPTION This description is wrapped
  across two physical lines for readability
DEFINE CONST QUESTION AS "What is up?"
ASSERT(DEFER synthesis REFLECTS
  "Enumerates the cluster's namespaces (discovered, not assumed). Real
   names; does not invent. Count not scored.")
ASSERT(synthesis CONTAINS "namespace")`
	ft := ParseFitnessTest(content)
	if ft.Description != "This description is wrapped across two physical lines for readability" {
		t.Errorf("multiline DESCRIPTION not joined: %q", ft.Description)
	}
	var def *Assertion
	for i := range ft.Assertions {
		if ft.Assertions[i].Kind == KindDeferred {
			def = &ft.Assertions[i]
		}
	}
	if def == nil {
		t.Fatal("expected a KindDeferred assertion from the wrapped ASSERT")
	}
	if def.Keyword != kwReflects {
		t.Errorf("keyword: %q", def.Keyword)
	}
	want := "Enumerates the cluster's namespaces (discovered, not assumed). Real names; does not invent. Count not scored."
	if def.Reference != want {
		t.Errorf("wrapped reference not rejoined:\n got: %q\nwant: %q", def.Reference, want)
	}
	if len(ft.Assertions) != 2 {
		t.Errorf("expected 2 assertions, got %d", len(ft.Assertions))
	}
}

func TestLooksLikeMarkdown(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"adl with ASSERT", "DESCRIPTION x\nASSERT(POST returns 200)", false},
		{"adl with DEFINE", `DEFINE CONST QUESTION AS "q"`, false},
		{"markdown heading", "# A scenario\nWhat is up?\n\n- POST returns 200", true},
		{"markdown fence only", "what is up?\n```reflects\nground truth\n```", true},
		{"plain prose, no markers", "just some text", false},
		{"markdown prose naming ADL words",
			"# Prompts\nWhich module sets the DESCRIPTION line?\n\n- synthesis is non-empty", true},
		{"indented ADL directive", "# comment\n  ASSERT(synthesis is non-empty)", false},
	}
	for _, c := range cases {
		if got := looksLikeMarkdown(c.content); got != c.want {
			t.Errorf("%s: looksLikeMarkdown = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseMarkdownFitnessTest(t *testing.T) {
	content := "# Single-agent — enumerate Helm releases\n" +
		"Which Helm releases are deployed across all namespaces and their versions?\n\n" +
		"- POST to discussion endpoint returns 200 with conversationId\n" +
		"- discussion completes with \"done\" event within 300 seconds\n" +
		"- coordinator produces synthesis\n" +
		"- synthesis CONTAINS \"release\"\n\n" +
		"```reflects\n" +
		"Enumerates the cluster's actual Helm releases across namespaces — native and\n" +
		"Flux-managed. Names the REAL releases; 'none found' is wrong; does not invent.\n" +
		"```\n"
	ft := ParseFitnessTest(content)

	if ft.Description != "Single-agent — enumerate Helm releases" {
		t.Errorf("description = %q", ft.Description)
	}
	expectConst(t, ft, "QUESTION", "Which Helm releases are deployed across all namespaces and their versions?")
	// MAX_DURATION is seeded from the completes-within gate.
	expectConst(t, ft, "MAX_DURATION", "300 seconds")
	// 4 inline gates + 1 deferred = 5.
	expectKinds(t, ft, KindPostReturns200, KindCompletesWithin, KindCoordinatorSynthesizes,
		KindSynthesisContains, KindDeferred)
	want := "Enumerates the cluster's actual Helm releases across namespaces \u2014 native and " +
		"Flux-managed. Names the REAL releases; 'none found' is wrong; does not invent."
	expectCanonicalDefer(t, ft.Assertions[4], kwReflects, want)
}

// expectCanonicalDefer checks a deferred assertion's keyword, collapsed
// reference, and Raw. Raw MUST be the canonical ADL DEFER form (stored
// transcripts carry only Raw; the operator re-parses it to route the post-suite
// score). A bare reference here regresses to "no DEFER assertions found".
func expectCanonicalDefer(t *testing.T, def Assertion, keyword, reference string) {
	t.Helper()
	if def.Keyword != keyword {
		t.Errorf("deferred keyword = %q, want %s (fence info string, uppercased)", def.Keyword, keyword)
	}
	if def.Reference != reference {
		t.Errorf("deferred reference not collapsed:\n got: %q\nwant: %q", def.Reference, reference)
	}
	if wantRaw := `DEFER synthesis ` + keyword + ` "` + reference + `"`; def.Raw != wantRaw {
		t.Errorf("deferred Raw not canonical:\n got: %q\nwant: %q", def.Raw, wantRaw)
	}
}

func TestMarkdownExplicitQuestionLabel(t *testing.T) {
	content := "# Scenario\n**Question:** What namespaces exist?\n\n- synthesis is non-empty\n"
	ft := ParseFitnessTest(content)
	if q := ft.Constants["QUESTION"]; q != "What namespaces exist?" {
		t.Errorf("labelled question = %q", q)
	}
}

// TestContinuesStatement pins the line-continuation predicate extracted from
// logicalLines: a statement continues when its buffer has an unbalanced
// quote/paren, OR the next physical line is indented and non-empty.
func TestContinuesStatement(t *testing.T) {
	cases := []struct {
		name string
		buf  string
		raw  string
		want bool
	}{
		{"balanced + non-indented = new statement", `ASSERT(POST returns 200)`, `ASSERT(synthesis)`, false},
		{"balanced + indented = continuation", `DESCRIPTION wrapped`, `  across lines`, true},
		{"unbalanced paren = continuation", `ASSERT(DEFER synthesis REFLECTS`, `more text`, true},
		{"unbalanced quote = continuation", `ASSERT(x "open`, `still open"`, true},
		{"balanced + blank line = new statement", `ASSERT(x)`, ``, false},
		{"balanced + tab-indented = continuation", `DESCRIPTION wrapped`, "\tafter tab", true},
	}
	for _, c := range cases {
		if got := continuesStatement(c.buf, c.raw, strings.TrimSpace(c.raw)); got != c.want {
			t.Errorf("%s: continuesStatement(%q,%q) = %v, want %v", c.name, c.buf, c.raw, got, c.want)
		}
	}
}

// evaluateAssertion is the positional form the tests use to build a RunState.
func evaluateAssertion(a Assertion, postOK bool, events []SignalEvent, synthesis string,
	timedOut bool) AssertionResult {
	return EvaluateAssertion(a, RunState{PostOK: postOK, Events: events, Synthesis: synthesis, TimedOut: timedOut})
}

// expectConst checks one parsed DEFINE CONST value.
func expectConst(t *testing.T, ft FitnessTest, name, want string) {
	t.Helper()
	if got := ft.Constants[name]; got != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

// expectKinds checks the assertion count and each assertion's kind, in order.
func expectKinds(t *testing.T, ft FitnessTest, kinds ...AssertionKind) {
	t.Helper()
	if len(ft.Assertions) != len(kinds) {
		t.Fatalf("expected %d assertions, got %d: %+v", len(kinds), len(ft.Assertions), ft.Assertions)
	}
	for i, k := range kinds {
		if ft.Assertions[i].Kind != k {
			t.Errorf("assertion[%d]: kind %v, want %v", i, ft.Assertions[i].Kind, k)
		}
	}
}

func TestParseDefer(t *testing.T) {
	cases := []struct {
		raw, keyword, reference string
		ok                      bool
	}{
		{`DEFER synthesis REFLECTS "28 namespaces"`, kwReflects, "28 namespaces", true},
		{`defer Synthesis reflects "x"`, kwReflects, "x", true},
		{`DEFER synthesis REFLECTS "names the "kubemoot" namespace"`, kwReflects, `names the "kubemoot" namespace`, true},
		{`DEFER synthesis REFLECTS "a" and "b"`, kwReflects, `a" and "b`, true},
		{`DEFER synthesis REFLECTS ""`, "", "", false},
		{`DEFER synthesis REFLECTS`, "", "", false},
		{`DEFER synthesis "no keyword"`, "", "", false},
		{`synthesis CONTAINS "x"`, "", "", false},
	}
	for _, c := range cases {
		kw, ref, ok := ParseDefer(c.raw)
		if kw != c.keyword || ref != c.reference || ok != c.ok {
			t.Errorf("ParseDefer(%q) = (%q, %q, %v), want (%q, %q, %v)", c.raw, kw, ref, ok, c.keyword, c.reference, c.ok)
		}
	}
}

// TestMarkdownReferenceVerbatim pins that a fenced reference reaches Raw, the
// text the judge reads, exactly as written: no escaping of quotes or backslashes.
func TestMarkdownReferenceVerbatim(t *testing.T) {
	ft := ParseFitnessTest("# Paths\nq\n\n```reflects\nthe \"kubemoot\" namespace, not C:\\temp\n```\n")
	want := `the "kubemoot" namespace, not C:\temp`
	expectKinds(t, ft, KindDeferred)
	expectCanonicalDefer(t, ft.Assertions[0], kwReflects, want)
}

func TestMarkdownFenceKeyword(t *testing.T) {
	cases := []struct {
		fence, keyword string
	}{
		{"```reflects extra words", kwReflects},
		{"```grounded-v2", "GROUNDED-V2"},
		{"```", ""},
		{"```\"quoted\"", ""},
		{"```synthesis", ""},
	}
	for _, c := range cases {
		ft := ParseFitnessTest("# K\nq\n" + c.fence + "\nref\n```\n")
		if c.keyword == "" {
			if len(ft.Assertions) != 0 {
				t.Errorf("%s: declared %+v, want nothing", c.fence, ft.Assertions)
			}
			continue
		}
		expectKinds(t, ft, KindDeferred)
		expectCanonicalDefer(t, ft.Assertions[0], c.keyword, "ref")
	}
}

func TestExtractFirstIntSaturates(t *testing.T) {
	if got := extractFirstInt("at least 99999999999999999999 specialists agree"); got != math.MaxInt {
		t.Errorf("oversized bound = %d, want math.MaxInt", got)
	}
	if got := extractFirstInt("within 300 seconds"); got != 300 {
		t.Errorf("bound = %d, want 300", got)
	}
}
