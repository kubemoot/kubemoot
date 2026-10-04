package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// TestSummarizeChildrenClassifiesEachTerminalPhase pins the count rollup
// the reconciler uses to know "are we done yet?" and "what was the
// pass/fail breakdown?" — both load-bearing for the suite's status.
func TestSummarizeChildrenClassifiesEachTerminalPhase(t *testing.T) {
	mk := func(phase kubemootv1alpha1.CrewFitnessPhase) kubemootv1alpha1.CrewFitness {
		return kubemootv1alpha1.CrewFitness{Status: kubemootv1alpha1.CrewFitnessStatus{Phase: phase}}
	}
	children := []kubemootv1alpha1.CrewFitness{
		mk(kubemootv1alpha1.CrewFitnessPhasePassed),
		mk(kubemootv1alpha1.CrewFitnessPhasePassed),
		mk(kubemootv1alpha1.CrewFitnessPhaseFailed),
		mk(kubemootv1alpha1.CrewFitnessPhaseError),
		mk(kubemootv1alpha1.CrewFitnessPhaseRunning),
		mk(kubemootv1alpha1.CrewFitnessPhasePending),
		mk(""), // empty == in-flight by convention
	}
	p := summarizeChildren(children)
	if p.completed != 4 {
		t.Errorf("completed: got %d, want 4 (2 passed + 1 failed + 1 errored)", p.completed)
	}
	if p.passed != 2 {
		t.Errorf("passed: got %d, want 2", p.passed)
	}
	if p.failed != 1 {
		t.Errorf("failed: got %d, want 1", p.failed)
	}
	if p.errored != 1 {
		t.Errorf("errored: got %d, want 1", p.errored)
	}
	if p.inFlight != 3 {
		t.Errorf("inFlight: got %d, want 3 (Running + Pending + empty)", p.inFlight)
	}
}

// TestIterationCRNameStable pins the per-iteration naming so reconciles
// remain idempotent — the scheduler uses "is this name in the
// previously-scheduled set?" as the dedup primitive.
func TestIterationCRNameStable(t *testing.T) {
	got1 := iterationCRName("a1b2c3d4", 0, 1)
	got2 := iterationCRName("a1b2c3d4", 0, 1)
	if got1 != got2 {
		t.Errorf("iterationCRName not stable: %q vs %q", got1, got2)
	}
	if got1 != "run-a1b2c3d4-s0-i1" {
		t.Errorf("name format: got %q, want run-a1b2c3d4-s0-i1", got1)
	}
	// Distinct script index -> distinct name.
	if iterationCRName("a1b2c3d4", 1, 1) == got1 {
		t.Errorf("different scriptIdx produced same name")
	}
	// Distinct iteration -> distinct name.
	if iterationCRName("a1b2c3d4", 0, 2) == got1 {
		t.Errorf("different iteration produced same name")
	}
}

// TestFindNextIterationOrdersScriptsThenIterations pins the scheduling
// order: iteration 1 of all scripts before iteration 2 of any. Important
// because it lets the dashboard show "we're on iteration 3 of 30" — the
// progress meaning is "all scripts at least 3 times" not "scripts[0]
// finished 3 times while others sit idle."
func TestFindNextIterationOrdersScriptsThenIterations(t *testing.T) {
	r := &CrewFitnessSuiteReconciler{}
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			Iterations: 3,
			Scripts: []kubemootv1alpha1.SuiteScript{
				{TestRef: "a", TestContent: testScriptContent},
				{TestRef: "b", TestContent: testScriptContent},
			},
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: testDeadbeef},
	}
	scheduled := map[string]bool{}

	// First call: iteration 1, script a
	expectNextIteration(t, r, suite, scheduled, "first", 1, 0)
	// Second call: iteration 1, script b (NOT iteration 2 of a)
	expectNextIteration(t, r, suite, scheduled, "second", 1, 1)
	// Third call: iteration 2, script a (only now do we move to iter 2)
	expectNextIteration(t, r, suite, scheduled, "third", 2, 0)

	// Skip ahead: schedule everything but the last
	for iter := int32(1); iter <= 3; iter++ {
		for scriptIdx := 0; scriptIdx < 2; scriptIdx++ {
			scheduled[iterationCRName(testDeadbeef, scriptIdx, iter)] = true
		}
	}
	// All scheduled — no next.
	if n := r.findNextIteration(suite, scheduled); n != nil {
		t.Errorf("expected nil when all scheduled, got %+v", n)
	}
}

// expectNextIteration asserts the next iteration findNextIteration picks and marks it scheduled.
func expectNextIteration(t *testing.T, r *CrewFitnessSuiteReconciler, suite *kubemootv1alpha1.CrewFitnessSuite,
	scheduled map[string]bool, call string, wantIter int32, wantScriptIdx int) {
	t.Helper()
	n := r.findNextIteration(suite, scheduled)
	if n == nil || n.iter != wantIter || n.scriptIdx != wantScriptIdx {
		t.Fatalf("%s: got %+v, want iter=%d scriptIdx=%d", call, n, wantIter, wantScriptIdx)
	}
	scheduled[n.crName] = true
}

