/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package controller — fitness_artifact.go writes the XLSX deliverable
// for a completed CrewFitnessSuite. Pure function: given the suite + the
// per-iteration results harvested from the owned CrewFitness CRs, returns
// the bytes of an XLSX with two sheets ("Overview" and "Runs"). The
// caller decides where to persist (NATS Object Store in production; a
// temp file or memory buffer in tests).
//
// Schema (per the [[Kubemoot Fitness Suite Runner with NATS Object Store
// Artifacts]] card):
//
//	Runs sheet — one row per iteration:
//	  crew, scenario, run_id, iteration, started_at, duration_ms,
//	  phase, assertions_passed, assertions_total,
//	  correctness, adherence, efficiency, run_grade
//
//	Scenarios sheet — one row per distinct scenario:
//	  scenario, sample_count, passed, failed, errored,
//	  duration_mean_ms, duration_p50_ms, duration_p90_ms, pass_rate,
//	  mean_correctness, mean_adherence, mean_efficiency,
//	  self_consistency, judge_quality, scenario_grade
//
//	Assertions sheet — one row per (iteration × assertion):
//	  scenario, iteration, assertion, passed, message
//
//	Failures sheet — one row per (scenario × failing assertion), aggregated:
//	  scenario, assertion, fail_count, failed_iterations, sample_message
//
//	Overview sheet — suite metadata + roll-up totals, led by the crew grade.
//	Rubric sheet — editable grade weights + efficiency budget the grade
//	  formulas reference (see fitness_scoring.go for the measure definitions).
package controller

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Sheet names, hoisted so the repeated references stay in sync (a typo in one
// of the dozens of call sites would silently target a non-existent sheet).
const (
	sheetOverview   = "Overview"
	sheetScenarios  = "Scenarios"
	sheetRuns       = "Runs"
	sheetAssertions = "Assertions"
	sheetFailures   = "Failures"
	sheetPassRate   = "Pass Rate"
	sheetDuration   = "Duration"
	sheetQuality    = "Quality"
)

// Repeated rollup labels and the live-formula fragment shared across the stats blocks.
const (
	labelPassRate         = "Pass rate"
	labelFitnessTests     = "Fitness tests"
	formulaCountScenarios = "=COUNTA(Scenarios!$A$2:$A$%d)"
)

// IterationResult is one harvested per-iteration outcome. Decoupled from
// CrewFitness so tests can construct results directly without spinning up
// the operator.
type IterationResult struct {
	Scenario         string
	Iteration        int32
	StartedAt        time.Time
	DurationMs       int64
	Phase            kubemootv1alpha1.CrewFitnessPhase
	AssertionsPassed int
	AssertionsTotal  int
	// Assertions is the per-assertion detail (raw text, pass/fail, message)
	// — carried through so the XLSX Assertions tab can show WHICH assertion
	// failed with WHAT message on each iteration, not just a pass count.
	Assertions []kubemootv1alpha1.AssertionResult
	// Error is the CrewFitness status error (set when Phase==Error: the
	// iteration couldn't execute, distinct from a failed assertion).
	Error string

	// --- objective measures (populated from the transcript events; zero on the
	// live-CR harvest path, which has no events) ---
	// Synthesis is the crew's answer text — for scenario self-consistency and the
	// LLM judge. Question is the user query (same per scenario) the judge scores
	// the synthesis against.
	Synthesis string
	Question  string
	// Correctness / Adherence / Efficiency are the per-run sub-scores (0-100).
	Correctness float64
	Adherence   float64
	Efficiency  float64
	// ConsensusOK is true when the run's "≥N toolers agree" gate passed (or the
	// scenario has no such gate). A run where it is FALSE earns 0 quality — good
	// solo-coordinator text cannot mask the crew failing to deliberate.
	ConsensusOK bool
	// Participation (0-100) is how well toolers engaged vs the scenario's
	// expectation (agree-depth). -1 means "unknown" (no event stream, e.g. the
	// live-CR harvest path) and is skipped in the scenario mean.
	Participation float64
	// Selectivity (0-100) is how precisely the coordinator woke its subcommittee
	// (contributing agents / responding agents). -1 means "unknown" (no event
	// stream) and is skipped in the scenario mean.
	Selectivity float64
}

// HarvestIterationResults extracts an IterationResult per child CrewFitness
// CR, decoded from the labels the suite reconciler stamped and the status
// the CrewFitness reconciler populated. Returns the list sorted by
// (scenario, iteration) so the Runs sheet reads top-to-bottom in the
// natural order.
func HarvestIterationResults(children []kubemootv1alpha1.CrewFitness) []IterationResult {
	out := make([]IterationResult, 0, len(children))
	for i := range children {
		c := &children[i]
		iter := parseIterationLabel(c.Labels[suiteIterationIndexLabel])
		passed, total := countAssertions(c.Status.Assertions)
		started := time.Time{}
		if c.Status.StartedAt != nil {
			started = c.Status.StartedAt.Time
		}
		out = append(out, IterationResult{
			Scenario:         c.Spec.TestRef,
			Iteration:        iter,
			StartedAt:        started,
			DurationMs:       c.Status.DurationMs,
			Phase:            c.Status.Phase,
			AssertionsPassed: passed,
			AssertionsTotal:  total,
			Assertions:       c.Status.Assertions,
			Error:            c.Status.Error,
			// No event stream on the harvest path: don't gate quality, and mark
			// participation + selectivity unknown (-1) so they're skipped in the mean.
			ConsensusOK:   true,
			Participation: -1,
			Selectivity:   -1,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Scenario != out[j].Scenario {
			return out[i].Scenario < out[j].Scenario
		}
		return out[i].Iteration < out[j].Iteration
	})
	return out
}

func parseIterationLabel(s string) int32 {
	if s == "" {
		return 0
	}
	var v int32
	if _, err := fmt.Sscanf(s, "%d", &v); err != nil {
		return 0
	}
	return v
}

func countAssertions(assertions []kubemootv1alpha1.AssertionResult) (passed, total int) {
	total = len(assertions)
	for i := range assertions {
		if assertions[i].Passed {
			passed++
		}
	}
	return
}

// BuildFitnessSuiteXLSX produces the XLSX bytes for the suite + its
// per-iteration results. Both sheets always exist (even if results is
// empty, callers get a valid XLSX with headers — useful for debugging
// "the operator ran the suite but no results came back" cases).
func BuildFitnessSuiteXLSX(suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult) ([]byte, error) {
	return BuildFitnessSuiteXLSXWithMeasures(suite, results, nil, nil)
}

// BuildFitnessSuiteXLSXWithMeasures is BuildFitnessSuiteXLSX with optional
// per-scenario overrides from the report path: semantic self-consistency and
// LLM-judge quality. A scenario absent from consistency falls back to the
// lexical score; absent from judgeQuality → 0 (judge's default weight is 0, so
// it never changes the grade). nil maps → lexical consistency, no judge.
func BuildFitnessSuiteXLSXWithMeasures(suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult, consistency, judgeQuality map[string]float64) ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	if err := createReportSheets(f); err != nil {
		return nil, err
	}

	weights := defaultRubricWeights()
	aggs := aggregateScenarios(results, weights, consistency, judgeQuality)
	n := len(aggs)

	if err := writeReportSheets(f, suite, results, aggs, weights, n); err != nil {
		return nil, err
	}

	setReportColumnWidths(f)

	if idx, err := f.GetSheetIndex(sheetOverview); err == nil {
		f.SetActiveSheet(idx)
	}

	// excelize writes formulas WITHOUT cached results, so Excel would show the
	// formula cells (counts, mean, pass_rate, totals, stats) blank until a manual
	// recalc. fullCalcOnLoad makes Excel recompute everything on open.
	fullCalc := true
	if err := f.SetCalcProps(&excelize.CalcPropsOptions{FullCalcOnLoad: &fullCalc}); err != nil {
		return nil, fmt.Errorf("set calc props: %w", err)
	}

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, fmt.Errorf("serializing XLSX: %w", err)
	}
	return buf.Bytes(), nil
}

