package controller

import (
	"bytes"
	"context"
	"math"
	"testing"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// TestCorrectnessAndEfficiencyScores pins the two simplest sub-scores, including
// the unknown-input edge cases (zero total, zero duration) that must not divide
// by zero or score a missing measurement as perfect.
func TestCorrectnessAndEfficiencyScores(t *testing.T) {
	if got := correctnessScore(5, 5); !approx(got, 100) {
		t.Errorf("correctness 5/5 = %v, want 100", got)
	}
	if got := correctnessScore(1, 4); !approx(got, 25) {
		t.Errorf("correctness 1/4 = %v, want 25", got)
	}
	if got := correctnessScore(0, 0); !approx(got, 0) {
		t.Errorf("correctness 0/0 = %v, want 0 (no assertions ≠ perfect)", got)
	}
	if got := efficiencyScore(30000); !approx(got, 100) {
		t.Errorf("efficiency under budget = %v, want 100", got)
	}
	if got := efficiencyScore(120000); !approx(got, 50) {
		t.Errorf("efficiency 2× budget = %v, want 50", got)
	}
	if got := efficiencyScore(0); !approx(got, 0) {
		t.Errorf("efficiency unknown duration = %v, want 0", got)
	}
}

// TestAdherenceScore covers protocol hygiene: a clean run (well-formed findings,
// synthesis, done, no error) is 100; missing pieces lower it; no events is 0
// (the protocol is unobservable, not perfect).
func TestAdherenceScore(t *testing.T) {
	clean := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU, Signal: testStandAside, StoodAside: true},
		{Type: testSynthesis, Content: testTheAnswer},
		{Type: testDone},
	}
	if got := adherenceScore(clean); !approx(got, 100) {
		t.Errorf("clean run adherence = %v, want 100", got)
	}
	if got := adherenceScore(nil); !approx(got, 0) {
		t.Errorf("no events adherence = %v, want 0", got)
	}
	// One malformed finding (empty signal, not stood aside) → findings component
	// 0.5; synthesis+done present, no error → (0.5+1+1+1)/4*100 = 87.5.
	mixed := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU}, // malformed: no signal, not stood aside
		{Type: testSynthesis, Content: "x"},
		{Type: testDone},
	}
	if got := adherenceScore(mixed); !approx(got, 87.5) {
		t.Errorf("mixed run adherence = %v, want 87.5", got)
	}
	// An error event drags the error component to 0.
	errored := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testSynthesis, Content: "x"},
		{Type: testDone},
		{Type: "status", Error: "provider timeout"},
	}
	if got := adherenceScore(errored); !approx(got, 75) {
		t.Errorf("errored run adherence = %v, want 75 (error component zeroed)", got)
	}
}

// selectivityScore = contributing agents / responding agents (0-100). A run where
// no agent was woken (answer-directly) scores 100 - nothing irrelevant woken.
func TestSelectivityScore(t *testing.T) {
	// 2 contribute, 3 stand aside -> 2/5 = 40 (over-waking).
	overWake := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU, Signal: testConcern},
		{Type: testFinding, Agent: "prox", Signal: testStandAside, StoodAside: true},
		{Type: testFinding, Agent: "obs", Signal: testStandAside, StoodAside: true},
		{Type: testFinding, Agent: "net", StoodAside: true},
	}
	if got := selectivityScore(overWake); !approx(got, 40) {
		t.Errorf("over-wake selectivity = %v, want 40", got)
	}
	// All three woken agents contribute -> 100 (precise selection).
	focused := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU, Signal: testAgree},
		{Type: testFinding, Agent: "prox", Signal: testConcern},
		{Type: testSynthesis, Content: "x"},
	}
	if got := selectivityScore(focused); !approx(got, 100) {
		t.Errorf("focused selectivity = %v, want 100", got)
	}
	// Answer-directly: no findings at all -> nobody irrelevant woken -> 100.
	answerDirectly := []transcriptEvent{
		{Type: testSynthesis, Content: "the capital is Paris"},
		{Type: testDone},
	}
	if got := selectivityScore(answerDirectly); !approx(got, 100) {
		t.Errorf("answer-directly selectivity = %v, want 100", got)
	}
	if got := selectivityScore(nil); !approx(got, 100) {
		t.Errorf("no-events selectivity = %v, want 100", got)
	}
	// A single agent that stands aside while another contributes -> 1/2 = 50.
	mixed := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU, Signal: testStandAside, StoodAside: true},
	}
	if got := selectivityScore(mixed); !approx(got, 50) {
		t.Errorf("mixed selectivity = %v, want 50", got)
	}
}

