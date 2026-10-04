/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"encoding/json"
	"sort"
	"strings"
)

// Pairwise A/B judging: the supporting two-arm measure of the ADL bake-off rubric.
// For each scenario present in BOTH arms (e.g. the ADL crew and the prose crew),
// the two crews' answers are shown to a judge panel in BLIND, randomized order
// (the judge never learns which answer came from which arm); a 3-judge majority
// decides which answer better addresses the question. Aggregating the per-scenario
// winners gives an ADL-vs-prose preference rate.
//
// This file is the deterministic CORE - alignment, blind doc assembly, the
// un-blind key, and the panel tally. It is pure (no NATS, no judge calls, no GPU)
// so it is exhaustively unit-tested; the dispatch + report wiring builds on it.
// See [[ADL Bake-Off Measurement Rubric]] (#6 blind pairwise preference).

// pairwiseDoc is the blind comparison handed to the judge: the question and the
// two answers in slots A and B. It deliberately carries NO arm identity - the
// judge must not be able to infer which is ADL.
type pairwiseDoc struct {
	Question string `json:"question"`
	AnswerA  string `json:"answer_a"`
	AnswerB  string `json:"answer_b"`
}

// pairwiseComparison is one scenario's blind comparison plus its un-blind key.
type pairwiseComparison struct {
	Scenario string
	Doc      pairwiseDoc
	// aSlot is which arm landed in slot A ("baseline" arm name vs "variant" arm
	// name supplied by the caller). bSlot is the other. Used to un-blind a verdict.
	aArm string // arm name occupying slot A
	bArm string // arm name occupying slot B
}

// armAnswers maps scenario -> the arm's representative synthesis for that scenario.
type armAnswers struct {
	arm     string            // arm label, e.g. "homelab-pilot" (ADL) or "homelab-pilot-prose"
	answers map[string]string // scenario testRef -> representative synthesis
	// questions carries the scenario question text (same across arms; either arm's is fine).
	questions map[string]string
}

// alignScenarios returns the scenarios present in BOTH arms (sorted), so only
// comparable pairs are judged. A scenario missing from either arm (a gap, or a
// gated/empty synthesis) is skipped and reported separately by the caller.
func alignScenarios(a, b armAnswers) []string {
	out := []string{}
	for s, ans := range a.answers {
		if strings.TrimSpace(ans) == "" {
			continue
		}
		if bans, ok := b.answers[s]; ok && strings.TrimSpace(bans) != "" {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// buildPairwiseComparison assembles one scenario's blind comparison. aInSlotA
// decides the randomized order: when true, arm a's answer is slot A; otherwise arm
// b's is. The caller supplies the random bit (recorded per comparison so the
// blinding is reproducible and the un-blind key is exact).
func buildPairwiseComparison(scenario, question string, a, b armAnswers, aInSlotA bool) pairwiseComparison {
	pc := pairwiseComparison{Scenario: scenario}
	if aInSlotA {
		pc.Doc = pairwiseDoc{Question: question, AnswerA: a.answers[scenario], AnswerB: b.answers[scenario]}
		pc.aArm, pc.bArm = a.arm, b.arm
	} else {
		pc.Doc = pairwiseDoc{Question: question, AnswerA: b.answers[scenario], AnswerB: a.answers[scenario]}
		pc.aArm, pc.bArm = b.arm, a.arm
	}
	return pc
}

// marshalPairwiseDoc renders the blind doc the judge receives. Distinct from the
// single-arm judgeComparisonDoc: no reference, no arm identity - just the question
// and the two anonymized answers.
func marshalPairwiseDoc(pc pairwiseComparison) (string, error) {
	out, err := json.Marshal(pc.Doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// pairwiseVote is one judge's normalized verdict for a blind comparison.
type pairwiseVote string

const (
	voteA   pairwiseVote = "A"
	voteB   pairwiseVote = "B"
	voteTie pairwiseVote = "TIE"
)

// normalizePairwiseVote maps a judge's raw reply to A / B / TIE. Tolerant of
// surrounding prose, case, and "answer a"/"option b" phrasings; anything
// unrecognized is a TIE (a non-committal judge must not be read as a preference).
func normalizePairwiseVote(raw string) pairwiseVote {
	s := strings.ToUpper(strings.TrimSpace(raw))
	if s == "" {
		return voteTie
	}
	// Prefer an explicit standalone token if present.
	if v, ok := pairwiseVoteTokens[s]; ok {
		return v
	}
	// Fall back to first decisive token scan.
	if strings.Contains(s, string(voteTie)) {
		return voteTie
	}
	hasA := mentionsPairwiseSlot(s, "A")
	hasB := mentionsPairwiseSlot(s, "B")
	switch {
	case hasA && !hasB:
		return voteA
	case hasB && !hasA:
		return voteB
	default:
		return voteTie
	}
}

// pairwiseVoteTokens are the whole replies that name a verdict directly.
var pairwiseVoteTokens = map[string]pairwiseVote{
	"A": voteA, "ANSWER A": voteA, "OPTION A": voteA,
	"B": voteB, "ANSWER B": voteB, "OPTION B": voteB,
	"TIE": voteTie, "EQUAL": voteTie, "NEITHER": voteTie, "BOTH": voteTie,
}

// mentionsPairwiseSlot reports whether an upper-cased reply starts with the slot
// letter or names it as "ANSWER <slot>" or "OPTION <slot>".
func mentionsPairwiseSlot(s, slot string) bool {
	return strings.HasPrefix(s, slot) || strings.Contains(s, "ANSWER "+slot) || strings.Contains(s, "OPTION "+slot)
}

// tallyPanel reduces a panel's votes to a single slot verdict by MAJORITY. With
// no strict majority for A or B (e.g. 1-1-1, or a tie plurality), the result is
// TIE - a panel that cannot agree is not a preference.
func tallyPanel(votes []pairwiseVote) pairwiseVote {
	var a, b int
	for _, v := range votes {
		switch v {
		case voteA:
			a++
		case voteB:
			b++
		}
	}
	majority := len(votes)/2 + 1
	switch {
	case a >= majority && a > b:
		return voteA
	case b >= majority && b > a:
		return voteB
	default:
		return voteTie
	}
}

// unblind maps a slot verdict back to the winning ARM name using the comparison's
// un-blind key. A TIE stays "" (no winner).
func unblind(pc pairwiseComparison, v pairwiseVote) string {
	switch v {
	case voteA:
		return pc.aArm
	case voteB:
		return pc.bArm
	default:
		return ""
	}
}

// pairwiseTally accumulates per-arm wins + ties across all judged scenarios.
type pairwiseTally struct {
	Wins map[string]int // arm name -> scenarios it won
	Ties int
	// PerScenario records the winning arm ("" = tie) for each scenario, for the delta tab.
	PerScenario map[string]string
}

func newPairwiseTally() *pairwiseTally {
	return &pairwiseTally{Wins: map[string]int{}, PerScenario: map[string]string{}}
}

// record folds one scenario's panel verdict into the tally.
func (t *pairwiseTally) record(pc pairwiseComparison, panel pairwiseVote) {
	winner := unblind(pc, panel)
	t.PerScenario[pc.Scenario] = winner
	if winner == "" {
		t.Ties++
		return
	}
	t.Wins[winner]++
}