// createReportSheets renames the default sheet to Overview and creates the
// remaining tabs in display order (active landing, the chart tabs, then data
// tabs).
func createReportSheets(f *excelize.File) error {
	if err := f.SetSheetName("Sheet1", sheetOverview); err != nil {
		return fmt.Errorf("rename default sheet: %w", err)
	}
	for _, name := range []string{sheetPassRate, sheetDuration, sheetQuality, sheetScenarios, sheetRuns, sheetAssertions, sheetFailures} {
		if _, err := f.NewSheet(name); err != nil {
			return fmt.Errorf("creating %s sheet: %w", name, err)
		}
	}
	return nil
}

// writeReportSheets populates every sheet in order. Raw sheets (Runs,
// Assertions) carry data; the derived sheets (Scenarios, Failures, Overview,
// and the chart tabs) compute everything with live formulas + charts over the
// raw data. The steps run in a fixed sequence so the first failure short-circuits
// with its wrapped error.
func writeReportSheets(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult, aggs []scenarioStat, weights rubricWeights, n int) error {
	steps := []struct {
		label string
		write func() error
	}{
		{"writing Runs sheet", func() error { return writeRunsSheet(f, suite, results) }},
		{"writing Assertions sheet", func() error { return writeAssertionsSheet(f, suite, results) }},
		{"writing Scenarios sheet", func() error { return writeScenariosSheet(f, aggs) }},
		{"writing Failures sheet", func() error { return writeFailureSummarySheet(f, results) }},
		{"writing Overview sheet", func() error { return writeOverviewSheet(f, suite, n, weights, crewMeans(aggs)) }},
		{"writing Pass Rate sheet", func() error { return writePassRateChartSheet(f, n) }},
		{"writing Duration sheet", func() error { return writeDurationChartSheet(f, n) }},
		{"writing Quality sheet", func() error { return writeQualityChartSheet(f, n) }},
	}
	for _, s := range steps {
		if err := s.write(); err != nil {
			return fmt.Errorf("%s: %w", s.label, err)
		}
	}
	return nil
}

// factualityCell renders a factuality score for the sheet: -1 ("no facts
// authored") becomes a blank cell rather than a misleading -1, so a reader sees
// "graded" vs "not graded" at a glance. Any real 0-100 score passes through.
func factualityCell(v float64) any {
	if v < 0 {
		return ""
	}
	return v
}

func writeRunsSheet(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult) error {
	headers := []string{
		"crew", "scenario", "run_id", "iteration",
		"started_at", "duration_ms", "phase",
		"assertions_passed", "assertions_total",
		"correctness", "adherence", "efficiency", "selectivity", "factuality", "fabricated",
	}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheetRuns, cell, h); err != nil {
			return err
		}
	}
	for row, r := range results {
		if err := writeRunRow(f, suite, r, row+2); err != nil {
			return err
		}
	}
	_ = f.SetColStyle(sheetRuns, "J:N", scoreStyle(f))
	return nil
}

// writeRunRow writes one Runs row: the raw values (A-I) plus the per-run data
// sub-scores (J-L). correctness and efficiency are LIVE formulas (so editing the
// data recomputes them) carrying a Go-computed cached value so they also show in
// Excel's Protected View; adherence is a value (it needs the event stream, not
// derivable in-sheet). These are informational — the graded measures are
// scenario-level (Scenarios sheet) and roll up to the Overview scorecard.
func writeRunRow(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite, r IterationResult, n int) error {
	started := ""
	if !r.StartedAt.IsZero() {
		started = r.StartedAt.UTC().Format(time.RFC3339)
	}
	values := []any{
		suite.Spec.CrewRef, r.Scenario, suite.Status.RunID, r.Iteration,
		started, r.DurationMs, string(r.Phase), r.AssertionsPassed, r.AssertionsTotal,
	}
	for col, v := range values {
		cell, _ := excelize.CoordinatesToCellName(col+1, n)
		if err := f.SetCellValue(sheetRuns, cell, v); err != nil {
			return err
		}
	}
	if err := setFormulaCached(f, sheetRuns, cellAt(10, n), r.Correctness,
		fmt.Sprintf("=IFERROR(H%d/I%d*100,0)", n, n)); err != nil {
		return err
	}
	if err := f.SetCellValue(sheetRuns, cellAt(11, n), r.Adherence); err != nil {
		return err
	}
	budget := defaultEfficiencyBudgetMs
	if err := setFormulaCached(f, sheetRuns, cellAt(12, n), r.Efficiency,
		fmt.Sprintf("=IF(F%d<=0,0,IF(F%d<=%d,100,%d/F%d*100))", n, n, budget, budget, n)); err != nil {
		return err
	}
	// selectivity is an event-stream value (not in-sheet derivable), like adherence.
	if err := f.SetCellValue(sheetRuns, cellAt(13, n), r.Selectivity); err != nil {
		return err
	}
	// factuality (per-run fact-assertion pass-rate; blank when the scenario authors
	// no facts) and fabricated (a forbidden/stale claim tripped this run) are the
	// integrity signals, surfaced per-iteration to match the Scenarios roll-up.
	if err := f.SetCellValue(sheetRuns, cellAt(14, n), factualityCell(factualityScore(r.Assertions))); err != nil {
		return err
	}
	return f.SetCellValue(sheetRuns, cellAt(15, n), fabricationTripped(r.Assertions))
}