// agreeCountFromEvents counts agree FINDINGS (not distinct agents), matching the
// runner's countAgreeSignals so participation is scored against the same N the
// floor used. Non-agree signals and non-finding events are ignored.
// See [[Persist Consensus Signals in Transcripts]].
func TestAgreeCountFromEvents(t *testing.T) {
	events := []transcriptEvent{
		{Type: testFinding, Agent: testK8s, Signal: testAgree},
		{Type: testFinding, Agent: testGPU, Signal: testAgree},
		{Type: testFinding, Agent: testK8s, Signal: testAgree}, // same agent again: still counts (matches the floor's total count)
		{Type: testFinding, Agent: "net", Signal: testConcern}, // not an agree
		{Type: "phase", Agent: "x", Signal: testAgree},         // not a finding
	}
	if got := agreeCountFromEvents(events); got != 3 {
		t.Errorf("agreeCountFromEvents = %d, want 3 (total agree findings)", got)
	}
	if got := agreeCountFromEvents(nil); got != 0 {
		t.Errorf("agreeCountFromEvents(nil) = %d, want 0", got)
	}
}

// The participation measure is now real (agree-depth), so it must carry weight,
// and the rubric must still sum to 1.0.
func TestParticipationWeightEnabled(t *testing.T) {
	w := defaultRubricWeights()
	if w.Participation <= 0 {
		t.Errorf("participation weight = %v, want > 0", w.Participation)
	}
	sum := w.Quality + w.Reliability + w.Participation + w.Consistency + w.Efficiency
	if !approx(sum, 1.0) {
		t.Errorf("rubric weights sum = %v, want 1.0", sum)
	}
}

// TestLexicalSelfConsistency: identical answers cluster at 100, disjoint answers
// near 0, and fewer than two usable texts is 100 (nothing to contradict).
func TestLexicalSelfConsistency(t *testing.T) {
	same := []string{"gpu utilization is high on rig0", "gpu utilization is high on rig0"}
	if got := lexicalSelfConsistency(same); !approx(got, 100) {
		t.Errorf("identical texts consistency = %v, want 100", got)
	}
	disjoint := []string{"alpha beta gamma", "delta epsilon zeta"}
	if got := lexicalSelfConsistency(disjoint); got > 1 {
		t.Errorf("disjoint texts consistency = %v, want ~0", got)
	}
	if got := lexicalSelfConsistency([]string{"only one"}); !approx(got, 100) {
		t.Errorf("single text consistency = %v, want 100", got)
	}
	if got := lexicalSelfConsistency(nil); !approx(got, 100) {
		t.Errorf("no texts consistency = %v, want 100", got)
	}
}

// fakeEmbedder maps text → a deterministic vector so semantic-consistency tests
// don't need a live Ollama. Identical text → identical vector (cosine 1).
type fakeEmbedder struct{ vecs map[string][]float64 }

func (e fakeEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	if v, ok := e.vecs[text]; ok {
		return v, nil
	}
	return []float64{0, 0, 1}, nil // default orthogonal-ish vector
}

