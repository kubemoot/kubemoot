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

package controller

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf8"

	"github.com/kubemoot/kubemoot/operator/pkg/fitnessscript"
)

const (
	fuzzBiasSeed    = "0.7"
	fuzzActionAllow = "allow"
	fuzzActionDeny  = "deny"
)

// FuzzParseScenarioQuality feeds arbitrary judge-crew output (model text, so
// untrusted) to the verdict parser. A parsed quality must be a finite score in
// 0-100, and parsing must be deterministic.
func FuzzParseScenarioQuality(f *testing.F) {
	for _, s := range []string{
		`{"scores":[{"index":0,"score":1.0,"fabrication":false},{"index":1,"score":0.5,"fabrication":false}],"total":2}`,
		`{"scores":[{"index":0,"score":1.0,"reason":"all key facts present"},{"index":1,"score":0.4,"reason":"missed the storage implication"}],"total":2}`,
		`{"scores":[{"index":0,"score":90}],"total":1}`,
		`{"scores":[],"total":3}`,
		"<think>weighing the answers</think>\n{\"scores\":[{\"index\":0,\"score\":0.8}],\"total\":1}",
		`{"score":0.85,"fabrication":false,"reasoning":"matches the reference"}`,
		`{"scores":[{"index":0,"score":-3}],"total":1e-300}`,
		`no verdict at all`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, synthesis string) {
		q, reason, err := parseScenarioQuality(synthesis)
		q2, reason2, err2 := parseScenarioQuality(synthesis)
		if q != q2 || reason != reason2 || (err == nil) != (err2 == nil) {
			t.Fatalf("not deterministic: (%v, %q, %v) then (%v, %q, %v)", q, reason, err, q2, reason2, err2)
		}
		if err == nil && (math.IsNaN(q) || q < 0 || q > 100) {
			t.Fatalf("quality %v outside 0-100 for %q", q, synthesis)
		}
	})
}

// FuzzMergeNamespaceCrews feeds an arbitrary kubemoot.ai/crews annotation value
// (anyone who can edit the Namespace can write it) and one crew's provenance.
// The merged value must record exactly that entry, keep every other crew of a
// well-formed value, and be a fixed point: merging the same entry again changes
// nothing. Inputs are valid UTF-8 like every string the Kubernetes API serves.
func FuzzMergeNamespaceCrews(f *testing.F) {
	f.Add(``, "pilot", "git@github.com:kubemoot/crews.git", "abc123", "2026-10-01T12:00:00Z")
	f.Add(`{"other":{"revision":"r1"}}`, "pilot", "", "r2", "")
	f.Add(`{"pilot":{"revision":"r1","deployedAt":"2026-10-01T12:00:00Z"}}`, "pilot", "", "", "")
	f.Add(`not json`, "pilot", "src", "r1", "")
	f.Add(`null`, "", "", "", "")
	f.Fuzz(func(t *testing.T, raw, crew, source, revision, deployedAt string) {
		if !allValidUTF8(raw, crew, source, revision, deployedAt) {
			return
		}
		entry := namespaceCrewEntry{Source: source, Revision: revision, DeployedAt: deployedAt}
		merged, _ := mergeNamespaceCrews(raw, crew, entry)
		got, wellFormed := parseNamespaceCrews(merged)
		if !wellFormed {
			t.Fatalf("merged value %q does not parse", merged)
		}
		checkMergedEntry(t, got, crew, entry)
		checkOthersKept(t, raw, got, crew)
		if again, changed := mergeNamespaceCrews(merged, crew, entry); changed || again != merged {
			t.Fatalf("second merge changed %q to %q (changed=%v)", merged, again, changed)
		}
	})
}

// allValidUTF8 reports whether every string is valid UTF-8.
func allValidUTF8(ss ...string) bool {
	for _, s := range ss {
		if !utf8.ValidString(s) {
			return false
		}
	}
	return true
}

// checkMergedEntry asserts crew's entry is recorded, or absent when empty.
func checkMergedEntry(t *testing.T, got map[string]namespaceCrewEntry, crew string, entry namespaceCrewEntry) {
	t.Helper()
	current, present := got[crew]
	if entry.isEmpty() && present {
		t.Fatalf("empty entry for %q left %+v behind", crew, current)
	}
	if !entry.isEmpty() && current != entry {
		t.Fatalf("entry for %q = %+v, want %+v", crew, current, entry)
	}
}