// writeAssertionsSheet emits one row per (iteration × assertion) so a reader
// can see WHICH assertion failed with WHAT message on each iteration — turning
// "scenario X = 60%" into "fails assertion A with message M on iterations 3,7,9".
// Error-phase iterations (no assertions recorded) get one row carrying the
// execution error so they aren't silently absent.
func writeAssertionsSheet(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite, results []IterationResult) error {
	headers := []string{"scenario", "iteration", "assertion", "passed", "message"}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheetAssertions, cell, h); err != nil {
			return err
		}
	}
	row := 2
	put := func(vals []any) error {
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			if err := f.SetCellValue(sheetAssertions, cell, v); err != nil {
				return err
			}
		}
		row++
		return nil
	}
	for _, r := range results {
		if err := writeAssertionRows(r, put); err != nil {
			return err
		}
	}
	return nil
}

// writeAssertionRows emits the assertion row(s) for a single iteration via put.
// Iterations with no assertions get one synthetic row carrying the execution
// error (or a placeholder) so error-phase iterations are never silently absent.
func writeAssertionRows(r IterationResult, put func([]any) error) error {
	if len(r.Assertions) == 0 {
		msg := r.Error
		if msg == "" {
			msg = "(no assertions recorded)"
		}
		return put([]any{r.Scenario, r.Iteration, "(none)", false, msg})
	}
	for _, a := range r.Assertions {
		if err := put([]any{r.Scenario, r.Iteration, a.Raw, a.Passed, a.Message}); err != nil {
			return err
		}
	}
	return nil
}

// writeFailureSummarySheet aggregates failures per (scenario, assertion): how
// many iterations failed it, which iterations, and a sample message. Plus a row
// per scenario for execution errors (Phase==Error). First-seen order preserved
// so the sheet is stable across runs.
func writeFailureSummarySheet(f *excelize.File, results []IterationResult) error {
	headers := []string{"scenario", "assertion", "fail_count", "failed_iterations", "sample_message"}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheetFailures, cell, h); err != nil {
			return err
		}
	}
	byKey, order := aggregateFailures(results)
	row := 2
	for _, key := range order {
		parts := strings.SplitN(key, failureKeySep, 2)
		e := byKey[key]
		iterStrs := make([]string, len(e.iters))
		for i, it := range e.iters {
			iterStrs[i] = fmt.Sprintf("%d", it)
		}
		vals := []any{parts[0], parts[1], e.count, strings.Join(iterStrs, ", "), e.sample}
		for col, v := range vals {
			cell, _ := excelize.CoordinatesToCellName(col+1, row)
			if err := f.SetCellValue(sheetFailures, cell, v); err != nil {
				return err
			}
		}
		row++
	}
	return nil
}

// failureKeySep joins (scenario, assertion) into a map key; NUL never appears in
// scenario names or assertion text, so the split back is unambiguous.
const failureKeySep = "\x00"

// failureAgg accumulates one (scenario, assertion) failure group: how many
// iterations failed it, which iterations, and a sample message.
type failureAgg struct {
	count  int
	iters  []int32
	sample string
}

// aggregateFailures groups failing assertions (and execution errors) by
// (scenario, assertion), preserving first-seen order so the sheet is stable.
func aggregateFailures(results []IterationResult) (map[string]*failureAgg, []string) {
	byKey := map[string]*failureAgg{}
	order := []string{}
	bump := func(scenario, assertion, message string, iter int32) {
		key := scenario + failureKeySep + assertion
		e := byKey[key]
		if e == nil {
			e = &failureAgg{sample: message}
			byKey[key] = e
			order = append(order, key)
		}
		e.count++
		e.iters = append(e.iters, iter)
		if e.sample == "" {
			e.sample = message
		}
	}
	for _, r := range results {
		for _, a := range r.Assertions {
			if !a.Passed {
				bump(r.Scenario, a.Raw, a.Message, r.Iteration)
			}
		}
		if r.Phase == kubemootv1alpha1.CrewFitnessPhaseError && r.Error != "" {
			bump(r.Scenario, "(execution error)", r.Error, r.Iteration)
		}
	}
	return byKey, order
}

// scenarioStat carries per-scenario percentile values that aren't expressible
// as simple spreadsheet formulas (median / p90 of a filtered set need array
// formulas). Counts, mean, and pass_rate are written as LIVE formulas on the
// Scenarios sheet so the workbook recalculates if a reader edits the data.
type scenarioStat struct {
	scenario        string
	p50, p90        int64
	meanCorrectness float64
	meanAdherence   float64
	meanEfficiency  float64
	reliability     float64 // binary pass-rate × 100: fully-passing runs / total
	participation   float64 // toolers-engaged vs expected (agree-depth), 0-100
	selectivity     float64 // coordinator wake precision (contributing/responding), 0-100
	consistency     float64 // self-consistency across the scenario's runs
	quality         float64 // reference-grounded REFLECTS judge mean (0 until judged)
	factuality      float64 // deterministic fact-assertion pass-rate mean (-1 = none authored)
	fabFraction     float64 // fraction of runs that asserted a forbidden/stale fact
	grade           float64 // composite scenario grade (cached for the formula)
}

// scenarioAcc accumulates one scenario's per-run measures before reduction.
type scenarioAcc struct {
	durs                      []int64
	corr, adh, eff, part, sel []float64
	fact                      []float64 // per-run factuality, known runs only (-1 excluded)
	synth                     []string
	passed, total             int // for reliability (binary pass-rate)
	fabricating               int // runs that asserted a forbidden/stale fact
}

// aggregateScenarios returns distinct scenarios (sorted) with p50/p90 durations,
// mean sub-scores, self-consistency across the scenario's synthesis texts, and
// the composite scenario grade (cached value for the in-sheet formula).
// consistency, when non-nil and containing a scenario, supplies that scenario's
// semantic self-consistency; otherwise the lexical fallback is computed.
func aggregateScenarios(results []IterationResult, w rubricWeights, consistency, judgeQuality map[string]float64) []scenarioStat {
	accs := map[string]*scenarioAcc{}
	order := []string{}
	for _, r := range results {
		a := accs[r.Scenario]
		if a == nil {
			a = &scenarioAcc{}
			accs[r.Scenario] = a
			order = append(order, r.Scenario)
		}
		a.add(r)
	}
	sort.Strings(order)
	out := make([]scenarioStat, 0, len(order))
	for _, s := range order {
		out = append(out, accs[s].toStat(s, consistency, judgeQuality, w))
	}
	return out
}

