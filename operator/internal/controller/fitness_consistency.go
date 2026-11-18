/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_consistency.go reports how reliably each scenario passes, beside the
// mean quality: the per-trial pass rate, pass^k (the chance that all k trials
// pass), and a flag for scenarios that never pass.
package controller

import (
	"math"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"k8s.io/utils/ptr"
)

// minTrialsToFlag is the fewest finished trials at which a scenario with no
// pass is flagged for a check. One or two failures are not yet a pattern.
const minTrialsToFlag = 3

// passRatePercent is passed out of trials as a 0-100 integer, rounded.
func passRatePercent(passed, trials int32) int32 {
	if trials <= 0 {
		return 0
	}
	return roundScore(100 * float64(passed) / float64(trials))
}

// passAllKPercent is the chance, 0-100 rounded, that all k=trials trials pass,
// estimated as the observed per-trial pass rate raised to k.
func passAllKPercent(passed, trials int32) int32 {
	if trials <= 0 {
		return 0
	}
	return roundScore(100 * math.Pow(float64(passed)/float64(trials), float64(trials)))
}

// scenarioNeedsCheck reports whether a scenario never passed across enough
// trials that its script or reference is the likelier fault.
func scenarioNeedsCheck(passed, trials int32) bool {
	return trials >= minTrialsToFlag && passed == 0
}

// suiteConsistency rolls the per-scenario rows up to the suite. It is nil when
// no scenario has a finished trial.
func suiteConsistency(rows []kubemootv1alpha1.SuiteScenarioResult, purpose kubemootv1alpha1.FitnessSuitePurpose) *kubemootv1alpha1.SuiteConsistency {
	var sum, measured, toCheck, regressions int32
	for _, row := range rows {
		if row.PassAllK == nil {
			continue
		}
		measured++
		sum += *row.PassAllK
		if row.CheckScenario {
			toCheck++
		}
		if row.Passed < row.Iterations {
			regressions++
		}
	}
	if measured == 0 {
		return nil
	}
	out := &kubemootv1alpha1.SuiteConsistency{
		MeanPassAllK:     ptr.To(roundScore(float64(sum) / float64(measured))),
		ScenariosToCheck: toCheck,
	}
	if purpose == kubemootv1alpha1.FitnessSuitePurposeRegression {
		out.Regressions = ptr.To(regressions)
	}
	return out
}

// setScenarioRollup stores the per-scenario rows on status and the suite-level
// consistency computed from them.
func setScenarioRollup(suite *kubemootv1alpha1.CrewFitnessSuite, rows []kubemootv1alpha1.SuiteScenarioResult) {
	suite.Status.Scenarios = rows
	suite.Status.Consistency = suiteConsistency(rows, suite.Spec.Purpose)
}
