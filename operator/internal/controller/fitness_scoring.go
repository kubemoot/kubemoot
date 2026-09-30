/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_scoring.go computes the OBJECTIVE crew-behavior measures that turn a
// fitness run into a graded "report card", so crews can be compared across
// bake-offs by a single 0-100 number plus its component sub-scores.
//
// All measures here are pure computation over the per-iteration transcript —
// zero model calls — so they run inline at report time and work retroactively
// on any already-captured run. Sub-scores are normalized to FIXED references
// (not this crew's own distribution) so the grade is comparable across crews.
//
//	Per-run sub-scores (0-100):
//	  correctness — assertions passed / total
//	  adherence   — signal-protocol hygiene (well-formed findings, synthesis,
//	                clean termination)
//	  efficiency  — wall-clock vs a fixed budget (slower decays as budget/dur)
//
//	Per-scenario sub-scores (0-100):
//	  reliability — binary pass-rate: fraction of the scenario's N runs that
//	                fully passed (a failed or errored run counts as 0). The
//	                unforgiving counterpart to correctness's partial credit.
//	  self_consistency — answer stability across the scenario's N iterations.
//	  v1 is LEXICAL (cosine over synthesis term-frequency vectors); the
//	  embedding-based upgrade rides the later judge/enrichment pass.
//
//	Grade (0-100) = weighted mean of the sub-scores. Weights + the efficiency
//	budget live in the editable Rubric sheet so a reader can re-weight for a
//	different kind of bake-off and the grades recompute.
package controller

