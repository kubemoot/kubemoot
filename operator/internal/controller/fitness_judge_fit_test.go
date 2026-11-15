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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	fitQuestion  = "Which VMs use the most memory?"
	fitReference = "names the top VMs by memory"
	fitSynthesis = "vm-101 uses 12 GiB and vm-204 uses 9 GiB."
	fitShortItem = "short item"
)

// proxmoxRows is evidence like a hypervisor's per-VM RRD listing: many short rows.
func proxmoxRows(rows int) string {
	var b strings.Builder
	b.WriteString("proxmox-qm: vm list with rrd samples\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, `{"vmid":%d,"name":"vm-%d","mem":%d,"maxmem":17179869184,"cpu":0.0%d,"netin":%d}`+"\n", i, i, i*1048576, i%10, i*977)
	}
	b.WriteString("total: " + fmt.Sprint(rows) + " vms")
	return b.String()
}

func fitDoc(evidence ...string) judgeComparisonDoc {
	return judgeComparisonDoc{
		Question: fitQuestion, Reference: fitReference, Total: 2,
		Answers: []judgeAnswer{
			{Synthesis: fitSynthesis, Evidence: evidence},
			{Synthesis: "second answer", Evidence: []string{"short finding"}},
		},
	}
}

func TestJudgeDocBudgetChars(t *testing.T) {
	if _, ok := judgeDocBudgetChars(0); ok {
		t.Error("an unknown window must give no budget")
	}
	if _, ok := judgeDocBudgetChars(-5); ok {
		t.Error("a negative window must give no budget")
	}
	// 32768 tokens: reserve 4096, prompt share 8192, 20480 tokens left at 3 chars each.
	if got, ok := judgeDocBudgetChars(32768); !ok || got != 61440 {
		t.Errorf("budget(32768) = %d,%v; want 61440,true", got, ok)
	}
	// A window under the minimum reserve still gives a usable (positive) budget.
	if got, ok := judgeDocBudgetChars(1000); !ok || got < 1 {
		t.Errorf("budget(1000) = %d,%v; want a positive budget", got, ok)
	}
}

func TestMarshalFittedLeavesAFittingDocumentAlone(t *testing.T) {
	cd := fitDoc("small evidence")
	plain, _ := json.Marshal(cd)
	for _, budget := range []int{0, -1, len(plain), len(plain) + 100} {
		got, err := marshalFitted(cd, budget)
		if err != nil || got != string(plain) {
			t.Errorf("budget %d: changed a document that fits (err %v)", budget, err)
		}
	}
}

// The 77 KB proxmox-vm-list case: the evidence is cut, the claims are not.
func TestMarshalFittedCutsBulkEvidenceAndKeepsTheClaims(t *testing.T) {
	cd := fitDoc(proxmoxRows(900))
	plain, _ := json.Marshal(cd)
	if len(plain) < 77_000 {
		t.Fatalf("test document is %d bytes, want at least 77000", len(plain))
	}
	budget, _ := judgeDocBudgetChars(16384)
	got, err := marshalFitted(cd, budget)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > budget {
		t.Errorf("fitted document is %d chars, budget %d", len(got), budget)
	}
	var parsed judgeComparisonDoc
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("fitted document is not valid JSON: %v", err)
	}
	assertClaimsKept(t, parsed)
	assertEvidenceCut(t, parsed)
}

// assertClaimsKept checks that the question, reference and syntheses came through whole.
func assertClaimsKept(t *testing.T, parsed judgeComparisonDoc) {
	t.Helper()
	if parsed.Question != fitQuestion || parsed.Reference != fitReference || parsed.Total != 2 {
		t.Errorf("question, reference or total changed: %+v", parsed)
	}
	if parsed.Answers[0].Synthesis != fitSynthesis || parsed.Answers[1].Synthesis != "second answer" {
		t.Error("a synthesis changed")
	}
}

// assertEvidenceCut checks that the long item was cut at both ends and marked, and the short one kept.
func assertEvidenceCut(t *testing.T, parsed judgeComparisonDoc) {
	t.Helper()
	if parsed.Answers[1].Evidence[0] != "short finding" {
		t.Errorf("a short evidence item was cut: %q", parsed.Answers[1].Evidence[0])
	}
	cut := parsed.Answers[0].Evidence[0]
	if !strings.HasPrefix(cut, "proxmox-qm: vm list") || !strings.HasSuffix(cut, "total: 900 vms") {
		t.Errorf("the cut item lost its start or end")
	}
	if !strings.Contains(cut, "characters of evidence omitted") {
		t.Error("the cut is not marked in the item")
	}
	if parsed.EvidenceTrimmed == nil || parsed.EvidenceTrimmed.Items != 1 || parsed.EvidenceTrimmed.CharsOmitted <= 0 {
		t.Errorf("evidenceTrimmed = %+v, want one item cut", parsed.EvidenceTrimmed)
	}
}