// add folds one iteration's measures into the per-scenario accumulator.
func (a *scenarioAcc) add(r IterationResult) {
	a.durs = append(a.durs, r.DurationMs)
	a.corr = append(a.corr, r.Correctness)
	a.adh = append(a.adh, r.Adherence)
	a.eff = append(a.eff, r.Efficiency)
	if r.Participation >= 0 { // -1 = unknown (no events); skip in the mean
		a.part = append(a.part, r.Participation)
	}
	if r.Selectivity >= 0 { // -1 = unknown (no events); skip in the mean
		a.sel = append(a.sel, r.Selectivity)
	}
	// Deterministic ground-truth factuality, computed here from the run's own
	// assertions + synthesis so no per-run sentinel field can be forgotten by a
	// constructor. -1 (no fact assertions authored) is excluded from the mean.
	fact := factualityScore(r.Assertions)
	fabricated := fabricationTripped(r.Assertions)
	if fact >= 0 && fact < honestFactualityFloor && !fabricated && declaresHonestFailure(r.Synthesis) {
		fact = honestFactualityFloor // an honest "could not retrieve" outranks a fabrication
	}
	if fact >= 0 {
		a.fact = append(a.fact, fact)
	}
	if fabricated {
		a.fabricating++
	}
	a.total++
	if r.Phase == kubemootv1alpha1.CrewFitnessPhasePassed {
		a.passed++
	}
	if strings.TrimSpace(r.Synthesis) != "" {
		a.synth = append(a.synth, r.Synthesis)
	}
}

// toStat reduces the accumulator to the per-scenario summary row. part/sel default
// to 100 (not penalized) when no event data was captured; consistency falls back to
// a lexical estimate when the judge did not supply one.
func (a *scenarioAcc) toStat(s string, consistency, judgeQuality map[string]float64, w rubricWeights) scenarioStat {
	_, p50, p90 := durationStats(a.durs)
	mc, ma, me := meanFloat(a.corr), meanFloat(a.adh), meanFloat(a.eff)
	rel := pct(a.passed, a.total) // binary pass-rate as 0-100
	part := 100.0
	if len(a.part) > 0 {
		part = meanFloat(a.part)
	}
	sel := 100.0
	if len(a.sel) > 0 {
		sel = meanFloat(a.sel)
	}
	cons, ok := consistency[s]
	if !ok {
		cons = lexicalSelfConsistency(a.synth)
	}
	q := judgeQuality[s] // reference-grounded REFLECTS score; 0 until judged
	fact := -1.0         // unknown until a run authored fact assertions
	if len(a.fact) > 0 {
		fact = meanFloat(a.fact)
	}
	fabFrac := 0.0
	if a.total > 0 {
		fabFrac = float64(a.fabricating) / float64(a.total)
	}
	return scenarioStat{
		scenario: s, p50: p50, p90: p90,
		meanCorrectness: mc, meanAdherence: ma, meanEfficiency: me,
		reliability: rel, participation: part, selectivity: sel, consistency: cons, quality: q,
		factuality: fact, fabFraction: fabFrac,
		grade: scenarioGrade(gradeMeasures{
			quality: q, reliability: rel, factuality: fact, fabFraction: fabFrac,
			participation: part, consistency: cons, efficiency: me,
		}, w),
	}
}

// meanFloat is the arithmetic mean (0 for empty input).
func meanFloat(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// gradeMeasures are the graded rubric measures scenarioGrade composes, for one
// scenario or as crew-level means.
type gradeMeasures struct {
	quality       float64
	reliability   float64
	factuality    float64 // -1 when no fact assertions were authored (excluded from the grade)
	fabFraction   float64 // fraction of runs that asserted a forbidden/stale fact
	participation float64
	consistency   float64
	efficiency    float64
}

// crewScore holds the crew-level mean of each sub-score (each scenario weighted
// equally, so a flaky niche scenario doesn't drown in a high-volume one).
// Crew factuality is the mean over scenarios that authored facts, -1 if none did.
type crewScore gradeMeasures

// crewMeans averages each graded measure across scenarios. Factuality averages
// only over scenarios that authored fact assertions (the rest carry -1); if none
// did, crew factuality stays -1 and is excluded from the grade.
func crewMeans(aggs []scenarioStat) crewScore {
	if len(aggs) == 0 {
		return crewScore{factuality: -1}
	}
	var q, r, p, k, e, fab, fSum float64
	var fN int
	for _, s := range aggs {
		q += s.quality
		r += s.reliability
		p += s.participation
		k += s.consistency
		e += s.meanEfficiency
		fab += s.fabFraction
		if s.factuality >= 0 {
			fSum += s.factuality
			fN++
		}
	}
	n := float64(len(aggs))
	fact := -1.0
	if fN > 0 {
		fact = fSum / float64(fN)
	}
	return crewScore{
		quality: q / n, reliability: r / n, factuality: fact, fabFraction: fab / n,
		participation: p / n, consistency: k / n, efficiency: e / n,
	}
}

// cellAt is CoordinatesToCellName without the discarded error (col/row are
// always valid here).
func cellAt(col, row int) string {
	c, _ := excelize.CoordinatesToCellName(col, row)
	return c
}

// setFormulaCached writes a formula AND a Go-computed cached result. Excel won't
// recalculate in Protected View, so the cached value is what a freshly-downloaded
// file shows; the formula still recomputes live once editing is enabled or an
// input (data / rubric weight) changes. excelize keeps both <v> and <f> when
// SetCellValue precedes SetCellFormula.
func setFormulaCached(f *excelize.File, sheet, cell string, cached float64, formula string) error {
	if err := f.SetCellValue(sheet, cell, cached); err != nil {
		return err
	}
	return f.SetCellFormula(sheet, cell, strings.TrimPrefix(formula, "="))
}

// scoreStyle is the one-decimal number format for 0-100 sub-score / grade columns.
func scoreStyle(f *excelize.File) int {
	fmtStr := "0.0"
	id, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &fmtStr})
	return id
}

