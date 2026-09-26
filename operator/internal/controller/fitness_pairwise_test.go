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
	"reflect"
	"strings"
	"testing"
)

// Neutral answer text (no "adl"/"prose" words) so the blind-leak assertion tests
// arm IDENTITY leakage, not coincidental answer content.
func adlArm() armAnswers {
	return armAnswers{
		arm:       "homelab-pilot",
		answers:   map[string]string{"s1": "first-crew reply one", "s2": "first-crew reply two", "only-adl": "x"},
		questions: map[string]string{"s1": "q1", "s2": "q2", "only-adl": "qx"},
	}
}
func proseArm() armAnswers {
	return armAnswers{
		arm:       "homelab-pilot-prose",
		answers:   map[string]string{"s1": "second-crew reply one", "s2": "second-crew reply two", "only-prose": "y", "empty": "  "},
		questions: map[string]string{"s1": "q1", "s2": "q2"},
	}
}

func TestAlignScenarios(t *testing.T) {
	got := alignScenarios(adlArm(), proseArm())
	want := []string{"s1", "s2"} // only-adl / only-prose excluded; "empty" excluded (not in adl anyway)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alignScenarios = %v, want %v", got, want)
	}
	// An arm with an empty answer for a shared scenario excludes it.
	a := armAnswers{arm: "a", answers: map[string]string{"s1": "x", "s2": ""}}
	b := armAnswers{arm: "b", answers: map[string]string{"s1": "y", "s2": "z"}}
	if got := alignScenarios(a, b); !reflect.DeepEqual(got, []string{"s1"}) {
		t.Fatalf("empty-answer scenario must be excluded, got %v", got)
	}
}

func TestBuildPairwiseComparison_BlindOrderAndKey(t *testing.T) {
	adl, prose := adlArm(), proseArm()

	// aInSlotA=true -> ADL is slot A.
	pc := buildPairwiseComparison("s1", "q1", adl, prose, true)
	if pc.Doc.AnswerA != "first-crew reply one" || pc.Doc.AnswerB != "second-crew reply one" {
		t.Fatalf("slot order wrong: A=%q B=%q", pc.Doc.AnswerA, pc.Doc.AnswerB)
	}
	if pc.aArm != "homelab-pilot" || pc.bArm != "homelab-pilot-prose" {
		t.Fatalf("un-blind key wrong: aArm=%q bArm=%q", pc.aArm, pc.bArm)
	}
	// aInSlotA=false -> prose is slot A (the randomized flip).
	pc2 := buildPairwiseComparison("s1", "q1", adl, prose, false)
	if pc2.Doc.AnswerA != "second-crew reply one" || pc2.Doc.AnswerB != "first-crew reply one" {
		t.Fatalf("flipped slot order wrong: A=%q B=%q", pc2.Doc.AnswerA, pc2.Doc.AnswerB)
	}
	if pc2.aArm != "homelab-pilot-prose" || pc2.bArm != "homelab-pilot" {
		t.Fatalf("flipped un-blind key wrong: aArm=%q bArm=%q", pc2.aArm, pc2.bArm)
	}
	// The marshalled doc carries NO arm identity (blind).
	doc, err := marshalPairwiseDoc(pc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(doc, "homelab-pilot") || strings.Contains(strings.ToLower(doc), "adl") || strings.Contains(strings.ToLower(doc), "prose") {
		t.Fatalf("blind doc leaked arm identity: %s", doc)
	}
	var rd pairwiseDoc
	if json.Unmarshal([]byte(doc), &rd) != nil || rd.Question != "q1" {
		t.Fatalf("doc must round-trip with the question, got %s", doc)
	}
}

func TestNormalizePairwiseVote(t *testing.T) {
	cases := map[string]pairwiseVote{
		"A":                          voteA,
		" b ":                        voteB,
		"Answer A":                   voteA,
		"option B":                   voteB,
		"TIE":                        voteTie,
		"neither":                    voteTie,
		"":                           voteTie,
		"A is clearly better":        voteA,
		"B better addresses it":      voteB,
		"It's a tie, both are equal": voteTie,
		"mumble":                     voteTie, // unrecognized -> tie (no false preference)
	}
	for raw, want := range cases {
		if got := normalizePairwiseVote(raw); got != want {
			t.Errorf("normalizePairwiseVote(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTallyPanel_Majority(t *testing.T) {
	cases := []struct {
		votes []pairwiseVote
		want  pairwiseVote
	}{
		{[]pairwiseVote{voteA, voteA, voteA}, voteA},   // 3-0
		{[]pairwiseVote{voteA, voteA, voteB}, voteA},   // 2-1
		{[]pairwiseVote{voteB, voteB, voteTie}, voteB}, // 2-1 with a tie vote
		{[]pairwiseVote{voteA, voteB, voteTie}, voteTie}, // 1-1-1 -> no majority
		{[]pairwiseVote{voteTie, voteTie, voteA}, voteTie}, // tie plurality -> tie
		{[]pairwiseVote{voteA, voteB}, voteTie},            // 1-1 even split
	}
	for _, c := range cases {
		if got := tallyPanel(c.votes); got != c.want {
			t.Errorf("tallyPanel(%v) = %q, want %q", c.votes, got, c.want)
		}
	}
}

func TestUnblindAndTally(t *testing.T) {
	adl, prose := adlArm(), proseArm()
	tally := newPairwiseTally()

	// s1: ADL in slot A, panel picks A -> ADL wins.
	pc1 := buildPairwiseComparison("s1", "q1", adl, prose, true)
	if w := unblind(pc1, voteA); w != "homelab-pilot" {
		t.Fatalf("unblind A = %q, want homelab-pilot", w)
	}
	tally.record(pc1, voteA)

	// s2: prose in slot A (flipped), panel picks A -> prose wins.
	pc2 := buildPairwiseComparison("s2", "q2", adl, prose, false)
	if w := unblind(pc2, voteA); w != "homelab-pilot-prose" {
		t.Fatalf("unblind flipped A = %q, want homelab-pilot-prose", w)
	}
	tally.record(pc2, voteA)

	// a tie scenario
	pc3 := buildPairwiseComparison("s1", "q1", adl, prose, true)
	tally.record(pc3, voteTie)

	if tally.Wins["homelab-pilot"] != 1 || tally.Wins["homelab-pilot-prose"] != 1 || tally.Ties != 1 {
		t.Fatalf("tally wrong: %+v", tally)
	}
	if tally.PerScenario["s2"] != "homelab-pilot-prose" {
		t.Fatalf("per-scenario winner wrong: %v", tally.PerScenario)
	}
	if unblind(pc1, voteTie) != "" {
		t.Fatalf("tie must un-blind to no winner")
	}
}