// TestIterationCRHasNoTTL pins the critical invariant that per-iteration
// child CRs carry NO TTL. A TTL on children makes the suite's live-count
// progress accounting decrease as children self-delete — the count never
// reaches IterationsTotal (suite runs forever) and the XLSX harvest loses
// every result older than the TTL. Regression guard for the 2026-06-01
// 390-run baseline failure.
func TestIterationCRHasNoTTL(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: testCrewTest},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: testCrewName},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: testDeadbeef},
	}
	next := &nextIteration{
		scriptIdx: 0, iter: 1, crName: "run-deadbeef-s0-i1",
		script: kubemootv1alpha1.SuiteScript{TestRef: "x", TestContent: testScriptContent},
	}
	cf := buildIterationCR(suite, next)
	if cf.Spec.TTL != nil {
		t.Errorf("per-iteration child must have NO TTL (got %v) — TTL'd children break progress accounting + XLSX harvest", cf.Spec.TTL)
	}
	// Sanity: ownership label + content threaded through.
	if cf.Labels[suiteOwnerLabel] != "s" {
		t.Errorf("owner label: got %q, want s", cf.Labels[suiteOwnerLabel])
	}
	if cf.Spec.TestContent != testScriptContent {
		t.Errorf("testContent not threaded through: %q", cf.Spec.TestContent)
	}
}

// TestValidateSuiteSpec covers the field-required and mutual-exclusion
// rules: every script needs exactly one source, iteration count must be
// >= 1, crewRef cannot be empty.
func TestValidateSuiteSpec(t *testing.T) {
	cases := []struct {
		name    string
		suite   *kubemootv1alpha1.CrewFitnessSuite
		wantErr string
	}{
		{
			name: "missing crewRef",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					Iterations: 1,
					Scripts:    []kubemootv1alpha1.SuiteScript{{TestRef: "x", TestContent: "a"}},
				},
			},
			wantErr: "crewRef",
		},
		{
			name: "zero iterations",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					CrewRef: testCrewName, Iterations: 0,
					Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "x", TestContent: "a"}},
				},
			},
			wantErr: "iterations",
		},
		{
			name: "empty scripts",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					CrewRef: testCrewName, Iterations: 1, Scripts: []kubemootv1alpha1.SuiteScript{},
				},
			},
			wantErr: "scripts",
		},
		{
			name: "script missing both sources",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					CrewRef: testCrewName, Iterations: 1,
					Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "x"}},
				},
			},
			wantErr: "testContent or configMapRef",
		},
		{
			name: "script with both sources",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					CrewRef: testCrewName, Iterations: 1,
					Scripts: []kubemootv1alpha1.SuiteScript{{TestRef: "x", TestContent: "a", ConfigMapRef: "b"}},
				},
			},
			wantErr: "testContent or configMapRef",
		},
		{
			name: "valid spec",
			suite: &kubemootv1alpha1.CrewFitnessSuite{
				Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
					CrewRef: testCrewName, Iterations: 3,
					Scripts: []kubemootv1alpha1.SuiteScript{
						{TestRef: "a", TestContent: testScriptContent},
						{TestRef: "b", ConfigMapRef: "scripts-cm"},
					},
				},
			},
			wantErr: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSuiteSpec(c.suite)
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", c.wantErr)
				return
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestAdvanceRunningCompletesDespiteIterationFailures pins the execution-
// lifecycle semantics: once every iteration reaches a terminal phase the
// suite is Completed — it ran to the end — even if some iterations FAILED
// their assertions. Failed/Error suite phase is reserved for the suite
// itself failing to execute (invalid spec, can't create children), never
// for individual iteration outcomes, which live in passed/failed/errored.
// Regression guard: the dashboard previously showed a fully-run suite as
// "FAILED" merely because 36/390 iterations failed assertions.
func TestAdvanceRunningCompletesDespiteIterationFailures(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}

	const runID = "abcd1234"
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: testBaseline, Namespace: testCrewTest},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef:    testCrewName,
			Iterations: 2,
			Scripts:    []kubemootv1alpha1.SuiteScript{{TestRef: "x", TestContent: testScriptContent}},
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
			Phase:           kubemootv1alpha1.CrewFitnessSuitePhaseRunning,
			RunID:           runID,
			IterationsTotal: 2,
		},
	}

	mkChild := func(iter int32, phase kubemootv1alpha1.CrewFitnessPhase) *kubemootv1alpha1.CrewFitness {
		return &kubemootv1alpha1.CrewFitness{
			ObjectMeta: metav1.ObjectMeta{
				Name:      iterationCRName(runID, 0, iter),
				Namespace: testCrewTest,
				Labels:    map[string]string{suiteOwnerLabel: testBaseline},
			},
			Status: kubemootv1alpha1.CrewFitnessStatus{Phase: phase},
		}
	}
	// Both iterations executed: one passed, one FAILED its assertions.
	passed := mkChild(1, kubemootv1alpha1.CrewFitnessPhasePassed)
	failed := mkChild(2, kubemootv1alpha1.CrewFitnessPhaseFailed)

	cli := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(suite, passed, failed).
		WithStatusSubresource(&kubemootv1alpha1.CrewFitnessSuite{}).
		Build()
	r := &CrewFitnessSuiteReconciler{Client: cli, Scheme: scheme}

	if _, err := r.advanceRunning(context.Background(), suite); err != nil {
		t.Fatalf("advanceRunning: %v", err)
	}

	got := &kubemootv1alpha1.CrewFitnessSuite{}
	if err := cli.Get(context.Background(),
		client.ObjectKey{Namespace: testCrewTest, Name: testBaseline}, got); err != nil {
		t.Fatalf("get suite: %v", err)
	}
	if got.Status.Phase != kubemootv1alpha1.CrewFitnessSuitePhaseCompleted {
		t.Errorf("phase: got %q, want Completed (suite executed fully despite an iteration failure)", got.Status.Phase)
	}
	if got.Status.Failed != 1 {
		t.Errorf("failed count: got %d, want 1 (the failure is an OUTCOME, recorded in counts not phase)", got.Status.Failed)
	}
	if got.Status.Passed != 1 {
		t.Errorf("passed count: got %d, want 1", got.Status.Passed)
	}
}
