package main

import (
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

// A standalone CrewFitness never reaches the judge, so its deferred assertion must
// say it was not scored instead of reading as a pass.
func TestLabelUnjudged(t *testing.T) {
	assertions := []fitnessscript.Assertion{
		{Kind: fitnessscript.KindSynthesisNonEmpty, Raw: "synthesis is non-empty"},
		{Kind: fitnessscript.KindDeferred, Keyword: "REFLECTS", Raw: `DEFER synthesis REFLECTS "ref"`},
	}
	results := fitnessscript.Evaluate(assertions, fitnessscript.RunState{PostOK: true, Synthesis: "an answer"})
	labelUnjudged(assertions, results)

	if results[0].Message == "" || strings.Contains(results[0].Message, "not scored") {
		t.Fatalf("a non-deferred assertion must keep its own message, got %q", results[0].Message)
	}
	if !strings.Contains(results[1].Message, "not scored") || !strings.Contains(results[1].Message, "CrewFitnessSuite") {
		t.Fatalf("deferred message = %q", results[1].Message)
	}
	if !results[1].Passed {
		t.Fatal("an unjudged assertion is not a failure of the crew")
	}
}

func TestLabelUnjudgedToleratesShortResults(t *testing.T) {
	assertions := []fitnessscript.Assertion{{Kind: fitnessscript.KindDeferred, Keyword: "REFLECTS"}}
	labelUnjudged(assertions, nil)
	labelUnjudged(nil, []AssertionResult{{Message: "kept"}})
}
