/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"math"
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func ar(raw string, passed bool) kubemootv1alpha1.AssertionResult {
	return kubemootv1alpha1.AssertionResult{Raw: raw, Passed: passed}
}

func TestFactAssertionClassification(t *testing.T) {
	cases := []struct {
		raw                       string
		required, forbidden, fact bool
	}{
		{`synthesis CONTAINS "harbor-database"`, true, false, true},
		{`synthesis does NOT CONTAIN "no database"`, false, true, true},
		{`synthesis CONTAINS "a" AND "b"`, true, false, true},
		// Shape + judge assertions are NOT factuality.
		{`synthesis is non-empty`, false, false, false},
		{`POST to discussion endpoint returns 200`, false, false, false},
		{`DEFER synthesis REFLECTS "method and honesty"`, false, false, false},
		{`at least 1 specialist contributes with signal=agree`, false, false, false},
	}
	for _, c := range cases {
		if got := isRequiredFactAssertion(c.raw); got != c.required {
			t.Errorf("isRequired(%q)=%v want %v", c.raw, got, c.required)
		}
		if got := isForbiddenFactAssertion(c.raw); got != c.forbidden {
			t.Errorf("isForbidden(%q)=%v want %v", c.raw, got, c.forbidden)
		}
		if got := isContentFactAssertion(c.raw); got != c.fact {
			t.Errorf("isContent(%q)=%v want %v", c.raw, got, c.fact)
		}
	}
}

func TestFactualityScore(t *testing.T) {
	// No content fact-assertions -> unknown (-1), NOT a phantom 100.
	shapeOnly := []kubemootv1alpha1.AssertionResult{
		ar("POST returns 200", true), ar("synthesis is non-empty", true),
		ar(`DEFER synthesis REFLECTS "x"`, true),
	}
	if got := factualityScore(shapeOnly); got != -1 {
		t.Errorf("shape-only factuality=%v, want -1 (unknown)", got)
	}

	// All facts present -> 100.
	allGood := []kubemootv1alpha1.AssertionResult{
		ar(`synthesis CONTAINS "harbor-database"`, true),
		ar(`synthesis does NOT CONTAIN "no database"`, true),
		ar("synthesis is non-empty", true), // shape ignored
	}
	if got := factualityScore(allGood); got != 100 {
		t.Errorf("all-facts factuality=%v, want 100", got)
	}

	// A fabrication: the forbidden claim is present (NOT-CONTAIN failed) and the
	// required fact missing -> 0 of 2 content assertions.
	fabricated := []kubemootv1alpha1.AssertionResult{
		ar(`synthesis CONTAINS "harbor-database"`, false),
		ar(`synthesis does NOT CONTAIN "no database"`, false),
	}
	if got := factualityScore(fabricated); got != 0 {
		t.Errorf("fabricated factuality=%v, want 0", got)
	}
	if !fabricationTripped(fabricated) {
		t.Error("fabricationTripped should be true when a NOT-CONTAIN failed")
	}
	if fabricationTripped(allGood) {
		t.Error("fabricationTripped should be false when all forbidden checks pass")
	}
}

func TestDeclaresHonestFailure(t *testing.T) {
	if !declaresHonestFailure("The database status cannot be determined as no data was returned") {
		t.Error("should detect an honest could-not-determine answer")
	}
	if declaresHonestFailure("There are 12 nodes across 3 control-plane and 9 workers") {
		t.Error("a confident answer is not an honest failure")
	}
}

func TestFabricationPenalty(t *testing.T) {
	if p := fabricationPenalty(0); p != 1.0 {
		t.Errorf("no fabrication -> penalty %v, want 1.0", p)
	}
	if p := fabricationPenalty(1); math.Abs(p-fabricationFloor) > 1e-9 {
		t.Errorf("all fabricate -> penalty %v, want floor %v", p, fabricationFloor)
	}
	// Half the runs fabricate -> halfway between 1.0 and the floor.
	if p := fabricationPenalty(0.5); math.Abs(p-(1-(1-fabricationFloor)*0.5)) > 1e-9 {
		t.Errorf("half fabricate -> penalty %v unexpected", p)
	}
	// Out-of-range inputs clamp.
	if p := fabricationPenalty(2); math.Abs(p-fabricationFloor) > 1e-9 {
		t.Errorf("clamp >1 -> %v, want floor", p)
	}
	if p := fabricationPenalty(-1); p != 1.0 {
		t.Errorf("clamp <0 -> %v, want 1.0", p)
	}
}