// checkOthersKept asserts every other crew of a well-formed raw value survives.
func checkOthersKept(t *testing.T, raw string, got map[string]namespaceCrewEntry, crew string) {
	t.Helper()
	before, wellFormed := parseNamespaceCrews(raw)
	if !wellFormed {
		return
	}
	for name, e := range before {
		if name != crew && got[name] != e {
			t.Fatalf("crew %q changed from %+v to %+v", name, e, got[name])
		}
	}
}

// FuzzParseQualityBias feeds an arbitrary CrewSchedulingPolicy qualityBias map
// value. An accepted value lies in [0, 1] and survives a format-and-parse.
func FuzzParseQualityBias(f *testing.F) {
	for _, s := range []string{fuzzBiasSeed, " 0.5 ", "1", "0", "1.0000001", "-0", "NaN", "+Inf", "0x1p-2", "1e-400", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, ok := parseQualityBias(s)
		if !ok {
			return
		}
		if v < 0 || v > 1 || math.IsNaN(v) {
			t.Fatalf("parseQualityBias(%q) = %v, outside [0, 1]", s, v)
		}
		if back, ok := parseQualityBias(strconv.FormatFloat(v, 'g', -1, 64)); !ok || back != v {
			t.Fatalf("%v does not survive a format-and-parse (%v, %v)", v, back, ok)
		}
	})
}

// FuzzParseAgentDecision feeds arbitrary policy-agent output (model text). An
// accepted decision is allow or deny, and re-encoding it parses to the same
// decision.
func FuzzParseAgentDecision(f *testing.F) {
	for _, s := range []string{
		`{"action":"allow","confidence":0.9,"reason":"good"}`,
		`Here is my evaluation: {"action":"deny","confidence":0.8,"reason":"risky"} done.`,
		`{"action":"maybe","confidence":0.5}`,
		`{"action":"ALLOW"}`,
		`} {`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, response string) {
		d, err := parseAgentDecision(response)
		if err != nil {
			return
		}
		if d.Action != fuzzActionAllow && d.Action != fuzzActionDeny {
			t.Fatalf("accepted action %q", d.Action)
		}
		b, mErr := json.Marshal(d)
		if mErr != nil {
			t.Fatalf("marshal %+v: %v", d, mErr)
		}
		if again, err := parseAgentDecision(string(b)); err != nil || !reflect.DeepEqual(again, d) {
			t.Fatalf("re-encoded %s parsed to (%+v, %v), want %+v", b, again, err, d)
		}
	})
}

// FuzzDeferredReference follows a DEFER reference from a scenario to the judge:
// the runner stores each assertion's Raw in the transcript, and the post-suite
// judge reads the reference back from it. The judge must read exactly the
// reference the scenario parser saw, for both the ADL and the Markdown form.
func FuzzDeferredReference(f *testing.F) {
	f.Add(`ASSERT(DEFER synthesis REFLECTS "There are 28 namespaces including kube-system, kubemoot, observability.")`)
	f.Add("# Helm\nWhich releases?\n\n```reflects\nNames the \"real\" releases; C:\\charts is not one.\n```\n")
	f.Add("# Two\nq\n```grounded extra words\nfirst\n```\n```reflects\nsecond\n```\n")
	f.Fuzz(func(t *testing.T, content string) {
		ft := fitnessscript.ParseFitnessTest(content)
		var td transcriptDoc
		for _, a := range ft.Assertions {
			td.Assertions = append(td.Assertions, transcriptAssertion{Raw: a.Raw})
		}
		for _, a := range ft.Assertions {
			if a.Kind != fitnessscript.KindDeferred || a.Keyword == "" {
				continue
			}
			if got := referenceForKeyword(td, a.Keyword); got != firstReference(ft, a.Keyword) {
				t.Fatalf("judge reads reference %q for %s, scenario parsed %q", got, a.Keyword, firstReference(ft, a.Keyword))
			}
		}
	})
}

// firstReference is the reference of the scenario's first DEFER for keyword.
func firstReference(ft fitnessscript.FitnessTest, keyword string) string {
	for _, a := range ft.Assertions {
		if a.Kind == fitnessscript.KindDeferred && a.Keyword == keyword {
			return a.Reference
		}
	}
	return ""
}