// writeOverviewScorecard appends the crew scorecard to the Overview sheet,
// starting at startRow, and returns the next free row. ONE table — Measure, Crew
// score, Weight, Contribution, Definition (inline, so a reader never hunts a
// separate legend) — over the four graded measures, with the contributions
// summing to the crew grade. Values are Go-computed (not cross-sheet formulas):
// the weights are code-driven (defaultRubricWeights), so the report states how
// the grade was reached rather than acting as a live re-weighting calculator.
func writeOverviewScorecard(f *excelize.File, w rubricWeights, means crewScore, startRow int) (int, error) {
	scoreFmt := scoreStyle(f)
	row := startRow
	set := func(col int, v any) error { return f.SetCellValue(sheetOverview, cellAt(col, row), v) }

	if err := set(1, "Scorecard — how the crew grade is computed"); err != nil {
		return row, err
	}
	row++
	for c, h := range []string{"Measure", "Crew score (0-100)", "Weight", "Contribution", "Definition"} {
		if err := f.SetCellValue(sheetOverview, cellAt(c+1, row), h); err != nil {
			return row, err
		}
	}
	row++

	tw := w.Quality + w.Reliability + w.Participation + w.Consistency + w.Efficiency
	measures := scorecardMeasures(w, means)
	firstRow := row
	for _, m := range measures {
		contrib := 0.0
		if tw > 0 {
			contrib = m.score * m.weight / tw
		}
		cells := []any{m.label, m.score, m.weight, contrib, m.def}
		for c, v := range cells {
			if err := f.SetCellValue(sheetOverview, cellAt(c+1, row), v); err != nil {
				return row, err
			}
		}
		row++
	}
	if err := set(1, "Crew grade (0-100)"); err != nil {
		return row, err
	}
	if err := set(4, crewGradeFromMeans(means, w)); err != nil {
		return row, err
	}
	gradeRow := row
	row++

	_ = f.SetCellStyle(sheetOverview, cellAt(2, firstRow), cellAt(2, firstRow+len(measures)-1), scoreFmt)
	_ = f.SetCellStyle(sheetOverview, cellAt(4, firstRow), cellAt(4, gradeRow), scoreFmt)
	return row, nil
}

// scorecardMeasure is one row of the Overview scorecard: a graded measure, the
// crew's mean score for it, its rubric weight, and an inline definition.
type scorecardMeasure struct {
	label  string
	score  float64
	weight float64
	def    string
}

// scorecardMeasures builds the five graded-measure rows (with inline definitions)
// shown on the Overview scorecard, in grade-contribution order.
func scorecardMeasures(w rubricWeights, means crewScore) []scorecardMeasure {
	return []scorecardMeasure{
		{"Quality", means.quality, w.Quality,
			"Reference-grounded REFLECTS judge: how well the answer matches ground truth, fabrication-penalized. GATED ON CONSENSUS — a run that failed the ≥N-agree gate scores 0, so solo-coordinator text can't mask a non-deliberating crew."},
		{"Reliability", means.reliability, w.Reliability,
			"Binary pass-rate: fraction of runs passing every mechanical gate (POST 200, done, ≥N agrees); a failed or errored run scores 0."},
		{"Participation", means.participation, w.Participation,
			"Did toolers actually engage — agree-depth vs the scenario's expectation. The consensus system's core job, scored directly."},
		{"Consistency", means.consistency, w.Consistency,
			"Answer stability across a scenario's iterations (semantic cosine of synthesis embeddings; meaningful only when N>1)."},
		{"Efficiency", means.efficiency, w.Efficiency,
			fmt.Sprintf("Wall-clock vs a %d ms budget: at/under budget scores 100, slower decays as budget/duration.", defaultEfficiencyBudgetMs)},
	}
}

// crewGradeFromMeans applies the rubric weights to already-computed crew means.
func crewGradeFromMeans(m crewScore, w rubricWeights) float64 {
	return scenarioGrade(gradeMeasures(m), w)
}

// writeScenariosSheet writes the per-scenario aggregate table. sample_count,
// passed/failed/errored, mean, and pass_rate are live formulas over the Runs
// sheet; p50/p90 are values (percentile-of-a-filtered-set needs array formulas).
func writeScenariosSheet(f *excelize.File, aggs []scenarioStat) error {
	headers := []string{
		"scenario", "sample_count", "passed", "failed", "errored",
		"duration_mean_ms", "duration_p50_ms", "duration_p90_ms", "pass_rate",
		"mean_correctness", "mean_adherence", "mean_efficiency", "self_consistency", "participation", "quality", "scenario_grade", "mean_selectivity",
		"factuality", "fabrication_pct",
	}
	for col, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(col+1, 1)
		if err := f.SetCellValue(sheetScenarios, cell, h); err != nil {
			return err
		}
	}
	pctStyle, _ := f.NewStyle(&excelize.Style{NumFmt: 10}) // 0.00%
	for i, a := range aggs {
		r := i + 2
		if err := writeScenarioRow(f, a, r, pctStyle); err != nil {
			return err
		}
	}
	// factuality (R) is a 0-100 score like J:Q; fabrication_pct (S) is a fraction.
	_ = f.SetColStyle(sheetScenarios, "J:R", scoreStyle(f))
	_ = f.SetColStyle(sheetScenarios, "S:S", pctStyle)
	return nil
}

// writeScenarioRow writes one Scenarios row: the scenario name, the count /
// duration / pass-rate formulas (A-I), and then the objective sub-scores (J-Q via
// writeScenarioScores). pctStyle formats the pass_rate cell as a percentage.
func writeScenarioRow(f *excelize.File, a scenarioStat, r, pctStyle int) error {
	setVal := func(col int, v any) error {
		cell, _ := excelize.CoordinatesToCellName(col, r)
		return f.SetCellValue(sheetScenarios, cell, v)
	}
	setFml := func(col int, fml string) error {
		cell, _ := excelize.CoordinatesToCellName(col, r)
		// excelize stores the formula verbatim; the OOXML <f> element must
		// NOT carry a leading "=" or strict consumers (Google Sheets,
		// LibreOffice) fail to parse it. Strip it.
		return f.SetCellFormula(sheetScenarios, cell, strings.TrimPrefix(fml, "="))
	}
	if err := setVal(1, a.scenario); err != nil {
		return err
	}
	if err := setFml(2, fmt.Sprintf("=COUNTIF(Runs!$B:$B,$A%d)", r)); err != nil {
		return err
	}
	if err := setFml(3, fmt.Sprintf("=COUNTIFS(Runs!$B:$B,$A%d,Runs!$G:$G,\"Passed\")", r)); err != nil {
		return err
	}
	if err := setFml(4, fmt.Sprintf("=COUNTIFS(Runs!$B:$B,$A%d,Runs!$G:$G,\"Failed\")", r)); err != nil {
		return err
	}
	if err := setFml(5, fmt.Sprintf("=COUNTIFS(Runs!$B:$B,$A%d,Runs!$G:$G,\"Error\")", r)); err != nil {
		return err
	}
	if err := setFml(6, fmt.Sprintf("=IFERROR(AVERAGEIF(Runs!$B:$B,$A%d,Runs!$F:$F),0)", r)); err != nil {
		return err
	}
	if err := setVal(7, a.p50); err != nil {
		return err
	}
	if err := setVal(8, a.p90); err != nil {
		return err
	}
	if err := setFml(9, fmt.Sprintf("=IFERROR(C%d/B%d,0)", r, r)); err != nil {
		return err
	}
	prCell, _ := excelize.CoordinatesToCellName(9, r)
	_ = f.SetCellStyle(sheetScenarios, prCell, prCell, pctStyle)
	return writeScenarioScores(f, a, r)
}

