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
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// seedADLPodStatus is a scenario from the homelab-pilot reference crew
// (crews/homelab-pilot-crew/fitness/k8s-pod-status.yaml).
const seedADLPodStatus = `DESCRIPTION Single-agent - k8s-workloads should report pod health for a specified namespace

DEFINE CONST QUESTION AS "Are all pods healthy in the kubemoot namespace? List any that are not Running."
DEFINE CONST MAX_DURATION AS 480 seconds

ASSERT(POST to discussion endpoint returns 200 with conversationId)
ASSERT(SSE stream emits "thread_found" event within 30 seconds)
ASSERT(discussion completes with "done" event within MAX_DURATION)

# k8s-workloads should pick this up
ASSERT(at least 1 specialist contributes with signal=agree)
ASSERT(coordinator produces synthesis)
ASSERT(synthesis is non-empty)

# Response should reference pods and either a healthy state or a not-running pod
ASSERT(synthesis CONTAINS "pod")
`

// seedADLWrapped is the complete scenario from docs/reference/adl-reference.md:
// a wrapped DESCRIPTION, QUESTION, and DEFER reference.
const seedADLWrapped = `DESCRIPTION List nodes with roles, capacity, and Ready condition;
  real identities, no invented nodes.

DEFINE CONST QUESTION AS "List the Kubernetes nodes with their roles, CPU and memory capacity,
  and current Ready condition."
DEFINE CONST MAX_DURATION AS 480 seconds

ASSERT(POST to discussion endpoint returns 200 with conversationId)
ASSERT(SSE stream emits "thread_found" event within 30 seconds)
ASSERT(discussion completes with "done" event within MAX_DURATION)
ASSERT(at least 1 specialist contributes with signal=agree)
ASSERT(synthesis is non-empty)
ASSERT(synthesis CONTAINS "node")

# Reference-grounded quality, scored 0.0-1.0 by the deferred judge.
ASSERT(DEFER synthesis REFLECTS "Lists the cluster's nodes with their roles, CPU/memory capacity,
  and Ready condition, discovered not assumed. Real node identities; no invented nodes.")
`

// seedMarkdownHelm is the prose Markdown twin used by TestParseMarkdownFitnessTest.
const seedMarkdownHelm = "# Single-agent \u2014 enumerate Helm releases\n" +
	"Which Helm releases are deployed across all namespaces and their versions?\n\n" +
	"- POST to discussion endpoint returns 200 with conversationId\n" +
	"- discussion completes with \"done\" event within 300 seconds\n" +
	"- coordinator produces synthesis\n" +
	"- synthesis CONTAINS \"release\"\n" +
	"- [ ] synthesis matches \"helm-[a-z]+\" at least 3 times\n\n" +
	"```reflects\n" +
	"Enumerates the cluster's actual Helm releases across namespaces, native and\n" +
	"Flux-managed. Names the REAL releases; 'none found' is wrong; does not invent.\n" +
	"```\n"

// fuzzSeedScenarios are the real scenario shapes every scenario fuzzer starts from.
var fuzzSeedScenarios = []string{
	seedADLPodStatus,
	seedADLWrapped,
	seedMarkdownHelm,
	"# Scenario\n**Question:** What namespaces exist?\n\n- synthesis is non-empty\n",
	"what is up?\n```reflects\nground truth\n```",
	`ASSERT(synthesis does NOT CONTAIN "agent")`,
	`ASSERT(DEFER synthesis REFLECTS "names the "kubemoot" namespace")`,
	"# Quoted\nq\n```reflects\nthe \"kubemoot\" namespace and C:\\temp\n```\n",
	"ASSERT(at least 99999999999999999999 specialists agree)",
}

// FuzzParseFitnessTest feeds arbitrary scenario text (ADL or Markdown) to the
// parser. Beyond not panicking it checks that parsing is deterministic, that every
// assertion's Raw text re-classifies to the same assertion (stored transcripts
// carry only Raw, and the operator re-parses it), and that numeric bounds are
// never negative.
func FuzzParseFitnessTest(f *testing.F) {
	for _, s := range fuzzSeedScenarios {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		ft := ParseFitnessTest(content)
		if again := ParseFitnessTest(content); !reflect.DeepEqual(ft, again) {
			t.Fatalf("parse is not deterministic:\n%+v\n%+v", ft, again)
		}
		if ft.Constants == nil {
			t.Fatal("Constants is nil")
		}
		for _, a := range ft.Assertions {
			checkAssertion(t, a)
		}
	})
}

// checkAssertion asserts the per-assertion invariants of FuzzParseFitnessTest.
func checkAssertion(t *testing.T, a Assertion) {
	t.Helper()
	if a.AgreeCount < 0 || a.MinCount < 0 || a.WithinSeconds < 0 {
		t.Fatalf("negative numeric bound in %+v", a)
	}
	if again := classifyAssertion(a.Raw); !reflect.DeepEqual(a, again) {
		t.Fatalf("Raw does not re-classify to the same assertion:\nparsed:     %+v\nre-parsed:  %+v", a, again)
	}
	if a.Kind != KindDeferred || a.Reference == "" {
		return
	}
	kw, ref, ok := ParseDefer(a.Raw)
	if !ok || kw != a.Keyword || ref != a.Reference {
		t.Fatalf("ParseDefer(%q) = (%q, %q, %v), want (%q, %q, true)", a.Raw, kw, ref, ok, a.Keyword, a.Reference)
	}
}