func TestMarshalFittedSharesTheRoomAcrossItems(t *testing.T) {
	cd := fitDoc(strings.Repeat("a", 30_000), strings.Repeat("b", 30_000), "tiny")
	got, err := marshalFitted(cd, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 10_000 {
		t.Fatalf("fitted document is %d chars, budget 10000", len(got))
	}
	var parsed judgeComparisonDoc
	_ = json.Unmarshal([]byte(got), &parsed)
	ev := parsed.Answers[0].Evidence
	if ev[2] != "tiny" || !strings.Contains(ev[0], "a") || !strings.Contains(ev[1], "b") {
		t.Errorf("items were not cut evenly or the short one changed: %d, %d, %q", len(ev[0]), len(ev[1]), ev[2])
	}
	if d := len(ev[0]) - len(ev[1]); d < -50 || d > 50 {
		t.Errorf("two equal items were cut unequally: %d and %d", len(ev[0]), len(ev[1]))
	}
}

func TestMarshalFittedKeepsClaimsWhenTheyAloneExceedTheBudget(t *testing.T) {
	cd := fitDoc(proxmoxRows(50))
	cd.Answers[0].Synthesis = strings.Repeat("claim ", 2000)
	got, err := marshalFitted(cd, 500)
	if err != nil {
		t.Fatal(err)
	}
	var parsed judgeComparisonDoc
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Answers[0].Synthesis != cd.Answers[0].Synthesis {
		t.Error("a synthesis was cut")
	}
	if !strings.Contains(parsed.Answers[0].Evidence[0], "omitted") {
		t.Errorf("evidence not reduced to the omission note: %q", parsed.Answers[0].Evidence[0])
	}
}

func TestMarshalFittedHandlesNoAnswersAndNoEvidence(t *testing.T) {
	for _, cd := range []judgeComparisonDoc{
		{Question: strings.Repeat("q", 900), Answers: []judgeAnswer{}},
		{Question: "q", Answers: []judgeAnswer{{Synthesis: strings.Repeat("s", 900)}}},
	} {
		got, err := marshalFitted(cd, 100)
		if err != nil || !json.Valid([]byte(got)) {
			t.Errorf("document with nothing to cut: err %v, valid %v", err, json.Valid([]byte(got)))
		}
	}
}

func TestCutEvidenceNeverSplitsACharacter(t *testing.T) {
	item := strings.Repeat("memoria éè中文 ", 400)
	for limit := judgeMinEvidenceKeepChars; limit < 400; limit += 7 {
		cut, omitted := cutEvidence(item, limit)
		if !utf8.ValidString(cut) {
			t.Fatalf("limit %d: the cut item is not valid UTF-8", limit)
		}
		if omitted <= 0 {
			t.Fatalf("limit %d: nothing omitted from an item longer than the limit", limit)
		}
	}
}

func TestCutEvidenceLimits(t *testing.T) {
	if got, n := cutEvidence(fitShortItem, -1); got != fitShortItem || n != 0 {
		t.Errorf("a negative limit must keep the item: %q %d", got, n)
	}
	if got, n := cutEvidence(fitShortItem, len(fitShortItem)); got != fitShortItem || n != 0 {
		t.Errorf("an item at the limit must stay whole: %q %d", got, n)
	}
	got, n := cutEvidence(strings.Repeat("x", 1000), 10)
	if n != 1000 || !strings.Contains(got, "1000 characters of evidence omitted") {
		t.Errorf("a limit below the minimum must leave only the note: %q %d", got, n)
	}
}

func TestEvidenceCap(t *testing.T) {
	items := []string{strings.Repeat("a", 10), strings.Repeat("b", 100), strings.Repeat("c", 100)}
	if got := evidenceCap(items, 1000); got != -1 {
		t.Errorf("cap with room to spare = %d, want -1 (no cut)", got)
	}
	// 10 stays whole; the other 90 are shared by two items: 45 each.
	if got := evidenceCap(items, 100); got != 45 {
		t.Errorf("cap = %d, want 45", got)
	}
	if got := evidenceCap(nil, 100); got != -1 {
		t.Errorf("cap of no items = %d, want -1", got)
	}
}

func TestSmallerWindowIgnoresUnknown(t *testing.T) {
	cases := []struct{ a, b, want int }{{0, 0, 0}, {0, 8, 8}, {8, 0, 8}, {8, 4, 4}, {-1, 4, 4}, {4, -1, 4}, {-1, -1, 0}}
	for _, c := range cases {
		if got := smallerWindow(c.a, c.b); got != c.want {
			t.Errorf("smallerWindow(%d,%d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func windowedEntry(name string, window int, abilities ...string) aiv1alpha1.CatalogModel {
	e := catEntryOf(name, aiv1alpha1.SizeLarge, abilities...)
	e.ContextLength = window
	return e
}

// The judge crew's window is the smallest one declared by a model its agents may run.
func TestJudgeCrewWindowIsTheSmallestAdmittedModel(t *testing.T) {
	server := opServer(selOllama, aiv1alpha1.ServerTypeOllama,
		windowedEntry("judge-a", 32768, testToolCalling),
		windowedEntry("judge-b", 16384, testToolCalling),
		windowedEntry("no-tools", 4096), // fails the rule's require
		windowedEntry("unsized", 0, testToolCalling))
	_, cli := catalogReconciler(t, server, pilotPolicy(), pilotAgent("judge"), pilotAgent("coordinator"))
	crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: selCrew, Namespace: selNS}}
	if got := judgeCrewWindow(context.Background(), cli, crew); got != 16384 {
		t.Errorf("window = %d, want 16384", got)
	}
}

func TestJudgeCrewWindowIsUnknownWithoutDeclaredWindows(t *testing.T) {
	server := opServer(selOllama, aiv1alpha1.ServerTypeOllama, windowedEntry("judge-a", 0, testToolCalling))
	_, cli := catalogReconciler(t, server, pilotPolicy(), pilotAgent("judge"))
	crew := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: selCrew, Namespace: selNS}}
	if got := judgeCrewWindow(context.Background(), cli, crew); got != 0 {
		t.Errorf("window = %d, want 0 (unknown)", got)
	}
	empty := &aiv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "no-agents", Namespace: selNS}}
	if got := judgeCrewWindow(context.Background(), cli, empty); got != 0 {
		t.Errorf("a crew with no agents has window %d, want 0", got)
	}
}

