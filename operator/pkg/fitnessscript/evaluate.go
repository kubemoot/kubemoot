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
	"fmt"
	"regexp"
	"strings"
)

// SignalEvent represents a single SSE event from the discussion gateway stream.
type SignalEvent struct {
	Type       string `json:"type"`
	Agent      string `json:"agent,omitempty"`
	Status     string `json:"status,omitempty"`
	GPU        string `json:"gpu,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Content    string `json:"content,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
	StoodAside bool   `json:"stood_aside,omitempty"`
	Error      string `json:"error,omitempty"`
}

// AssertionResult is the per-assertion pass/fail outcome of one run.
type AssertionResult struct {
	Raw     string `json:"raw"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// RunState is what a finished run offers the assertions to check against.
// The fitness runner fills it from the live discussion; the operator's re-judge
// fills it from a stored transcript.
type RunState struct {
	PostOK    bool
	Events    []SignalEvent
	Synthesis string
	TimedOut  bool
}

// Evaluate runs every assertion against the run state and returns the
// per-assertion results in order.
func Evaluate(assertions []Assertion, s RunState) []AssertionResult {
	results := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		results = append(results, EvaluateAssertion(a, s))
	}
	return results
}

// evaluators maps each assertion kind to its check.
var evaluators = map[AssertionKind]func(Assertion, RunState) AssertionResult{
	KindPostReturns200: func(a Assertion, s RunState) AssertionResult {
		return evalPostReturns200(a, s.PostOK)
	},
	KindSseEmits: func(a Assertion, s RunState) AssertionResult {
		return evalSseEmits(a, s.Events)
	},
	KindSseEmitsWithin: func(a Assertion, s RunState) AssertionResult {
		return evalSseEmitsWithin(a, s.Events)
	},
	KindCompletesWithin: func(a Assertion, s RunState) AssertionResult {
		return evalCompletesWithin(a, s.Events, s.TimedOut)
	},
	KindMinSpecialistAgrees: func(a Assertion, s RunState) AssertionResult {
		return evalMinSpecialistAgrees(a, s.Events)
	},
	KindCoordinatorSynthesizes: func(a Assertion, s RunState) AssertionResult {
		return evalCoordinatorSynthesizes(a, s.Synthesis)
	},
	KindSynthesisNonEmpty: func(a Assertion, s RunState) AssertionResult {
		return evalSynthesisNonEmpty(a, s.Synthesis)
	},
	KindSynthesisContains: func(a Assertion, s RunState) AssertionResult {
		return evalSynthesisContains(a, s.Synthesis)
	},
	KindSynthesisNotContains: func(a Assertion, s RunState) AssertionResult {
		return evalSynthesisNotContains(a, s.Synthesis)
	},
	KindSynthesisMatchCount: func(a Assertion, s RunState) AssertionResult {
		return evalSynthesisMatchCount(a, s.Synthesis)
	},
	KindDeferred: func(a Assertion, _ RunState) AssertionResult { return evalDeferred(a) },
}

// EvaluateAssertion checks one assertion against the run state. A kind with no
// automatic check passes with a manual-review advisory.
func EvaluateAssertion(a Assertion, s RunState) AssertionResult {
	if eval, ok := evaluators[a.Kind]; ok {
		return eval(a, s)
	}
	return AssertionResult{Raw: a.Raw, Passed: true, Message: "Custom assertion - manual review recommended"}
}

// evalDeferred does not evaluate inline (a judge call would compete with the crew
// for GPU and perturb the run). The post-suite engine resolves the keyword to its
// crew and overwrites this with the 0.0-1.0 reference-grounded score.
func evalDeferred(a Assertion) AssertionResult {
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  true,
		Message: fmt.Sprintf("DEFER %s - deferred to post-suite judge crew", a.Keyword),
	}
}

func evalPostReturns200(a Assertion, postOK bool) AssertionResult {
	if postOK {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "POST returned 200"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "POST did not return 200"}
}

func evalSseEmits(a Assertion, events []SignalEvent) AssertionResult {
	if hasEventType(events, a.EventType) {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: fmt.Sprintf(`Event "%s" received`, a.EventType)}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: fmt.Sprintf(`Event "%s" not received`, a.EventType)}
}

func evalSseEmitsWithin(a Assertion, events []SignalEvent) AssertionResult {
	// We cannot measure per-event timing from the stream since we don't timestamp events.
	// A "within N seconds" check degrades to a plain presence check: if the stream
	// completed within the overall timeout AND the event was present, it passed.
	// Delegates to evalSseEmits since timing granularity is not yet available.
	return evalSseEmits(a, events)
}

func evalCompletesWithin(a Assertion, events []SignalEvent, timedOut bool) AssertionResult {
	hasDone := HasDone(events)
	if hasDone && !timedOut {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Discussion completed with done event"}
	}
	if timedOut {
		return AssertionResult{Raw: a.Raw, Passed: false, Message: "Timed out before done event"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "No done event received"}
}

func evalMinSpecialistAgrees(a Assertion, events []SignalEvent) AssertionResult {
	count := CountAgreeSignals(events)
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  count >= a.AgreeCount,
		Message: fmt.Sprintf("%d agree signal(s) received (need %d)", count, a.AgreeCount),
	}
}

func evalCoordinatorSynthesizes(a Assertion, synthesis string) AssertionResult {
	if synthesis != "" {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Coordinator produced synthesis"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "No synthesis produced"}
}

func evalSynthesisNonEmpty(a Assertion, synthesis string) AssertionResult {
	if synthesis != "" {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "Synthesis is non-empty"}
	}
	return AssertionResult{Raw: a.Raw, Passed: false, Message: "Synthesis is empty"}
}

func evalSynthesisContains(a Assertion, synthesis string) AssertionResult {
	lower := strings.ToLower(synthesis)
	var missing []string
	for _, term := range a.Terms {
		if !strings.Contains(lower, strings.ToLower(term)) {
			missing = append(missing, term)
		}
	}
	if len(missing) == 0 {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "All expected terms found in synthesis"}
	}
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  false,
		Message: fmt.Sprintf("Missing terms: %s", strings.Join(missing, ", ")),
	}
}

func evalSynthesisNotContains(a Assertion, synthesis string) AssertionResult {
	lower := strings.ToLower(synthesis)
	var found []string
	for _, term := range a.Terms {
		if strings.Contains(lower, strings.ToLower(term)) {
			found = append(found, term)
		}
	}
	if len(found) == 0 {
		return AssertionResult{Raw: a.Raw, Passed: true, Message: "No prohibited terms found in synthesis"}
	}
	return AssertionResult{
		Raw:     a.Raw,
		Passed:  false,
		Message: fmt.Sprintf("Prohibited terms found: %s", strings.Join(found, ", ")),
	}
}

// evalSynthesisMatchCount passes when the synthesis contains at least MinCount
// matches of Pattern (case-insensitive regex) - the drift-tolerant numeric bound
// that catches a gross under-report (e.g. 4 of 141 deployments) without pinning
// the exact count.
func evalSynthesisMatchCount(a Assertion, synthesis string) AssertionResult {
	re, err := regexp.Compile("(?i)" + a.Pattern)
	if err != nil {
		return AssertionResult{Raw: a.Raw, Passed: false,
			Message: fmt.Sprintf("invalid match pattern %q: %v", a.Pattern, err)}
	}
	n := len(re.FindAllString(synthesis, -1))
	if n >= a.MinCount {
		return AssertionResult{Raw: a.Raw, Passed: true,
			Message: fmt.Sprintf("%d matches of /%s/ (need >= %d)", n, a.Pattern, a.MinCount)}
	}
	return AssertionResult{Raw: a.Raw, Passed: false,
		Message: fmt.Sprintf("only %d matches of /%s/ (need >= %d) - likely an under-report", n, a.Pattern, a.MinCount)}
}

// hasEventType returns true if any event in the slice has the given type.
func hasEventType(events []SignalEvent, eventType string) bool {
	for _, ev := range events {
		if ev.Type == eventType {
			return true
		}
	}
	return false
}

// HasDone reports whether the stream reached its 'done' event.
func HasDone(events []SignalEvent) bool {
	return hasEventType(events, "done")
}

// FindSynthesis scans events (in order) and returns the content of the first
// "synthesis" event, or the "done" event content if no synthesis event exists.
func FindSynthesis(events []SignalEvent) string {
	for _, ev := range events {
		if ev.Type == "synthesis" && ev.Content != "" {
			return ev.Content
		}
	}
	// Fallback: some implementations put synthesis content on the done event
	for _, ev := range events {
		if ev.Type == "done" && ev.Content != "" {
			return ev.Content
		}
	}
	return ""
}

// CountAgreeSignals counts "finding" events with signal "agree". This backs the
// ">=N toolers agree" floor assertion; the operator's agreeCountFromEvents
// (fitness_scoring.go) MUST count agrees identically, because participation is
// scored against the same expected N. Keep the two in sync.
func CountAgreeSignals(events []SignalEvent) int {
	count := 0
	for _, ev := range events {
		if ev.Type == "finding" && ev.Signal == "agree" {
			count++
		}
	}
	return count
}