// TestCosineVec pins vector cosine, including the zero/mismatch guards.
func TestCosineVec(t *testing.T) {
	if got := cosineVec([]float64{1, 0}, []float64{1, 0}); !approx(got, 1) {
		t.Errorf("identical vecs cosine = %v, want 1", got)
	}
	if got := cosineVec([]float64{1, 0}, []float64{0, 1}); !approx(got, 0) {
		t.Errorf("orthogonal vecs cosine = %v, want 0", got)
	}
	if got := cosineVec([]float64{1, 0}, []float64{1}); got != 0 {
		t.Errorf("length-mismatch cosine = %v, want 0", got)
	}
	if got := cosineVec([]float64{0, 0}, []float64{1, 1}); got != 0 {
		t.Errorf("zero vector cosine = %v, want 0", got)
	}
}

// TestSemanticConsistency: identical answers (same vector) → 100; semantically
// opposite (orthogonal vectors) → ~0; <2 texts → 100. This is the upgrade that
// stops penalizing a live metric whose number changes but whose meaning doesn't.
func TestSemanticConsistency(t *testing.T) {
	ctx := context.Background()
	emb := fakeEmbedder{vecs: map[string][]float64{
		testGPUAt41:               {1, 0, 0},
		"gpu at 76%":              {1, 0, 0}, // same meaning → same vector
		"totally different topic": {0, 1, 0},
	}}
	if got, _ := semanticConsistency(ctx, []string{testGPUAt41, "gpu at 76%"}, emb); !approx(got, 100) {
		t.Errorf("same-meaning different-number consistency = %v, want 100", got)
	}
	if got, _ := semanticConsistency(ctx, []string{testGPUAt41, "totally different topic"}, emb); got > 1 {
		t.Errorf("different-meaning consistency = %v, want ~0", got)
	}
	if got, _ := semanticConsistency(ctx, []string{"only one"}, emb); !approx(got, 100) {
		t.Errorf("single text consistency = %v, want 100", got)
	}
}

// TestConsistencyOverrideUsedInBuild verifies the semantic override is what the
// workbook uses for self_consistency (Scenarios!M), not the lexical fallback.
func TestConsistencyOverrideUsedInBuild(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "ov-suite", Namespace: testCrewTest},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: testABC, Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	// Two answers with NO lexical overlap (lexical would score ~0), but we inject
	// a semantic override of 92 — the build must use 92.
	results := []IterationResult{
		{Scenario: testGPUUtilization, Iteration: 1, DurationMs: 1000, Phase: kubemootv1alpha1.CrewFitnessPhasePassed,
			AssertionsPassed: 1, AssertionsTotal: 1, Synthesis: "alpha beta", Correctness: 100, Adherence: 100, Efficiency: 100},
		{Scenario: testGPUUtilization, Iteration: 2, DurationMs: 1000, Phase: kubemootv1alpha1.CrewFitnessPhasePassed,
			AssertionsPassed: 1, AssertionsTotal: 1, Synthesis: "gamma delta", Correctness: 100, Adherence: 100, Efficiency: 100},
	}
	b, err := BuildFitnessSuiteXLSXWithMeasures(suite, results, map[string]float64{testGPUUtilization: 92}, nil)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	if v, _ := f.GetCellValue(testSheetScenarios, "M2"); v != "92.0" {
		t.Errorf("Scenarios M2 (self_consistency) = %q, want \"92.0\" (semantic override, not lexical)", v)
	}
}

// gradeInput builds scenarioGrade's input in the rubric's positional order, with
// consistency held at 100 (the grade tests vary the other measures).
func gradeInput(quality, reliability, factuality, participation, efficiency, fabFraction float64) gradeMeasures {
	return gradeMeasures{
		quality: quality, reliability: reliability, factuality: factuality, fabFraction: fabFraction,
		participation: participation, consistency: 100, efficiency: efficiency,
	}
}