// writeScenarioScores writes a scenario row's objective sub-scores and grade
// (columns J-O). The mean sub-scores are AVERAGEIF formulas over the Runs
// sub-score columns; self-consistency (M) and judge_quality (N) are values
// (computed across the scenario's iterations); scenario_grade (O) is the
// rubric-weighted formula incl. the judge term. All carry cached values so they
// show in Protected View.
func writeScenarioScores(f *excelize.File, a scenarioStat, r int) error {
	if err := setFormulaCached(f, sheetScenarios, cellAt(10, r), a.meanCorrectness,
		fmt.Sprintf("=IFERROR(AVERAGEIF(Runs!$B:$B,$A%d,Runs!$J:$J),0)", r)); err != nil {
		return err
	}
	if err := setFormulaCached(f, sheetScenarios, cellAt(11, r), a.meanAdherence,
		fmt.Sprintf("=IFERROR(AVERAGEIF(Runs!$B:$B,$A%d,Runs!$K:$K),0)", r)); err != nil {
		return err
	}
	if err := setFormulaCached(f, sheetScenarios, cellAt(12, r), a.meanEfficiency,
		fmt.Sprintf("=IFERROR(AVERAGEIF(Runs!$B:$B,$A%d,Runs!$L:$L),0)", r)); err != nil {
		return err
	}
	if err := f.SetCellValue(sheetScenarios, cellAt(13, r), a.consistency); err != nil {
		return err
	}
	if err := f.SetCellValue(sheetScenarios, cellAt(14, r), a.participation); err != nil {
		return err
	}
	if err := f.SetCellValue(sheetScenarios, cellAt(15, r), a.quality); err != nil {
		return err
	}
	// scenario_grade is the rubric-weighted composite (quality, reliability,
	// participation, consistency, efficiency), computed in Go — a value, not a
	// cross-sheet formula, since the weights are code-driven.
	if err := f.SetCellValue(sheetScenarios, cellAt(16, r), a.grade); err != nil {
		return err
	}
	// mean_selectivity is an objective DATA column (not a grade term): coordinator
	// wake precision averaged over the scenario's runs. Appended after the graded
	// columns the chart tabs reference so those column positions stay stable.
	if err := f.SetCellValue(sheetScenarios, cellAt(17, r), a.selectivity); err != nil {
		return err
	}
	// factuality (mean fact-assertion pass-rate; blank when no facts authored) and
	// fabrication_pct (fraction of runs that tripped a forbidden/stale claim) are the
	// integrity roll-ups this scenario contributes to the grade's Factuality term.
	if err := f.SetCellValue(sheetScenarios, cellAt(18, r), factualityCell(a.factuality)); err != nil {
		return err
	}
	return f.SetCellValue(sheetScenarios, cellAt(19, r), a.fabFraction)
}

// writeOverviewSheet writes the suite-level summary: human metadata plus roll-up
// totals expressed as LIVE formulas over the Scenarios / Runs sheets.
// overviewWriter threads the current row + date style through the Overview-sheet
// writes so each section is a small method instead of one CC-55 function.
type overviewWriter struct {
	f         *excelize.File
	row       int
	dateStyle int
}

func (o *overviewWriter) put(label string, value any) error {
	if err := o.f.SetCellValue(sheetOverview, cellAt(1, o.row), label); err != nil {
		return err
	}
	vc := cellAt(2, o.row)
	o.row++
	return o.f.SetCellValue(sheetOverview, vc, value)
}

func (o *overviewWriter) putFormula(label, fml string) error {
	if err := o.f.SetCellValue(sheetOverview, cellAt(1, o.row), label); err != nil {
		return err
	}
	if err := o.f.SetCellFormula(sheetOverview, cellAt(2, o.row), strings.TrimPrefix(fml, "=")); err != nil {
		return err
	}
	o.row++
	return nil
}

// putDate writes a real Excel datetime (so durations can be computed by formula)
// and returns the row it landed on.
func (o *overviewWriter) putDate(label string, t time.Time) (int, error) {
	if err := o.f.SetCellValue(sheetOverview, cellAt(1, o.row), label); err != nil {
		return 0, err
	}
	vc := cellAt(2, o.row)
	if err := o.f.SetCellValue(sheetOverview, vc, t.UTC()); err != nil {
		return 0, err
	}
	_ = o.f.SetCellStyle(sheetOverview, vc, vc, o.dateStyle)
	r := o.row
	o.row++
	return r, nil
}

// headline writes the crew's single report-card grade (rubric-weighted).
func (o *overviewWriter) headline(w rubricWeights, means crewScore) error {
	if err := o.f.SetCellValue(sheetOverview, cellAt(1, o.row), "Crew grade (0-100)"); err != nil {
		return err
	}
	// The scorecard table below traces how each measure contributes to this.
	if err := o.f.SetCellValue(sheetOverview, cellAt(2, o.row), crewGradeFromMeans(means, w)); err != nil {
		return err
	}
	_ = o.f.SetCellStyle(sheetOverview, cellAt(2, o.row), cellAt(2, o.row), scoreStyle(o.f))
	o.row++
	return nil
}

// metadata writes the human-readable run labels (description, suite identity,
// crew chart version for provenance).
func (o *overviewWriter) metadata(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	if suite.Spec.Description != "" {
		if err := o.put("Description", suite.Spec.Description); err != nil {
			return err
		}
	}
	for _, kv := range [][2]string{
		{"Suite", suite.Name},
		{"Namespace", suite.Namespace},
		{"Crew", suite.Spec.CrewRef},
		{"Run ID", suite.Status.RunID},
		{"Status", string(suite.Status.Phase)},
	} {
		if err := o.put(kv[0], kv[1]); err != nil {
			return err
		}
	}
	// Crew chart version, resolved from the Crew CR's kubemoot.ai/crew-version label.
	// Omitted for hand-applied crews. Lets a downloaded report attribute a run to a
	// specific crew version - essential for the ADL-vs-prose measurement.
	if cv := suite.Labels[crewVersionLabel]; cv != "" {
		if err := o.put("Crew version", cv); err != nil {
			return err
		}
	}
	return nil
}