import (
	"math"
	"strings"
	"unicode"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// --- Factuality: deterministic, drift-tolerant ground truth -----------------
//
// The grade was structurally unable to fail a confident fabrication: "correctness"
// was the SHAPE-assertion pass-rate, the REFLECTS judge graded "method and honesty,
// not specific values", and the deterministic content checks were authored too
// weakly. The fix is to score the DETERMINISTIC content fact-assertions a scenario
// declares - CONTAINS for required facts (set-membership, e.g. names >=2 real
// databases), NOT CONTAIN for forbidden/stale claims (e.g. "no database",
// "384 GiB", an active-NFS claim) - as drift-tolerant ground truth: bounds and
// set-membership survive normal cluster churn while still catching gross
// fabrication. See [[Measurement Integrity - Grade Inflation and Fabrication]].

// isForbiddenFactAssertion is true for a NOT-CONTAIN content check: the synthesis
// must NOT assert the named (stale/fabricated) claim. A FAILED one is a fabrication.
func isForbiddenFactAssertion(raw string) bool {
	u := strings.ToUpper(raw)
	return strings.Contains(u, "NOT CONTAIN")
}

// isMatchCountFactAssertion is true for a numeric-bound count check
// (synthesis matches "<regex>" at least N times). A required fact: a failing one
// is an under-report, not a fabrication. Guarded against a REFLECTS reference that
// merely contains the words "matches"/"times".
func isMatchCountFactAssertion(raw string) bool {
	u := strings.ToUpper(raw)
	return strings.Contains(u, "MATCHES") && strings.Contains(u, "TIMES") && !strings.Contains(u, "REFLECTS")
}

// isRequiredFactAssertion is true for a CONTAINS content check or a match-count
// bound (a required ground-truth fact) that is not the forbidden (NOT CONTAIN) form.
func isRequiredFactAssertion(raw string) bool {
	if isForbiddenFactAssertion(raw) {
		return false
	}
	return strings.Contains(strings.ToUpper(raw), "CONTAINS") || isMatchCountFactAssertion(raw)
}

// isContentFactAssertion is true for either deterministic content fact check.
// Shape assertions (HTTP 200, thread_found, synthesis non-empty, the consensus
// floor) and the DEFER ... REFLECTS judge are NOT factuality - REFLECTS has no
// CONTAINS token, so it is excluded.
func isContentFactAssertion(raw string) bool {
	return isRequiredFactAssertion(raw) || isForbiddenFactAssertion(raw)
}

// factualityScore is the pass-rate (0-100) over a run's deterministic content
// fact-assertions. A run with NONE returns -1 (unknown) so the aggregator excludes
// it rather than score a phantom 100 - scoring "no facts authored" as perfect is
// the exact inflation being removed.
func factualityScore(asserts []kubemootv1alpha1.AssertionResult) float64 {
	var passed, total int
	for _, a := range asserts {
		if isContentFactAssertion(a.Raw) {
			total++
			if a.Passed {
				passed++
			}
		}
	}
	if total == 0 {
		return -1
	}
	return pct(passed, total)
}

// fabricationTripped is true when a forbidden-claim (NOT CONTAIN) assertion FAILED:
// the synthesis asserted something the scenario forbids - a confident stale or
// fabricated fact. This is the heaviest negative signal and drives the grade
// penalty (penalize, not hard-fail).
func fabricationTripped(asserts []kubemootv1alpha1.AssertionResult) bool {
	for _, a := range asserts {
		if isForbiddenFactAssertion(a.Raw) && !a.Passed {
			return true
		}
	}
	return false
}

// honestFailureMarkers are phrases by which an answer DECLARES it could not get the
// data (NO_DATA / TOOL_GAP and natural-language equivalents). An honest "could not
// retrieve" must outscore a confident wrong answer, so a non-fabricating run that
// declares failure is floored on factuality rather than scored as fully false.
var honestFailureMarkers = []string{
	"no_data", "tool_gap", "could not retrieve", "could not determine",
	"cannot be determined", "unable to determine", "no data was", "not available",
}

func declaresHonestFailure(synthesis string) bool {
	l := strings.ToLower(synthesis)
	for _, m := range honestFailureMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

// honestFactualityFloor is the factuality a non-fabricating run keeps when it
// honestly declares it could not retrieve the data: it made no false claim, so it
// must rank above a confident fabrication (which earns low factuality AND the
// fabrication penalty) even though it missed the required facts.
const honestFactualityFloor = 50.0

// fabricationFloor is the fraction of its grade a fully-fabricating scenario keeps:
// heavy penalty, not an auto-zero (penalize, don't hard-fail, per Jonathan).
const fabricationFloor = 0.35

// fabricationPenalty is the multiplicative grade factor for a scenario, scaling
// linearly with the fraction of its runs that fabricated: 1.0 when none fabricate,
// fabricationFloor when all do.
func fabricationPenalty(fabricatingFraction float64) float64 {
	if fabricatingFraction < 0 {
		fabricatingFraction = 0
	}
	if fabricatingFraction > 1 {
		fabricatingFraction = 1
	}
	return 1 - (1-fabricationFloor)*fabricatingFraction
}

// rubricWeights are the grade weights (each sub-score's share). They need not
// sum to 1 — the grade normalizes by the weight sum — so a reader can zero out
// a measure or emphasize one without rebalancing the rest.
type rubricWeights struct {
	Quality       float64 // reference-grounded REFLECTS judge score (0-100), gated on consensus
	Reliability   float64 // binary pass-rate: fraction of runs that fully passed
	Factuality    float64 // deterministic ground-truth fact-assertion pass-rate (only when authored)
	Participation float64 // did toolers actually engage (agree-depth vs expected)
	Consistency   float64 // answer stability across a scenario's iterations
	Efficiency    float64 // wall-clock vs the efficiency budget
}

// defaultRubricWeights lead with QUALITY — the reference-grounded REFLECTS judge
// score, now GATED ON CONSENSUS (a run where the consensus floor assertion failed,
// or that produced no synthesis, earns 0 quality, so good solo-coordinator text
// can't mask the crew not deliberating). The gate is computed from the stored
// "≥N toolers agree" assertion result, which the runner evaluates live and
// persists — verified present and discriminating in the captured baselines.
//
// PARTICIPATION is a graded measure: the gateway persists per-agent agree
// findings and the operator counts them (agreeCountFromEvents) against the
// scenario's expected N, so it is the TRUE agree count, independent of the
// consensus gate (not a mirror of the floor). It is weighted modestly, funded
// from consistency + efficiency. See [[Persist Consensus Signals in Transcripts]].
//
// Reliability is the unforgiving binary pass-rate; consistency is repeatable
// behavior across iterations (meaningful only N>1); efficiency rates wall-clock vs
// a budget. Correctness/adherence are retired as grade terms (data columns only).
func defaultRubricWeights() rubricWeights {
	// Participation is now a REAL, independent measure (true agree-depth from the
	// persisted per-agent signals), no longer a copy of the consensus floor, so it
	// carries weight. Funded from consistency + efficiency; quality still leads.
	// See [[Persist Consensus Signals in Transcripts]].
	// FACTUALITY (deterministic, drift-tolerant ground truth) is weighted to lead
	// alongside quality WHEN a scenario authors fact assertions; scenarios with none
	// pass factuality=-1 and the term is excluded (the grade normalizes by the
	// weights actually present), so this is INERT until facts are authored and does
	// not change existing grades. Tunable in the editable Rubric sheet.
	// See [[Measurement Integrity - Grade Inflation and Fabrication]].
	return rubricWeights{Quality: 0.45, Reliability: 0.25, Factuality: 0.40, Participation: 0.10, Consistency: 0.10, Efficiency: 0.10}
}

// defaultEfficiencyBudgetMs: a run at/under this wall-clock scores 100 on
// efficiency; slower runs decay as budget/duration (2× budget → 50). Fixed, not
// population-relative, so a fast crew and a slow crew get genuinely different
// efficiency scores instead of both centering on ~50.
const defaultEfficiencyBudgetMs int64 = 60000

// recognizedSignals are the consensus verdicts a well-formed finding may carry.
// `failure` is first-class: a declared failure is well-formed, a silent/empty
// finding is not (see feedback_embrace_failure).
var recognizedSignals = map[string]bool{
	"agree": true, "concern": true, "stand_aside": true, "block": true,
	"advisory": true, "evaluating": true, "triaging": true,
	"proposal": true, "consent": true, "failure": true,
}

// transcriptEvent is the subset of the runner's SSE SignalEvent the scoring
// needs. Parsed from the stored RunOutcome (report_server widens transcriptDoc
// to carry these).
type transcriptEvent struct {
	Type       string `json:"type"`
	Agent      string `json:"agent,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Content    string `json:"content,omitempty"`
	Summary    string `json:"summary,omitempty"` // concern findings carry their text here (agree findings use Content)
	GPU        string `json:"gpu,omitempty"`
	StoodAside bool   `json:"stood_aside,omitempty"`
	Error      string `json:"error,omitempty"`
}

// pct is num/den as a 0-100 percentage; 0 when den is non-positive.
func pct(num, den int) float64 {
	if den <= 0 {
		return 0
	}
	return float64(num) / float64(den) * 100
}

func boolScore(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// correctnessScore is the assertion pass percentage for one run.
func correctnessScore(passed, total int) float64 { return pct(passed, total) }

// efficiencyScore rates one run's wall-clock against a fixed budget: 100 at or
// under budget, decaying as budget/duration past it. 0 when duration is unknown.
func efficiencyScore(durationMs, budgetMs int64) float64 {
	if durationMs <= 0 || budgetMs <= 0 {
		return 0
	}
	if durationMs <= budgetMs {
		return 100
	}
	return float64(budgetMs) / float64(durationMs) * 100
}

// findingWellFormed is true when an agent's finding carries a recognized signal
// (or the stand-aside flag), names its agent, and reports no error.
func findingWellFormed(e transcriptEvent) bool {
	if strings.TrimSpace(e.Agent) == "" || strings.TrimSpace(e.Error) != "" {
		return false
	}
	sig := strings.ToLower(strings.TrimSpace(e.Signal))
	if sig == "" {
		return e.StoodAside // a stood-aside finding may carry the flag, not the word
	}
	return recognizedSignals[sig]
}

// adherenceScore rates signal-protocol hygiene for one run (0-100): the average
// of four components — fraction of findings well-formed, a synthesis was
// produced, the run reached `done`, and no event carried an error. Runs with no
// captured events score 0 (the protocol is unobservable).
func adherenceScore(events []transcriptEvent) float64 {
	if len(events) == 0 {
		return 0
	}
	var findings, wellFormed int
	var hasSynthesis, hasDone, hasError bool
	for _, e := range events {
		switch e.Type {
		case "finding":
			findings++
			if findingWellFormed(e) {
				wellFormed++
			}
		case "synthesis":
			hasSynthesis = hasSynthesis || strings.TrimSpace(e.Content) != ""
		case "done":
			hasDone = true
		}
		hasError = hasError || strings.TrimSpace(e.Error) != ""
	}
	findingsOK := 1.0
	if findings > 0 {
		findingsOK = float64(wellFormed) / float64(findings)
	}
	return (findingsOK + boolScore(hasSynthesis) + boolScore(hasDone) + boolScore(!hasError)) / 4 * 100
}

// agreeCountFromEvents counts `agree` finding events in the transcript. It MUST
// use the same counting as the runner's countAgreeSignals (fitness-runner
// runner.go), because the expected N it is scored against comes from that same
// ">=N toolers agree" floor assertion: if the two drifted (e.g. one counts total
// events and the other distinct agents), participation could penalize a run the
// gate blessed, or vice versa. Both count total agree findings; keep them in sync.
// See [[Persist Consensus Signals in Transcripts]].
func agreeCountFromEvents(events []transcriptEvent) int {
	count := 0
	for _, e := range events {
		if e.Type == "finding" && strings.EqualFold(strings.TrimSpace(e.Signal), "agree") {
			count++
		}
	}
	return count
}

// participationScore rates how well toolers engaged versus the scenario's
// expectation (0-100): agrees over expected, capped at 100. A scenario that
// expects no agreement (expected<=0, e.g. out-of-scope) is not penalized. It is
// fed the TRUE agree count (agreeCountFromEvents), an independent measure
// rather than a copy of the consensus floor.
// See [[Persist Consensus Signals in Transcripts]].
func participationScore(agrees, expected int) float64 {
	if expected <= 0 {
		return 100
	}
	r := float64(agrees) / float64(expected)
	if r > 1 {
		r = 1
	}
	return r * 100
}

// selectivityScore rates how precisely the coordinator woke its subcommittee
// (0-100): of the agents that RESPONDED, the fraction that actually CONTRIBUTED
// (agree/concern) rather than standing aside. A focused, rule-driven selection
// wakes only relevant agents -> ~100; over-waking (many woken agents stand aside)
// drives it down. A run where NO agent was woken (the coordinator answered
// directly, or no toolers) scores 100: nothing irrelevant was woken. This is the
// objective "agents woken vs relevant" measure - ADL's selective-subcommittee
// claim, computed purely from the signal stream. Data column, not a grade term
// (the grade stays quality-led). See [[ADL Bake-Off Measurement Rubric]].
func selectivityScore(events []transcriptEvent) float64 {
	contributing := map[string]bool{}
	stoodAside := map[string]bool{}
	for _, e := range events {
		if e.Type != "finding" || e.Agent == "" {
			continue
		}
		sig := strings.ToLower(strings.TrimSpace(e.Signal))
		switch {
		case e.StoodAside || sig == "stand_aside":
			stoodAside[e.Agent] = true
		case sig == "agree" || sig == "concern":
			contributing[e.Agent] = true
		}
	}
	woken := len(contributing)
	for a := range stoodAside {
		if !contributing[a] { // an agent that also contributed counts as contributing
			woken++
		}
	}
	if woken == 0 {
		return 100 // nothing irrelevant woken (incl. the answer-directly path)
	}
	return float64(len(contributing)) / float64(woken) * 100
}

// extractLeadingInt returns the first run of digits in s as an int, or 0 — used to
// read the expected agree count from an assertion like "at least 2 toolers…".
func extractLeadingInt(s string) int {
	n, seen := 0, false
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
			seen = true
		} else if seen {
			break
		}
	}
	return n
}

// synthesisFromEvents returns the first non-empty synthesis content (or a done
// event's content as fallback) — the crew's answer text, for consistency now
// and the LLM judge later.
func synthesisFromEvents(events []transcriptEvent) string {
	for _, e := range events {
		if e.Type == "synthesis" && strings.TrimSpace(e.Content) != "" {
			return e.Content
		}
	}
	for _, e := range events {
		if e.Type == "done" && strings.TrimSpace(e.Content) != "" {
			return e.Content
		}
	}
	return ""
}

// lexicalSelfConsistency is the mean pairwise cosine similarity (0-100) over the
// term-frequency vectors of a scenario's synthesis texts — a v1 proxy for answer
// stability across iterations. Fewer than two usable texts → 100 (nothing to
// contradict). Embedding-based similarity is the later enrichment-pass upgrade.
func lexicalSelfConsistency(texts []string) float64 {
	vecs := make([]map[string]float64, 0, len(texts))
	for _, t := range texts {
		if v := termFreq(t); len(v) > 0 {
			vecs = append(vecs, v)
		}
	}
	if len(vecs) < 2 {
		return 100
	}
	var sum float64
	var pairs int
	for i := range vecs {
		for j := i + 1; j < len(vecs); j++ {
			sum += cosine(vecs[i], vecs[j])
			pairs++
		}
	}
	if pairs == 0 {
		return 100
	}
	return sum / float64(pairs) * 100
}

func termFreq(s string) map[string]float64 {
	tf := map[string]float64{}
	for _, tok := range tokenize(s) {
		tf[tok]++
	}
	return tf
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func cosine(a, b map[string]float64) float64 {
	var dot, na, nb float64
	for k, v := range a {
		na += v * v
		if w, ok := b[k]; ok {
			dot += v * w
		}
	}
	for _, v := range b {
		nb += v * v
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// gradeTerm is one (score, weight) contribution to a weighted grade.
type gradeTerm struct {
	score  float64
	weight float64
}

// weightedGrade is the weight-normalized mean of its terms (0 when no weight).
func weightedGrade(terms ...gradeTerm) float64 {
	var num, den float64
	for _, t := range terms {
		num += t.score * t.weight
		den += t.weight
	}
	if den == 0 {
		return 0
	}
	return num / den
}

// scenarioGrade composes a scenario's rubric measures (all scenario-level, a
// function of the N runs together). factuality is the deterministic ground-truth
// pass-rate: a value < 0 means the scenario authored NO fact assertions, so the
// term is EXCLUDED (the grade normalizes by the weights present) - keeping the
// grade unchanged until facts are authored. fabFraction is the fraction of the
// scenario's runs that asserted a forbidden/stale fact; it applies a heavy (not
// fatal) multiplicative penalty so a confident fabrication cannot grade well.
func scenarioGrade(quality, reliability, factuality, participation, consistency, meanEfficiency, fabFraction float64, w rubricWeights) float64 {
	terms := []gradeTerm{
		{quality, w.Quality},
		{reliability, w.Reliability},
		{participation, w.Participation},
		{consistency, w.Consistency},
		{meanEfficiency, w.Efficiency},
	}
	if factuality >= 0 { // known: scenario authored fact assertions
		terms = append(terms, gradeTerm{factuality, w.Factuality})
	}
	return weightedGrade(terms...) * fabricationPenalty(fabFraction)
}