// TestGradeComposition checks the weighted-mean grade, including weight
// normalization (weights need not sum to 1) and zeroing a measure out.
// NOTE: these weights are deliberately NOT defaultRubricWeights() (participation
// is 0 here): this test exercises the grade arithmetic in isolation, not the
// production rubric. See TestParticipationWeightEnabled for the live weights.
func TestGradeComposition(t *testing.T) {
	w := rubricWeights{Quality: 0.45, Reliability: 0.25, Participation: 0, Consistency: 0.15, Efficiency: 0.15}
	// gradeInput(quality, reliability, factuality, participation, efficiency,
	// fabFraction). factuality=-1 (no facts authored) excludes the
	// term, so these legacy cases match the pre-factuality grade arithmetic.
	// All measures 100 → grade 100 regardless of weights.
	if got := scenarioGrade(gradeInput(100, 100, -1, 100, 100, 0), w); !approx(got, 100) {
		t.Errorf("all-100 scenario grade = %v, want 100", got)
	}
	// Weight normalization (weights need not sum to 1): efficiency 0, rest 100 →
	// (100*.45+100*.25+0+100*.15+0*.15)/(.45+.25+0+.15+.15) = 85/1.0 = 85.
	if got := scenarioGrade(gradeInput(100, 100, -1, 100, 0, 0), w); !approx(got, 85) {
		t.Errorf("efficiency-0 scenario grade = %v, want 85", got)
	}
	// Zero all weights → 0, no divide-by-zero.
	if got := weightedGrade(gradeTerm{100, 0}, gradeTerm{100, 0}); !approx(got, 0) {
		t.Errorf("zero-weight grade = %v, want 0", got)
	}
	// Quality leads: a zero on quality (weight .45) drags the grade more than a
	// zero on efficiency (weight .15).
	lowQuality := scenarioGrade(gradeInput(0, 100, -1, 100, 100, 0), w)
	lowEfficiency := scenarioGrade(gradeInput(100, 100, -1, 100, 0, 0), w)
	if lowQuality >= lowEfficiency {
		t.Errorf("low quality should hurt more than low efficiency: q=%v e=%v", lowQuality, lowEfficiency)
	}
	// Reliability counts: a scenario that fully passed (reliability 100) scores
	// higher than one that didn't (reliability 0), all else equal.
	hi := scenarioGrade(gradeInput(100, 100, -1, 100, 100, 0), w)
	lo := scenarioGrade(gradeInput(100, 0, -1, 100, 100, 0), w)
	if hi <= lo {
		t.Errorf("reliability should raise the grade: reliable=%v unreliable=%v", hi, lo)
	}
	// Participation counts: weight it, and a crew that didn't deliberate
	// (participation 0) scores below one that did, all else equal.
	wp := rubricWeights{Quality: 0.40, Reliability: 0.20, Participation: 0.15, Consistency: 0.10, Efficiency: 0.15}
	engaged := scenarioGrade(gradeInput(100, 100, -1, 100, 100, 0), wp)
	silent := scenarioGrade(gradeInput(100, 100, -1, 0, 100, 0), wp)
	if engaged <= silent {
		t.Errorf("participation should raise the grade: engaged=%v silent=%v", engaged, silent)
	}
}

// TestGradeFactualityAndFabrication pins the new ground-truth behavior: factuality
// is excluded until authored, a low factuality drags the grade once authored, and
// a fabrication heavily but not fatally discounts it.
func TestGradeFactualityAndFabrication(t *testing.T) {
	w := defaultRubricWeights()

	// Inert: factuality=-1 (no facts authored) gives the same grade as before, so
	// existing scenarios are unaffected until they declare facts.
	base := scenarioGrade(gradeInput(80, 100, -1, 100, 100, 0), w)
	withFactExcluded := scenarioGrade(gradeInput(80, 100, -1, 100, 100, 0), w)
	if !approx(base, withFactExcluded) {
		t.Errorf("factuality=-1 must be inert: %v vs %v", base, withFactExcluded)
	}

	// Once authored, a factual answer (100) outscores a false one (0), all else equal.
	factual := scenarioGrade(gradeInput(80, 100, 100, 100, 100, 0), w)
	false0 := scenarioGrade(gradeInput(80, 100, 0, 100, 100, 0), w)
	if factual <= false0 {
		t.Errorf("high factuality should raise the grade: factual=%v false=%v", factual, false0)
	}

	// A fabrication (forbidden claim asserted) heavily discounts the grade but does
	// NOT zero it (penalize, not hard-fail): the floor keeps >= 35% of the base.
	clean := scenarioGrade(gradeInput(80, 100, 100, 100, 100, 0), w)
	fabricated := scenarioGrade(gradeInput(80, 100, 100, 100, 100, 1), w)
	if fabricated >= clean {
		t.Errorf("fabrication must discount the grade: clean=%v fabricated=%v", clean, fabricated)
	}
	if fabricated <= 0 || !approx(fabricated, clean*fabricationFloor) {
		t.Errorf("fabrication penalty floor: got %v, want %v (clean*%v)", fabricated, clean*fabricationFloor, fabricationFloor)
	}
}