// FuzzDeferRoundTrip checks the canonical DEFER form both ways: a formatted
// assertion parses back to its keyword and reference, and any text ParseDefer
// accepts re-formats to text that parses to the same pair.
func FuzzDeferRoundTrip(f *testing.F) {
	f.Add(kwReflects, "There are 28 namespaces including kube-system, kubemoot, observability.")
	f.Add(kwReflects, `names the "kubemoot" namespace`)
	f.Add("GROUNDED", `C:\temp and a tab`)
	f.Fuzz(func(t *testing.T, keyword, reference string) {
		if kw, ref, ok := ParseDefer(keyword); ok {
			if k2, r2, ok2 := ParseDefer(FormatDefer(kw, ref)); !ok2 || k2 != kw || r2 != ref {
				t.Fatalf("re-formatted %q does not parse back to (%q, %q)", FormatDefer(kw, ref), kw, ref)
			}
		}
		if !validDeferKeyword(keyword) || reference == "" {
			return
		}
		raw := FormatDefer(keyword, reference)
		kw, ref, ok := ParseDefer(raw)
		if !ok || kw != strings.ToUpper(keyword) || ref != reference {
			t.Fatalf("ParseDefer(FormatDefer(%q, %q)) = (%q, %q, %v)", keyword, reference, kw, ref, ok)
		}
	})
}

// fuzzSafeText is the alphabet the parity fuzzer keeps: text both scenario forms
// can carry verbatim (no quotes, parens, list markers, fences, or line breaks).
var fuzzSafeText = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ,.:;'/=?!-]*$`)

// FuzzMarkdownADLParity writes the same scenario in both forms and checks they
// parse to the same description, question, and assertions: the two forms are
// documented as interchangeable, so a crew author's choice never changes a grade.
func FuzzMarkdownADLParity(f *testing.F) {
	f.Add("enumerate Helm releases", "Which Helm releases are deployed", "synthesis CONTAINS release", "reflects",
		"Names the REAL releases; none found is wrong.")
	f.Add("pods", "Are all pods healthy", "at least 1 specialist contributes with signal=agree", "grounded", "Lists pods.")
	f.Add("prompt fields", "Which PromptModule sets the DESCRIPTION line", "synthesis is non-empty", "reflects",
		"Names the module that sets DESCRIPTION and DEFINE CONST lines.")
	f.Fuzz(func(t *testing.T, desc, question, gate, keyword, reference string) {
		if !parityInputs(desc, question, gate, keyword, reference) {
			return
		}
		adl := "DESCRIPTION " + desc + "\n" +
			`DEFINE CONST QUESTION AS "` + question + "\"\n" +
			"ASSERT(" + gate + ")\n" +
			"ASSERT(" + FormatDefer(strings.ToUpper(keyword), reference) + ")\n"
		md := "# " + desc + "\n" + question + "\n\n- " + gate + "\n\n```" + keyword + "\n" + reference + "\n```\n"
		a, m := ParseFitnessTest(adl), ParseFitnessTest(md)
		if a.Description != m.Description || a.Constants["QUESTION"] != m.Constants["QUESTION"] {
			t.Fatalf("description/question differ:\nadl: %q %q\nmd:  %q %q",
				a.Description, a.Constants["QUESTION"], m.Description, m.Constants["QUESTION"])
		}
		if !reflect.DeepEqual(a.Assertions, m.Assertions) {
			t.Fatalf("assertions differ:\nadl: %+v\nmd:  %+v", a.Assertions, m.Assertions)
		}
	})
}

// parityInputs reports whether the fuzzed fields fit both forms verbatim. A
// Markdown line that opens with an ADL directive makes the file ADL by rule, so
// such a field is not a Markdown scenario.
func parityInputs(desc, question, gate, keyword, reference string) bool {
	for _, s := range []string{desc, question, gate, reference} {
		if !fuzzSafeText.MatchString(s) || s != collapseSpaces(s) || isADLDirective(s) {
			return false
		}
	}
	return validDeferKeyword(keyword) && !strings.HasPrefix(strings.ToUpper(gate), "DEFER")
}

// FuzzExtractFirstInt checks the integer scan never yields a negative bound,
// whatever digits a scenario author writes (overflow included).
func FuzzExtractFirstInt(f *testing.F) {
	f.Add("at least 1 specialist contributes with signal=agree")
	f.Add("completes within 300 seconds")
	f.Add("99999999999999999999 seconds")
	f.Fuzz(func(t *testing.T, text string) {
		if n := extractFirstInt(text); n < 0 {
			t.Fatalf("extractFirstInt(%q) = %d, want >= 0", text, n)
		}
	})
}
