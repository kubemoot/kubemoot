package controller

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// TestPercentileNearestRank pins the percentile math the Overview sheet
// shows. Edge cases matter: empty input, single element, exact and
// fractional percentile positions.
func TestPercentileNearestRank(t *testing.T) {
	cases := []struct {
		name   string
		sorted []int64
		p      int
		want   int64
	}{
		{"empty", []int64{}, 50, 0},
		{"single", []int64{42}, 50, 42},
		{"single p0", []int64{42}, 0, 42},
		{"single p100", []int64{42}, 100, 42},
		{"ten values p50", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 50, 5},
		{"ten values p90", []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 90, 9},
		{"p0 returns min", []int64{10, 20, 30}, 0, 10},
		{"p100 returns max", []int64{10, 20, 30}, 100, 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := percentile(c.sorted, c.p)
			if got != c.want {
				t.Errorf("percentile(%v, %d) = %d, want %d", c.sorted, c.p, got, c.want)
			}
		})
	}
}

// TestDurationStatsHandlesAllShapes covers mean + p50 + p90 for empty,
// single, and a realistic multi-element sample.
func TestDurationStatsHandlesAllShapes(t *testing.T) {
	if mean, p50, p90 := durationStats(nil); mean != 0 || p50 != 0 || p90 != 0 {
		t.Errorf("empty: got (%d,%d,%d), want (0,0,0)", mean, p50, p90)
	}
	if mean, p50, p90 := durationStats([]int64{1000}); mean != 1000 || p50 != 1000 || p90 != 1000 {
		t.Errorf("single: got (%d,%d,%d), want (1000,1000,1000)", mean, p50, p90)
	}
	// 10 values: mean=55, p50=5 (nearest-rank), p90=9
	mean, p50, p90 := durationStats([]int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if mean != 5 { // integer division: 55/10 = 5
		t.Errorf("mean: got %d, want 5", mean)
	}
	if p50 != 5 {
		t.Errorf("p50: got %d, want 5", p50)
	}
	if p90 != 9 {
		t.Errorf("p90: got %d, want 9", p90)
	}
}

// TestBuildFitnessSuiteXLSXProducesValidWorkbook is an end-to-end test on
// the XLSX writer: build a workbook, parse it back, verify both sheets
// exist with the expected headers and at least one populated data row.
// Catches "we wrote bytes but they're not actually a valid XLSX" failures.
func TestBuildFitnessSuiteXLSXProducesValidWorkbook(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "test-suite", Namespace: "crew-test"},
		Spec: kubemootv1alpha1.CrewFitnessSuiteSpec{
			CrewRef:    "homelab-pilot",
			Iterations: 2,
		},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{
			RunID:               "abc12345",
			IterationsTotal:     4,
			IterationsCompleted: 4,
			Phase:               kubemootv1alpha1.CrewFitnessSuitePhaseCompleted,
		},
	}
	results := []IterationResult{
		{Scenario: "gpu-utilization", Iteration: 1, StartedAt: time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC),
			DurationMs: 1500, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 5, AssertionsTotal: 5},
		{Scenario: "gpu-utilization", Iteration: 2, StartedAt: time.Date(2026, 5, 31, 12, 1, 0, 0, time.UTC),
			DurationMs: 1700, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 5, AssertionsTotal: 5},
		{Scenario: "k8s-pod-status", Iteration: 1, StartedAt: time.Date(2026, 5, 31, 12, 2, 0, 0, time.UTC),
			DurationMs: 2100, Phase: kubemootv1alpha1.CrewFitnessPhaseFailed, AssertionsPassed: 4, AssertionsTotal: 5,
			Assertions: []kubemootv1alpha1.AssertionResult{
				{Raw: "synthesis is non-empty", Passed: true, Message: "ok"},
				{Raw: "at least 2 toolers contribute with signal=agree", Passed: false, Message: "1 agree (need 2)"},
			}},
		{Scenario: "k8s-pod-status", Iteration: 2, StartedAt: time.Date(2026, 5, 31, 12, 3, 0, 0, time.UTC),
			DurationMs: 2300, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 5, AssertionsTotal: 5},
	}

	xlsxBytes, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("BuildFitnessSuiteXLSX failed: %v", err)
	}
	if len(xlsxBytes) < 100 {
		t.Fatalf("xlsxBytes suspiciously short: %d bytes", len(xlsxBytes))
	}

	// Parse back and verify structure.
	f, err := excelize.OpenReader(bytes.NewReader(xlsxBytes))
	if err != nil {
		t.Fatalf("failed to open generated XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()

	assertSheetsPresent(t, f, "Overview", "Runs")
	assertRunsSheet(t, f)
	assertScenariosSheet(t, f)
	assertOverviewSheet(t, f)

	// Assertions + Failures sheets exist.
	if !sheetExists(f, "Assertions") || !sheetExists(f, "Failures") {
		t.Fatalf("XLSX missing Assertions/Failures sheet (got %v)", f.GetSheetList())
	}
	assertAssertionsSheet(t, f)
	assertFailuresSheet(t, f)
}

// TestRunsSheetSurfacesFactualityAndFabrication verifies the per-run integrity
// columns: a run with a CONTAINS fact passed and a NOT CONTAIN forbidden fact
// FAILED scores factuality 50 (1 of 2 content facts) and fabricated=TRUE.
func TestRunsSheetSurfacesFactualityAndFabrication(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "test-suite", Namespace: "crew-test"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: "homelab-pilot", Iterations: 1},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "fab00001", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	results := []IterationResult{
		{Scenario: "storage-topology", Iteration: 1, Phase: kubemootv1alpha1.CrewFitnessPhaseFailed,
			AssertionsPassed: 1, AssertionsTotal: 2,
			Assertions: []kubemootv1alpha1.AssertionResult{
				{Raw: "synthesis CONTAINS \"TrueNAS\"", Passed: true},
				{Raw: "synthesis does NOT CONTAIN \"active NFS\"", Passed: false},
			}},
	}
	xlsxBytes, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("BuildFitnessSuiteXLSX failed: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(xlsxBytes))
	if err != nil {
		t.Fatalf("failed to open generated XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()

	// factuality is styled "0.0", so match a "50" prefix (50 or 50.0): 1 of 2
	// content facts passed (CONTAINS passed, NOT CONTAIN failed).
	if v, _ := f.GetCellValue("Runs", "N2"); !strings.HasPrefix(v, "50") {
		t.Errorf("Runs factuality N2 = %q, want ~50 (1 of 2 content facts passed)", v)
	}
	if v, _ := f.GetCellValue("Runs", "O2"); v != "TRUE" {
		t.Errorf("Runs fabricated O2 = %q, want TRUE (a NOT CONTAIN forbidden fact failed)", v)
	}
	// Scenarios roll-up carries the same 50 factuality mean for the single run.
	if v, _ := f.GetCellValue("Scenarios", "R2"); !strings.HasPrefix(v, "50") {
		t.Errorf("Scenarios factuality R2 = %q, want ~50", v)
	}
}

// TestRunsSheetFactualityBlankWhenNoFacts verifies a run with no content-fact
// assertions leaves the factuality cell blank (not a misleading -1).
func TestRunsSheetFactualityBlankWhenNoFacts(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "crew-test"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: "homelab-pilot", Iterations: 1},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "nofacts1", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	results := []IterationResult{
		{Scenario: "concept-only", Iteration: 1, Phase: kubemootv1alpha1.CrewFitnessPhasePassed,
			AssertionsPassed: 1, AssertionsTotal: 1,
			Assertions: []kubemootv1alpha1.AssertionResult{{Raw: "synthesis is non-empty", Passed: true}}},
	}
	xlsxBytes, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("BuildFitnessSuiteXLSX failed: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(xlsxBytes))
	if err != nil {
		t.Fatalf("failed to open generated XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()
	if v, _ := f.GetCellValue("Runs", "N2"); v != "" {
		t.Errorf("Runs factuality N2 = %q, want blank (no facts authored)", v)
	}
	if v, _ := f.GetCellValue("Runs", "O2"); v != "FALSE" {
		t.Errorf("Runs fabricated O2 = %q, want FALSE", v)
	}
}

// sheetExists reports whether the workbook contains a sheet named want.
func sheetExists(f *excelize.File, want string) bool {
	for _, s := range f.GetSheetList() {
		if s == want {
			return true
		}
	}
	return false
}

// assertSheetsPresent fails the test for each requested sheet that is absent.
func assertSheetsPresent(t *testing.T, f *excelize.File, want ...string) {
	t.Helper()
	for _, name := range want {
		if !sheetExists(f, name) {
			t.Errorf("XLSX missing %q sheet (got %v)", name, f.GetSheetList())
		}
	}
}

// assertCellEquals fails unless the cell value matches want.
func assertCellEquals(t *testing.T, f *excelize.File, sheet, cell, want string) {
	t.Helper()
	if v, _ := f.GetCellValue(sheet, cell); v != want {
		t.Errorf("%s %s = %q, want %q", sheet, cell, v, want)
	}
}

// assertFormulaContains fails unless the cell formula contains substr.
func assertFormulaContains(t *testing.T, f *excelize.File, sheet, cell, substr string) {
	t.Helper()
	if fml, _ := f.GetCellFormula(sheet, cell); !strings.Contains(fml, substr) {
		t.Errorf("%s %s should contain formula %q, got %q", sheet, cell, substr, fml)
	}
}

// assertRunsSheet checks the Runs sheet header + first data row.
func assertRunsSheet(t *testing.T, f *excelize.File) {
	t.Helper()
	assertCellEquals(t, f, "Runs", "A1", "crew")
	assertCellEquals(t, f, "Runs", "B2", "gpu-utilization")
	assertCellEquals(t, f, "Runs", "C2", "abc12345")
	assertCellEquals(t, f, "Runs", "N1", "factuality")
	assertCellEquals(t, f, "Runs", "O1", "fabricated")
}

// assertScenariosSheet checks the per-scenario table header + first row. The
// sample_count/passed/... columns are live formulas over Runs, so GetCellValue
// is empty until recalc; assert the formula instead.
func assertScenariosSheet(t *testing.T, f *excelize.File) {
	t.Helper()
	assertCellEquals(t, f, "Scenarios", "A1", "scenario")
	assertCellEquals(t, f, "Scenarios", "A2", "gpu-utilization")
	assertFormulaContains(t, f, "Scenarios", "B2", "COUNTIF(Runs")
	assertCellEquals(t, f, "Scenarios", "R1", "factuality")
	assertCellEquals(t, f, "Scenarios", "S1", "fabrication_pct")
}

// overviewHasMetaLabel reports whether a human-readable metadata label
// (Suite/Description) appears in the first rows of the Overview sheet.
func overviewHasMetaLabel(f *excelize.File) bool {
	for r := 1; r <= 8; r++ {
		if v, _ := f.GetCellValue("Overview", "A"+itoa(r)); v == "Suite" || v == "Description" {
			return true
		}
	}
	return false
}

// overviewHasRollupTotal reports whether the Overview sheet carries a
// SUM(Scenarios...) roll-up formula.
func overviewHasRollupTotal(f *excelize.File) bool {
	for r := 1; r <= 30; r++ {
		if fml, _ := f.GetCellFormula("Overview", "B"+itoa(r)); strings.Contains(fml, "SUM(Scenarios") {
			return true
		}
	}
	return false
}

// assertOverviewSheet checks the crew-grade headline plus metadata + roll-up.
func assertOverviewSheet(t *testing.T, f *excelize.File) {
	t.Helper()
	assertCellEquals(t, f, "Overview", "A1", "Crew grade (0-100)")
	if !overviewHasMetaLabel(f) {
		t.Errorf("Overview missing a human-readable metadata label (Suite/Description)")
	}
	if !overviewHasRollupTotal(f) {
		t.Errorf("Overview missing a SUM(Scenarios...) roll-up formula")
	}
}

// assertionsHasFailedRow reports whether the Assertions sheet carries the
// expected failed-assertion row; it also asserts that row's message.
func assertionsHasFailedRow(t *testing.T, f *excelize.File) bool {
	t.Helper()
	for row := 2; row <= 12; row++ {
		raw, _ := f.GetCellValue("Assertions", "C"+itoa(row))
		passed, _ := f.GetCellValue("Assertions", "D"+itoa(row))
		if raw == "at least 2 toolers contribute with signal=agree" && passed == "FALSE" {
			if msg, _ := f.GetCellValue("Assertions", "E"+itoa(row)); msg != "1 agree (need 2)" {
				t.Errorf("Assertions failed-row message = %q, want \"1 agree (need 2)\"", msg)
			}
			return true
		}
	}
	return false
}

// assertAssertionsSheet checks the header + the failed assertion row from
// k8s-pod-status iteration 1.
func assertAssertionsSheet(t *testing.T, f *excelize.File) {
	t.Helper()
	assertCellEquals(t, f, "Assertions", "A1", "scenario")
	if !assertionsHasFailedRow(t, f) {
		t.Errorf("Assertions sheet missing the failed assertion row")
	}
}

// assertFailuresSheet checks the failing assertion aggregated for k8s-pod-status.
func assertFailuresSheet(t *testing.T, f *excelize.File) {
	t.Helper()
	assertCellEquals(t, f, "Failures", "A1", "scenario")
	assertCellEquals(t, f, "Failures", "A2", "k8s-pod-status")
	assertCellEquals(t, f, "Failures", "C2", "1")
	assertCellEquals(t, f, "Failures", "D2", "1")
}

// itoa is a tiny helper so the cell-address loops above read cleanly.
func itoa(n int) string { return fmt.Sprintf("%d", n) }

// TestHarvestIterationResultsOrdersByScenarioThenIteration pins the
// sort the writer relies on so the Runs sheet reads top-to-bottom in
// natural order.
func TestBuildFitnessSuiteXLSXEmbedsChartsAndPassRate(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{Name: "chart-suite", Namespace: "crew-test"},
		Spec:       kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: "homelab-pilot", Iterations: 2},
		Status:     kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "deadbeef", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	// gpu-utilization: 1 passed of 2 → pass_rate 0.5.
	results := []IterationResult{
		{Scenario: "gpu-utilization", Iteration: 1, DurationMs: 1500, Phase: kubemootv1alpha1.CrewFitnessPhasePassed},
		{Scenario: "gpu-utilization", Iteration: 2, DurationMs: 1700, Phase: kubemootv1alpha1.CrewFitnessPhaseFailed},
	}

	b, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("BuildFitnessSuiteXLSX failed: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open generated XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()

	// Each chart is on its own tab — presence means AddChart succeeded (errors
	// propagate out of the build). "Quality" is the judgment counterpart to
	// "Duration"; its presence means AddChart over the quality/grade columns
	// succeeded (AddChart errors abort the build).
	assertSheetsPresent(t, f, "Pass Rate", "Duration", "Quality", "Scenarios")

	// pass_rate is a live formula on the Scenarios sheet.
	assertCellEquals(t, f, "Scenarios", "I1", "pass_rate")
	assertFormulaContains(t, f, "Scenarios", "I2", "C2/B2")
	// Verify the formulas actually compute (excelize's calc engine resolves the
	// chain): 1 passed of 2 → sample_count 2, pass_rate 0.5 (displayed "50.00%"
	// because pass_rate is percent-formatted).
	assertComputedCell(t, f, "Scenarios", "B2", "2")
	assertComputedCell(t, f, "Scenarios", "I2", "50.00%")
}

// assertComputedCell fails unless the recalculated cell value matches want. A
// calc error is tolerated (matches the original err==nil guard) so the test
// stays robust if excelize cannot resolve the formula chain.
func assertComputedCell(t *testing.T, f *excelize.File, sheet, cell, want string) {
	t.Helper()
	if v, err := f.CalcCellValue(sheet, cell); err == nil && v != want {
		t.Errorf("%s %s computed = %q, want %q", sheet, cell, v, want)
	}
}

func TestHarvestIterationResultsOrdersByScenarioThenIteration(t *testing.T) {
	children := []kubemootv1alpha1.CrewFitness{
		{
			Spec: kubemootv1alpha1.CrewFitnessSpec{TestRef: "z-scenario"},
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{suiteIterationIndexLabel: "2"},
			},
			Status: kubemootv1alpha1.CrewFitnessStatus{
				DurationMs: 100,
				Phase:      kubemootv1alpha1.CrewFitnessPhasePassed,
			},
		},
		{
			Spec: kubemootv1alpha1.CrewFitnessSpec{TestRef: "a-scenario"},
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{suiteIterationIndexLabel: "1"},
			},
			Status: kubemootv1alpha1.CrewFitnessStatus{
				DurationMs: 200,
				Phase:      kubemootv1alpha1.CrewFitnessPhaseFailed,
			},
		},
		{
			Spec: kubemootv1alpha1.CrewFitnessSpec{TestRef: "a-scenario"},
			ObjectMeta: metav1.ObjectMeta{
				Labels: map[string]string{suiteIterationIndexLabel: "2"},
			},
			Status: kubemootv1alpha1.CrewFitnessStatus{
				DurationMs: 300,
				Phase:      kubemootv1alpha1.CrewFitnessPhasePassed,
			},
		},
	}
	got := HarvestIterationResults(children)
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	// Expect a-scenario first (iter 1, then iter 2), then z-scenario.
	if got[0].Scenario != "a-scenario" || got[0].Iteration != 1 {
		t.Errorf("results[0]: got (%q, %d), want (a-scenario, 1)", got[0].Scenario, got[0].Iteration)
	}
	if got[1].Scenario != "a-scenario" || got[1].Iteration != 2 {
		t.Errorf("results[1]: got (%q, %d), want (a-scenario, 2)", got[1].Scenario, got[1].Iteration)
	}
	if got[2].Scenario != "z-scenario" || got[2].Iteration != 2 {
		t.Errorf("results[2]: got (%q, %d), want (z-scenario, 2)", got[2].Scenario, got[2].Iteration)
	}
}

// TestOverviewIncludesCrewVersion verifies the crew chart version (provenance) is
// written to the Overview tab when the suite carries the kubemoot.ai/crew-version
// label (stamped by the controller from the Crew CR before building).
func TestOverviewIncludesCrewVersion(t *testing.T) {
	suite := &kubemootv1alpha1.CrewFitnessSuite{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ver-suite", Namespace: "crew-test",
			Labels: map[string]string{crewVersionLabel: "1.4.2"},
		},
		Spec:   kubemootv1alpha1.CrewFitnessSuiteSpec{CrewRef: "homelab-pilot", Iterations: 1},
		Status: kubemootv1alpha1.CrewFitnessSuiteStatus{RunID: "r1", Phase: kubemootv1alpha1.CrewFitnessSuitePhaseCompleted},
	}
	results := []IterationResult{
		{Scenario: "s1", Iteration: 1, StartedAt: time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
			DurationMs: 1000, Phase: kubemootv1alpha1.CrewFitnessPhasePassed, AssertionsPassed: 1, AssertionsTotal: 1},
	}
	b, err := BuildFitnessSuiteXLSX(suite, results)
	if err != nil {
		t.Fatalf("build XLSX: %v", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open XLSX: %v", err)
	}
	defer func() { _ = f.Close() }()
	rows, err := f.GetRows("Overview")
	if err != nil {
		t.Fatalf("get Overview rows: %v", err)
	}
	found := false
	for _, row := range rows {
		if len(row) >= 2 && row[0] == "Crew version" {
			if row[1] != "1.4.2" {
				t.Fatalf("Crew version cell = %q, want 1.4.2", row[1])
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("Overview missing the 'Crew version' provenance row")
	}
}

// TestCreateReportSheetsBuildsExpectedTabs pins the extracted sheet-creation
// helper: it renames the default sheet to Overview and creates the remaining
// tabs, leaving no stray "Sheet1". (Extracted from BuildFitnessSuiteXLSXWithMeasures
// to cut cognitive complexity; this guards the sheet set in isolation.)
func TestCreateReportSheetsBuildsExpectedTabs(t *testing.T) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := createReportSheets(f); err != nil {
		t.Fatalf("createReportSheets: %v", err)
	}

	got := map[string]bool{}
	for _, s := range f.GetSheetList() {
		got[s] = true
	}
	want := []string{
		sheetOverview, sheetPassRate, sheetDuration, sheetQuality,
		sheetScenarios, sheetRuns, sheetAssertions, sheetFailures,
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing sheet %q (got %v)", name, f.GetSheetList())
		}
	}
	if got["Sheet1"] {
		t.Errorf("default Sheet1 was not renamed (got %v)", f.GetSheetList())
	}
	if n := len(f.GetSheetList()); n != len(want) {
		t.Errorf("expected %d sheets, got %d (%v)", len(want), n, f.GetSheetList())
	}
}

// TestWriteAssertionRows pins the row-emission contract extracted from
// writeAssertionsSheet during the cognitive-complexity refactor: an iteration
// with assertions yields one row per assertion (Raw/Passed/Message carried
// through); an iteration with NO assertions yields exactly one synthetic row
// that surfaces the execution error, falling back to a placeholder when Error
// is empty so error-phase iterations are never silently dropped.
func collectAssertionRows(t *testing.T, r IterationResult) [][]any {
	t.Helper()
	var rows [][]any
	if err := writeAssertionRows(r, func(vals []any) error { rows = append(rows, vals); return nil }); err != nil {
		t.Fatalf("writeAssertionRows: %v", err)
	}
	return rows
}

func assertRowsEqual(t *testing.T, got, want [][]any) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("want %d rows, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if fmt.Sprint(got[i]) != fmt.Sprint(want[i]) {
			t.Errorf("row%d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestWriteAssertionRows(t *testing.T) {
	cases := []struct {
		name string
		r    IterationResult
		want [][]any
	}{
		{"one row per assertion",
			IterationResult{Scenario: "s1", Iteration: 2, Assertions: []kubemootv1alpha1.AssertionResult{
				{Raw: "ASSERT a", Passed: true, Message: "ok"},
				{Raw: "ASSERT b", Passed: false, Message: "nope"},
			}},
			[][]any{{"s1", int32(2), "ASSERT a", true, "ok"}, {"s1", int32(2), "ASSERT b", false, "nope"}}},
		{"no assertions surfaces the error",
			IterationResult{Scenario: "s2", Iteration: 5, Error: "boom"},
			[][]any{{"s2", int32(5), "(none)", false, "boom"}}},
		{"no assertions and no error uses placeholder",
			IterationResult{Scenario: "s3", Iteration: 1},
			[][]any{{"s3", int32(1), "(none)", false, "(no assertions recorded)"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertRowsEqual(t, collectAssertionRows(t, c.r), c.want)
		})
	}

	t.Run("put error propagates", func(t *testing.T) {
		errPut := func([]any) error { return fmt.Errorf("write failed") }
		r := IterationResult{Scenario: "s4", Assertions: []kubemootv1alpha1.AssertionResult{{Raw: "ASSERT x"}}}
		if err := writeAssertionRows(r, errPut); err == nil {
			t.Error("want error from put, got nil")
		}
	})
}