// TestIterationFromTranscriptComputesMeasures verifies the report path turns a
// stored RunOutcome (with events) into per-run sub-scores: correctness from the
// assertions, efficiency from the duration, adherence + synthesis from the
// event stream.
func TestIterationFromTranscriptComputesMeasures(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: testGPUUtilization}},
		},
	}
	data := []byte(`{
		"assertions":[{"raw":"a","passed":true,"message":""},{"raw":"b","passed":true,"message":""}],
		"events":[
			{"type":"finding","agent":"gpu","signal":"agree"},
			{"type":"synthesis","content":"gpu utilization is high"},
			{"type":"done"}
		],
		"durationMs":30000,
		"startedAt":"2026-06-01T12:00:00Z"
	}`)
	ir, ok := iterationFromTranscript(suite, "ns/suite/run/s0-i3.json", data)
	if !ok {
		t.Fatal("iterationFromTranscript returned ok=false")
	}
	if ir.Scenario != testGPUUtilization || ir.Iteration != 3 {
		t.Errorf("scenario/iter = (%q,%d), want (gpu-utilization,3)", ir.Scenario, ir.Iteration)
	}
	if !approx(ir.Correctness, 100) {
		t.Errorf("correctness = %v, want 100", ir.Correctness)
	}
	if !approx(ir.Efficiency, 100) {
		t.Errorf("efficiency (30s < 60s budget) = %v, want 100", ir.Efficiency)
	}
	if !approx(ir.Adherence, 100) {
		t.Errorf("adherence (clean run) = %v, want 100", ir.Adherence)
	}
	if ir.Synthesis != "gpu utilization is high" {
		t.Errorf("synthesis = %q, want the synthesis event content", ir.Synthesis)
	}
}