// timing writes started/completed timestamps and the wall-clock duration formula.
func (o *overviewWriter) timing(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	startedRow, completedRow := 0, 0
	if suite.Status.StartedAt != nil {
		r, err := o.putDate("Started", suite.Status.StartedAt.Time)
		if err != nil {
			return err
		}
		startedRow = r
	}
	if suite.Status.CompletedAt != nil {
		r, err := o.putDate("Completed", suite.Status.CompletedAt.Time)
		if err != nil {
			return err
		}
		completedRow = r
	}
	if startedRow == 0 || completedRow == 0 {
		return nil
	}
	// Total wall-clock time as a formula: completed − started, [h]:mm:ss elapsed.
	if err := o.putFormula("Total wall-clock time", fmt.Sprintf("=B%d-B%d", completedRow, startedRow)); err != nil {
		return err
	}
	durFmt := "[h]:mm:ss"
	if durStyle, e := o.f.NewStyle(&excelize.Style{CustomNumFmt: &durFmt}); e == nil {
		_ = o.f.SetCellStyle(sheetOverview, cellAt(2, o.row-1), cellAt(2, o.row-1), durStyle)
	}
	return nil
}

// config writes iterations/concurrency/timeout settings.
func (o *overviewWriter) config(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	// Iterations per scenario derived from the data (max iteration in Runs).
	if err := o.putFormula("Iterations per scenario", "=MAX(Runs!$D:$D)"); err != nil {
		return err
	}
	concurrency := suite.Spec.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if err := o.put("Concurrency", concurrency); err != nil {
		return err
	}
	if suite.Spec.PerIterationTimeout != nil {
		if err := o.put("Per-iteration timeout", suite.Spec.PerIterationTimeout.Duration.String()); err != nil {
			return err
		}
	}
	if suite.Spec.ArtifactRetention != nil {
		if err := o.put("Artifact retention", suite.Spec.ArtifactRetention.Duration.String()); err != nil {
			return err
		}
	}
	return nil
}

// rollups writes the totals as live formulas over the Scenarios/Runs sheets.
func (o *overviewWriter) rollups(last int) error {
	for _, lf := range [][2]string{
		{"Total runs", fmt.Sprintf("=SUM(Scenarios!$B$2:$B$%d)", last)},
		{"Passed", fmt.Sprintf("=SUM(Scenarios!$C$2:$C$%d)", last)},
		{"Failed", fmt.Sprintf("=SUM(Scenarios!$D$2:$D$%d)", last)},
		{"Errored", fmt.Sprintf("=SUM(Scenarios!$E$2:$E$%d)", last)},
		{labelPassRate, fmt.Sprintf("=IFERROR(SUM(Scenarios!$C$2:$C$%d)/SUM(Scenarios!$B$2:$B$%d),0)", last, last)},
	} {
		if err := o.putFormula(lf[0], lf[1]); err != nil {
			return err
		}
		if lf[0] == labelPassRate {
			if pctStyle, e := o.f.NewStyle(&excelize.Style{NumFmt: 10}); e == nil {
				_ = o.f.SetCellStyle(sheetOverview, cellAt(2, o.row-1), cellAt(2, o.row-1), pctStyle)
			}
		}
	}
	return o.putFormula("Total inference time (ms)", "=SUM(Runs!$F:$F)")
}

func writeOverviewSheet(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite, numScenarios int, w rubricWeights, means crewScore) error {
	last := numScenarios + 1 // Scenarios data rows are 2..last
	dateFmt := "yyyy-mm-dd hh:mm:ss"
	dateStyle, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &dateFmt})
	o := &overviewWriter{f: f, row: 1, dateStyle: dateStyle}

	if numScenarios > 0 {
		if err := o.headline(w, means); err != nil {
			return err
		}
	}
	if err := o.metadata(suite); err != nil {
		return err
	}
	if err := o.timing(suite); err != nil {
		return err
	}
	if err := o.config(suite); err != nil {
		return err
	}
	if err := o.rollups(last); err != nil {
		return err
	}
	// --- Scorecard (Rubric folded in): how the crew grade is computed ---
	if numScenarios > 0 {
		if _, err := writeOverviewScorecard(f, w, means, o.row+1); err != nil {
			return err
		}
	}

	// Readable column widths: labels in A, values/timestamps in B, definitions in E.
	_ = f.SetColWidth(sheetOverview, "A", "A", 24)
	_ = f.SetColWidth(sheetOverview, "B", "B", 42)
	_ = f.SetColWidth(sheetOverview, "E", "E", 80)
	return nil
}

// chartXAxis is the shared X-axis: just the "Fitness Tests" title (no per-test
// tick labels — the scenario names live on the Scenarios sheet).
func chartXAxis() excelize.ChartAxis {
	return excelize.ChartAxis{
		Title: excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Fitness Tests"}}},
		Font:  excelize.Font{Color: "000000"}, // black tick labels (default is light grey)
	}
}

// writePassRateChartSheet puts the pass-rate column chart on its own sheet, with
// average + count stats (live formulas) below it.
func writePassRateChartSheet(f *excelize.File, numScenarios int) error {
	const sheet = sheetPassRate
	last := numScenarios + 1
	if numScenarios > 0 {
		zero, one := 0.0, 1.0
		if err := f.AddChart(sheet, "A1", &excelize.Chart{
			Type: excelize.Col,
			Series: []excelize.ChartSeries{{
				Name:   "Scenarios!$I$1",
				Values: fmt.Sprintf("Scenarios!$I$2:$I$%d", last),
			}},
			Title:     excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Pass rate by fitness test"}}},
			XAxis:     chartXAxis(),
			YAxis:     excelize.ChartAxis{Title: excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: labelPassRate}}}, Minimum: &zero, Maximum: &one, Font: excelize.Font{Color: "000000"}},
			Legend:    excelize.ChartLegend{Position: "none"},
			Dimension: excelize.ChartDimension{Width: 1000, Height: 400},
		}); err != nil {
			return fmt.Errorf("pass-rate chart: %w", err)
		}
	}
	return writeStatsBlock(f, sheet, 23, [][2]string{
		{"Average pass rate", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$I$2:$I$%d),0)", last)},
		{labelFitnessTests, fmt.Sprintf(formulaCountScenarios, last)},
		{"Total passed", fmt.Sprintf("=SUM(Scenarios!$C$2:$C$%d)", last)},
		{"Total failed", fmt.Sprintf("=SUM(Scenarios!$D$2:$D$%d)", last)},
		{"Total errored", fmt.Sprintf("=SUM(Scenarios!$E$2:$E$%d)", last)},
	})
}

