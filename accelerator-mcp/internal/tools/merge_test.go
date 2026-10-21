/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// A new result replaces an earlier one for the same model, whatever the earlier
// outcome: a model that failed now does not keep the speeds it had measured before,
// and one that is measured now does not keep an earlier failure.
func TestMerge_aNewResultReplacesTheEarlierOneForTheSameModel(t *testing.T) {
	then, now := metav1.Unix(1_000, 0), metav1.Unix(2_000, 0)
	measured := aiv1alpha1.ModelCalibration{Model: model, Fits: true, Status: aiv1alpha1.ModelCalibrationMeasured,
		GenerateTokensPerSecond: 50, LoadMs: 3000}
	failed := aiv1alpha1.ModelCalibration{Model: model, Fits: true, Status: aiv1alpha1.ModelCalibrationFailed,
		Reason: "POST /api/generate: status 500"}
	other := aiv1alpha1.ModelCalibration{Model: small, Fits: true, Status: aiv1alpha1.ModelCalibrationMeasured,
		GenerateTokensPerSecond: 90}

	afterFailure := merge([]aiv1alpha1.ModelCalibration{measured, other}, []aiv1alpha1.ModelCalibration{failed}, now)
	check(t, "one entry per model", len(afterFailure) == 2, afterFailure)
	got := resultOf(&aiv1alpha1.ServerCalibration{Models: afterFailure}, model)
	check(t, "the failure replaced the measurement, with none of its figures",
		got.Status == aiv1alpha1.ModelCalibrationFailed && got.GenerateTokensPerSecond == 0 && got.LoadMs == 0 &&
			got.Reason != "" && got.CalibratedAt != nil && got.CalibratedAt.Equal(&now), got)
	untouched := resultOf(&aiv1alpha1.ServerCalibration{Models: afterFailure}, small)
	check(t, "the other model is kept", untouched.GenerateTokensPerSecond == 90, untouched)

	earlier := measured
	earlier.CalibratedAt = &then
	back := merge(afterFailure, []aiv1alpha1.ModelCalibration{earlier}, now)
	again := resultOf(&aiv1alpha1.ServerCalibration{Models: back}, model)
	check(t, "a later measurement replaces the failure",
		len(back) == 2 && again.Status == aiv1alpha1.ModelCalibrationMeasured && again.Reason == "", again)
}