// TestBuildXLSXEmitsGradeColumns is the end-to-end check that the data columns
// and the Overview scorecard (Rubric folded in) are present, that the grade is
// quality-led (an unjudged perfect run can't score 100 because quality carries
// the largest weight), and that supplying a REFLECTS quality score folds in.
func TestBuildXLSXEmitsGradeColumns(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "grade-suite", Namespace: testCrewTest},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "feedface", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	// A perfect mechanical run: 5/5 assertions, fast, clean protocol, both passed.
	results := []IterationResult{
		{Scenario: testGPUUtilization, Iteration: 1, DurationMs: 1000,
			Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 5, AssertionsTotal: 5,
			Synthesis: "gpu high", Correctness: 100, Adherence: 100, Efficiency: 100,
			ConsensusOK: true, Participation: -1},
		{Scenario: testGPUUtilization, Iteration: 2, DurationMs: 1000,
			Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 5, AssertionsTotal: 5,
			Synthesis: "gpu high", Correctness: 100, Adherence: 100, Efficiency: 100,
			ConsensusOK: true, Participation: -1},
	}
	b, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("build failed: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Data columns: Runs keeps correctness/adherence/efficiency as info (no
	// per-run grade) plus selectivity (M); Scenarios carries self_consistency (M),
	// participation (N), quality (O), scenario_grade (P).
	assertHeaderCells(t, f, [][3]string{
		{testSheetRuns, "J1", "correctness"}, {testSheetRuns, "K1", "adherence"}, {testSheetRuns, "L1", "efficiency"},
		{testSheetRuns, "M1", "selectivity"},
		{testSheetScenarios, "J1", "mean_correctness"}, {testSheetScenarios, "M1", "self_consistency"},
		{testSheetScenarios, "N1", "participation"}, {testSheetScenarios, "O1", "quality"}, {testSheetScenarios, "P1", "scenario_grade"},
	})
	// The Rubric sheet is gone — folded into the Overview scorecard.
	if idx, _ := f.GetSheetIndex("Rubric"); idx != -1 {
		t.Errorf("Rubric sheet should be removed (folded into Overview), got index %d", idx)
	}
	// Overview carries the scorecard with the four measures, Quality first.
	foundScorecard, foundQuality, foundGrade := scanOverviewScorecard(f)
	if !foundScorecard || !foundQuality || !foundGrade {
		t.Errorf("Overview scorecard incomplete: scorecard=%v quality=%v grade=%v",
			foundScorecard, foundQuality, foundGrade)
	}
	// Quality-led grade: an unjudged perfect run (quality 0) scores, with the
	// default weights (Q.45/R.25/P0/C.15/E.15 — participation graded at weight 0),
	// (0*.45 + 100*.25 + 100*.15 + 100*.15)/1.0 = 55, NOT 100. scenario_grade is a
	// Go-computed value (no cross-sheet formula).
	if v, _ := f.GetCellValue(testSheetScenarios, "P2"); v != "55.0" {
		t.Errorf("Scenarios P2 (scenario_grade, quality 0) = %q, want \"55.0\"", v)
	}

	assertGradeWithQuality100(t, suite, results)
}

// assertGradeWithQuality100 checks that a REFLECTS quality score of 100 lifts the scenario grade to 100.
func assertGradeWithQuality100(t *testing.T, suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult) {
	t.Helper()
	// With a REFLECTS quality score of 100, the grade reaches 100.
	b2, err := BuildFitnessSuiteXLSXWithMeasures(suite, results, nil, map[string]float64{testGPUUtilization: 100})
	if err != nil {
		t.Fatalf("build with measures failed: %v", err)
	}
	f2, err := excelize.OpenReader(bytes.NewReader(b2))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f2.Close() }()
	if v, _ := f2.GetCellValue(testSheetScenarios, "P2"); v != "100.0" {
		t.Errorf("Scenarios P2 (scenario_grade, quality 100) = %q, want \"100.0\"", v)
	}
	if v, _ := f2.GetCellValue(testSheetScenarios, "O2"); v != "100.0" {
		t.Errorf("Scenarios O2 (quality) = %q, want \"100.0\"", v)
	}
}

// assertHeaderCells checks each (sheet, cell, want-header) triple, failing the
// test for any mismatch.
func assertHeaderCells(t *testing.T, f *excelize.File, cells [][3]string) {
	t.Helper()
	for _, hc := range cells {
		if v, _ := f.GetCellValue(hc[0], hc[1]); v != hc[2] {
			t.Errorf("%s!%s = %q, want %q", hc[0], hc[1], v, hc[2])
		}
	}
}

// scanOverviewScorecard walks the Overview sheet's column A looking for the
// scorecard title, the Quality measure row, and the crew-grade row.
func scanOverviewScorecard(f *excelize.File) (foundScorecard, foundQuality, foundGrade bool) {
	for r := 1; r <= 60; r++ {
		switch v, _ := f.GetCellValue("Overview", "A"+itoa(r)); v {
		case "Scorecard — how the crew grade is computed":
			foundScorecard = true
		case "Quality":
			foundQuality = true
		case "Crew grade (0-100)":
			foundGrade = true
		}
	}
	return foundScorecard, foundQuality, foundGrade
}