// writeDurationChartSheet puts the mean/p50/p90 duration chart on its own sheet,
// with average + count stats (live formulas) below it.
func writeDurationChartSheet(f *excelize.File, numScenarios int) error {
	const sheet = sheetDuration
	last := numScenarios + 1
	if numScenarios > 0 {
		if err := f.AddChart(sheet, "A1", &excelize.Chart{
			Type: excelize.Col,
			Series: []excelize.ChartSeries{
				{Name: "Scenarios!$F$1", Values: fmt.Sprintf("Scenarios!$F$2:$F$%d", last)},
				{Name: "Scenarios!$G$1", Values: fmt.Sprintf("Scenarios!$G$2:$G$%d", last)},
				{Name: "Scenarios!$H$1", Values: fmt.Sprintf("Scenarios!$H$2:$H$%d", last)},
			},
			Title:     excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Duration by fitness test"}}},
			XAxis:     chartXAxis(),
			YAxis:     excelize.ChartAxis{Title: excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Duration (ms)"}}}, Font: excelize.Font{Color: "000000"}},
			Legend:    excelize.ChartLegend{Position: "bottom"},
			Dimension: excelize.ChartDimension{Width: 1000, Height: 400},
		}); err != nil {
			return fmt.Errorf("duration chart: %w", err)
		}
	}
	return writeStatsBlock(f, sheet, 23, [][2]string{
		{"Average mean (ms)", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$F$2:$F$%d),0)", last)},
		{"Average p50 (ms)", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$G$2:$G$%d),0)", last)},
		{"Average p90 (ms)", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$H$2:$H$%d),0)", last)},
		{labelFitnessTests, fmt.Sprintf(formulaCountScenarios, last)},
	})
}

// writeQualityChartSheet puts the reference-grounded quality (REFLECTS judge score,
// column O) and the composite scenario grade (column P) per fitness test on their
// own sheet — the quality counterpart to the Duration chart, since judgment quality
// and duration are the two headline measures — with average stats below. Y-axis is
// fixed 0-100 so quality is comparable across runs.
func writeQualityChartSheet(f *excelize.File, numScenarios int) error {
	const sheet = sheetQuality
	last := numScenarios + 1
	if numScenarios > 0 {
		zero, hundred := 0.0, 100.0
		if err := f.AddChart(sheet, "A1", &excelize.Chart{
			Type: excelize.Col,
			Series: []excelize.ChartSeries{
				{Name: "Scenarios!$O$1", Values: fmt.Sprintf("Scenarios!$O$2:$O$%d", last)},
				{Name: "Scenarios!$P$1", Values: fmt.Sprintf("Scenarios!$P$2:$P$%d", last)},
			},
			Title:     excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Quality (judgment) by fitness test"}}},
			XAxis:     chartXAxis(),
			YAxis:     excelize.ChartAxis{Title: excelize.ChartTitle{Paragraph: []excelize.RichTextRun{{Text: "Score (0-100)"}}}, Minimum: &zero, Maximum: &hundred, Font: excelize.Font{Color: "000000"}},
			Legend:    excelize.ChartLegend{Position: "bottom"},
			Dimension: excelize.ChartDimension{Width: 1000, Height: 400},
		}); err != nil {
			return fmt.Errorf("quality chart: %w", err)
		}
	}
	return writeStatsBlock(f, sheet, 23, [][2]string{
		{"Average quality", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$O$2:$O$%d),0)", last)},
		{"Average grade", fmt.Sprintf("=IFERROR(AVERAGE(Scenarios!$P$2:$P$%d),0)", last)},
		{labelFitnessTests, fmt.Sprintf(formulaCountScenarios, last)},
	})
}

// setReportColumnWidths gives each sheet readable default widths so long
// scenario names, assertion text, and timestamps aren't clipped. (Overview
// widths are set in writeOverviewSheet.)
func setReportColumnWidths(f *excelize.File) {
	type cw struct {
		start, end string
		w          float64
	}
	widths := map[string][]cw{
		sheetScenarios:  {{"A", "A", 30}, {"B", "E", 13}, {"F", "H", 16}, {"I", "I", 11}, {"J", "N", 16}, {"O", "O", 15}},
		sheetRuns:       {{"A", "A", 18}, {"B", "B", 30}, {"C", "C", 14}, {"D", "D", 9}, {"E", "E", 22}, {"F", "F", 13}, {"G", "G", 9}, {"H", "I", 17}, {"J", "L", 12}},
		sheetAssertions: {{"A", "A", 30}, {"B", "B", 9}, {"C", "C", 55}, {"D", "D", 8}, {"E", "E", 55}},
		sheetFailures:   {{"A", "A", 30}, {"B", "B", 50}, {"C", "C", 11}, {"D", "D", 18}, {"E", "E", 55}},
		sheetPassRate:   {{"A", "A", 22}, {"B", "B", 14}},
		sheetDuration:   {{"A", "A", 22}, {"B", "B", 14}},
	}
	for sheet, cws := range widths {
		for _, c := range cws {
			_ = f.SetColWidth(sheet, c.start, c.end, c.w)
		}
	}
}

// writeStatsBlock writes label/formula pairs (column A label, B value) from startRow.
func writeStatsBlock(f *excelize.File, sheet string, startRow int, rows [][2]string) error {
	for i, kv := range rows {
		r := startRow + i
		lc, _ := excelize.CoordinatesToCellName(1, r)
		vc, _ := excelize.CoordinatesToCellName(2, r)
		if err := f.SetCellValue(sheet, lc, kv[0]); err != nil {
			return err
		}
		if err := f.SetCellFormula(sheet, vc, strings.TrimPrefix(kv[1], "=")); err != nil {
			return err
		}
	}
	return nil
}

// durationStats returns (mean, p50, p90) of the given durations in MiB.
// Empty input returns zeros. Single-element returns the value for all
// three.
func durationStats(durations []int64) (mean, p50, p90 int64) {
	if len(durations) == 0 {
		return 0, 0, 0
	}
	sorted := make([]int64, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum int64
	for _, d := range sorted {
		sum += d
	}
	mean = sum / int64(len(sorted))

	p50 = percentile(sorted, 50)
	p90 = percentile(sorted, 90)
	return
}

// percentile returns the p-th percentile of an ascending-sorted slice using
// nearest-rank (Hyndman–Fan type 1). p in [0,100].
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int(math.Ceil(float64(p)/100.0*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}