// buildComparisonDoc cuts the evidence of a large transcript to the judge's window and
// leaves it whole when the window is unknown.
func TestBuildComparisonDocFitsTheJudgeWindow(t *testing.T) {
	rows, _ := json.Marshal(proxmoxRows(900))
	transcript := `{"question":"` + fitQuestion + `","assertions":[{"raw":"DEFER synthesis REFLECTS \"` + fitReference + `\"","passed":true}],` +
		`"events":[{"type":"finding","agent":"proxmox-qm","signal":"agree","content":` + string(rows) + `},` +
		`{"type":"synthesis","content":"` + fitSynthesis + `"},{"type":"done"}]}`
	store := fakeStore{objs: map[string][]byte{testRunPrefix + "s0-i1.json": []byte(transcript)}}

	whole, _, _, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 0)
	if err != nil || len(whole) < 77_000 {
		t.Fatalf("unknown window: err %v, document %d bytes; want the whole 77 KB document", err, len(whole))
	}
	budget, _ := judgeDocBudgetChars(16384)
	fitted, total, answers, err := buildComparisonDoc(store, testRunPrefix, 0, testVerdictReflects, 16384)
	if err != nil || total != 1 || answers != 1 {
		t.Fatalf("fitted: err %v total %d answers %d", err, total, answers)
	}
	if len(fitted) > budget {
		t.Errorf("document is %d chars, budget %d", len(fitted), budget)
	}
	if !strings.Contains(fitted, fitSynthesis) || !strings.Contains(fitted, fitReference) {
		t.Error("the claims were cut")
	}
}

func TestJudgePassFindsTheWindowOnce(t *testing.T) {
	p := newWorkerPass(t, fakeStore{objs: map[string][]byte{}}, reflectsCrew())
	if got := p.judgeWindow(context.Background(), testVerdictReflects); got != 0 {
		t.Errorf("a crew with no agents has window %d, want 0", got)
	}
	if _, kept := p.windows[testVerdictReflects]; !kept {
		t.Error("the window was not kept for the next scenario")
	}
	if got := p.judgeWindow(context.Background(), "UNKNOWN_KEYWORD"); got != 0 {
		t.Errorf("an unresolved keyword has window %d, want 0", got)
	}
}
