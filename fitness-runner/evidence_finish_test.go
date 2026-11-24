package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/fitness-runner/internal/evidencenames"
)

func TestFinishEvidenceSkipsAnEmptyOrOversizedSet(t *testing.T) {
	if set, _, res := finishEvidence(evidencenames.NewEvidence(), 0, inlineEvidence{}); set != nil || res != readSkipped {
		t.Fatalf("no calls: set=%v res=%v", set, res)
	}

	big := evidencenames.NewEvidence()
	var sb strings.Builder
	for i := 0; i <= evidencenames.MaxTokens; i++ {
		fmt.Fprintf(&sb, "n%d ", i)
	}
	big.Add(sb.String())
	if !big.Truncated() {
		t.Fatal("test setup: the set should be truncated")
	}
	if set, _, res := finishEvidence(big, 1, inlineEvidence{}); set != nil || res != readSkipped {
		t.Fatalf("truncated: set=%v res=%v", set, res)
	}
}

func TestFinishEvidenceCountsInlineCallsAndJoinsTheirText(t *testing.T) {
	set, calls, res := finishEvidence(evidencenames.NewEvidence(), 2, inlineEvidence{calls: 1, texts: []string{"pod-abc"}})
	if res != readComplete || calls != 3 || !set.Has("pod-abc") {
		t.Fatalf("res=%v calls=%d has=%v", res, calls, set != nil && set.Has("pod-abc"))
	}
	// Inline results alone are enough to read the evidence.
	if _, calls, res = finishEvidence(evidencenames.NewEvidence(), 0, inlineEvidence{calls: 1}); res != readComplete || calls != 1 {
		t.Fatalf("inline only: res=%v calls=%d", res, calls)
	}
}
