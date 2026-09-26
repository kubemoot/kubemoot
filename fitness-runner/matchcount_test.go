package main

import "testing"

// TestSynthesisMatchCount_ParseAndEval covers the numeric-bound count assertion:
// "synthesis matches "<regex>" at least N times" parses with the regex + bound,
// and a gross under-report (4 of an expected >=40) fails while a full listing passes.
func TestSynthesisMatchCount_ParseAndEval(t *testing.T) {
	a := classifyAssertion(`synthesis matches "[a-z0-9-]+/[a-z0-9-]+" at least 40 times`)
	if a.Kind != KindSynthesisMatchCount {
		t.Fatalf("kind = %v, want KindSynthesisMatchCount", a.Kind)
	}
	if a.Pattern != "[a-z0-9-]+/[a-z0-9-]+" {
		t.Errorf("pattern = %q", a.Pattern)
	}
	if a.MinCount != 40 {
		t.Errorf("minCount = %d, want 40 (a digit inside the regex must not be read)", a.MinCount)
	}

	// Under-report: only 4 namespace/name lines -> fails.
	under := "harbor/core\nharbor/jobservice\nnats/box\nkubemoot/operator\n"
	if r := evalSynthesisMatchCount(a, under); r.Passed {
		t.Errorf("4 matches must fail a >=40 bound: %s", r.Message)
	}

	// Full listing: 40 entries -> passes.
	full := ""
	for i := 0; i < 40; i++ {
		full += "ns/dep\n"
	}
	if r := evalSynthesisMatchCount(a, full); !r.Passed {
		t.Errorf("40 matches must pass a >=40 bound: %s", r.Message)
	}

	// An invalid regex fails closed rather than panicking.
	bad := Assertion{Kind: KindSynthesisMatchCount, Pattern: "([", MinCount: 1}
	if r := evalSynthesisMatchCount(bad, "anything"); r.Passed {
		t.Error("invalid regex should fail, not pass")
	}
}
