package controller

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func TestPassAllKPercent(t *testing.T) {
	cases := []struct {
		name           string
		passed, trials int32
		wantRate       int32
		wantAllK       int32
	}{
		{"no trials", 0, 0, 0, 0},
		{"all pass", 3, 3, 100, 100},
		{"three quarters over four trials", 3, 4, 75, 32}, // 0.75^4
		{"none pass", 0, 5, 0, 0},
		{"one of one", 1, 1, 100, 100},
		{"one of three", 1, 3, 33, 4}, // (1/3)^3
	}
	for _, c := range cases {
		if got := passRatePercent(c.passed, c.trials); got != c.wantRate {
			t.Errorf("%s: passRate = %d, want %d", c.name, got, c.wantRate)
		}
		if got := passAllKPercent(c.passed, c.trials); got != c.wantAllK {
			t.Errorf("%s: passAllK = %d, want %d", c.name, got, c.wantAllK)
		}
	}
}

func TestScenarioNeedsCheck(t *testing.T) {
	cases := []struct {
		passed, trials int32
		want           bool
	}{
		{0, 0, false}, {0, 1, false}, {0, 2, false}, // too few trials to call a pattern
		{0, 3, true}, {0, 15, true},
		{1, 15, false}, {15, 15, false},
	}
	for _, c := range cases {
		if got := scenarioNeedsCheck(c.passed, c.trials); got != c.want {
			t.Errorf("scenarioNeedsCheck(%d, %d) = %v, want %v", c.passed, c.trials, got, c.want)
		}
	}
}

func iterResults(scenario string, phases ...kubemootv1alpha1.CrewFitnessPhase) []IterationResult {
	out := make([]IterationResult, 0, len(phases))
	for i, p := range phases {
		out = append(out, IterationResult{Scenario: scenario, Iteration: int32(i + 1), Phase: p})
	}
	return out
}

func TestScenarioResultsCarryConsistency(t *testing.T) {
	passed, failed, errored := kubemootv1alpha1.CrewFitnessPhasePassed, kubemootv1alpha1.CrewFitnessPhaseFailed, kubemootv1alpha1.CrewFitnessPhaseError
	results := append(iterResults("steady", passed, passed, passed),
		iterResults("flaky", passed, failed, passed, failed)...)
	results = append(results, iterResults("broken", failed, errored, failed)...)

	rows := scenarioResults(scripts("steady", "flaky", "broken", "waiting"), results)
	if got := *rows[0].PassAllK; got != 100 || rows[0].CheckScenario {
		t.Errorf("steady: passAllK=%d check=%v, want 100 false", got, rows[0].CheckScenario)
	}
	if got := *rows[1].PassRate; got != 50 {
		t.Errorf("flaky: passRate=%d, want 50", got)
	}
	if got := *rows[1].PassAllK; got != 6 { // 0.5^4
		t.Errorf("flaky: passAllK=%d, want 6", got)
	}
	if !rows[2].CheckScenario || *rows[2].PassRate != 0 {
		t.Errorf("broken: want checkScenario and 0%% pass rate, got %+v", rows[2])
	}
	if rows[3].PassRate != nil || rows[3].PassAllK != nil || rows[3].CheckScenario {
		t.Errorf("waiting scenario has no trials, so no consistency: %+v", rows[3])
	}
}

func TestSuiteConsistency(t *testing.T) {
	rows := []kubemootv1alpha1.SuiteScenarioResult{
		{Name: "a", Iterations: 3, Passed: 3, PassAllK: ptr.To(int32(100))},
		{Name: "b", Iterations: 4, Passed: 0, PassAllK: ptr.To(int32(0)), CheckScenario: true},
		{Name: "c"}, // no trials yet: left out of the mean
	}
	got := suiteConsistency(rows, "")
	if *got.MeanPassAllK != 50 || got.ScenariosToCheck != 1 {
		t.Errorf("got mean=%d toCheck=%d, want 50 and 1", *got.MeanPassAllK, got.ScenariosToCheck)
	}
	if got.Regressions != nil {
		t.Errorf("regressions are counted only for a Regression suite, got %d", *got.Regressions)
	}
	reg := suiteConsistency(rows, kubemootv1alpha1.FitnessSuitePurposeRegression)
	if reg.Regressions == nil || *reg.Regressions != 1 {
		t.Errorf("regression suite: regressions = %v, want 1", reg.Regressions)
	}
	if suiteConsistency([]kubemootv1alpha1.SuiteScenarioResult{{Name: "c"}}, "") != nil {
		t.Error("no finished trial anywhere: consistency must be nil")
	}
}

func TestSetScenarioRollupSetsBoth(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
		Scripts: scripts("a"), Purpose: kubemootv1alpha1.FitnessSuitePurposeRegression,
	}}
	setScenarioRollup(suite, scenarioResults(suite.Spec.Scripts,
		iterResults("a", kubemootv1alpha1.CrewFitnessPhasePassed, kubemootv1alpha1.CrewFitnessPhaseFailed)))
	if len(suite.Status.Scenarios) != 1 || suite.Status.Consistency == nil || *suite.Status.Consistency.Regressions != 1 {
		t.Fatalf("rollup not stored: %+v", suite.Status)
	}
}

func TestOverviewAndScenariosReportConsistency(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: testCrewTest},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef: testCrewName, Iterations: 3, Purpose: kubemootv1alpha1.FitnessSuitePurposeCapability,
			Scripts: scripts("never"),
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	failed := kubemootv1alpha1.CrewFitnessPhaseFailed
	results := iterResults("never", failed, failed, failed)
	for i := range results {
		results[i].StartedAt = time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	}
	setScenarioRollup(suite, scenarioResults(suite.Spec.Scripts, results))
	r := &CrewFitnessSuiteReconciler{memory: &fakePurger{}}
	r.recordTrialIsolation(suite)

	b, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()

	assertOverviewRows(t, f, map[string]string{
		"Trials independent": "true (PerTrialPurge)", "Purpose": "Capability",
		"Mean pass^k (%)": "0", "Scenarios to check": "1",
	})
	rows, _ := f.GetRows(sheetScenarios)
	if len(rows) < 2 || rows[0][19] != "pass_all_k" || rows[0][20] != "check_scenario" || rows[1][20] != checkFlag {
		t.Errorf("Scenarios sheet must flag the broken scenario: %v", rows)
	}
}

// checkFlag is the check_scenario cell of a scenario that never passed.
const checkFlag = "yes"

func assertOverviewRows(t *testing.T, f *excelize.File, want map[string]string) {
	t.Helper()
	overview, _ := f.GetRows(sheetOverview)
	for _, row := range overview {
		if len(row) < 2 {
			continue
		}
		if w, ok := want[row[0]]; ok {
			if row[1] != w {
				t.Errorf("Overview %q = %q, want %q", row[0], row[1], w)
			}
			delete(want, row[0])
		}
	}
	if len(want) > 0 {
		t.Errorf("Overview rows missing: %v", want)
	}
}
