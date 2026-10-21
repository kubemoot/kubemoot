/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The record a server was left with by the earlier recipe: Failed on the embedding
// model, no per-model results, and no calibration request on the status at all.
func legacyFailedServer() *aiv1alpha1.ModelServer {
	failedAt := metav1.NewTime(time.Now().Add(-time.Hour))
	return calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt,
		Message: legacyFailureSummary + calEmbedModel + ": POST /api/generate: status 400"}, "0.12.0")
}

func TestRequestCalibration_asksAgainForALegacyFailureWithNoRequestOnTheStatus(t *testing.T) {
	p := legacyFailedServer()
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r := busReconciler(t, cal, p)

	if got := r.requestCalibration(context.Background(), p); got != nil {
		t.Fatalf("waiting: %v", got.err())
	}
	if len(cal.started) != 1 {
		t.Errorf("started %v, want one calibration", cal.started)
	}
}

// The record ollama-cpu was left with: the earlier request (key new, marked with the
// earlier retry's note) and a Failed run recorded in the very second the request was
// stamped, because it failed at once. It is asked again, once.
func TestRequestCalibration_asksAgainForAFailureRecordedInTheSecondOfTheRequest(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 21, 0, 8, 7, 0, time.UTC))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &at,
		Message: legacyFailureSummary + calEmbedModel + ": POST /api/generate: status 400"}, "0.40.5")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: at,
		Reason: "the server has not been calibrated (asked again after a failure on the bus)"}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r := busReconciler(t, cal, p)

	r.requestCalibration(context.Background(), p)
	if len(cal.started) != 1 || !strings.HasSuffix(p.Status.CalibrationRequest.Reason, retryNote) {
		t.Fatalf("not asked again: %v %+v", cal.started, p.Status.CalibrationRequest)
	}
	// It fails the same way again, in the same second: the mark stops a second retry.
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &at,
		Message: legacyFailureSummary + calEmbedModel + ": POST /api/generate: status 400"}
	r.requestCalibration(context.Background(), p)
	if len(cal.started) != 1 {
		t.Errorf("retried a second time: %v", cal.started)
	}
}

// A busy server is not asked again until it is idle.
func TestRequestCalibration_theRetryWaitsForTheServerToBeIdle(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 10, 21, 0, 8, 7, 0, time.UTC))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &at,
		Message: legacyFailureSummary + calEmbedModel + ": status 400"}, "0.40.5")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: at}
	cal := &busCalibrator{}
	r := busReconciler(t, cal, p)
	r.requestCalibration(context.Background(), p)
	if len(cal.started) != 0 {
		t.Errorf("asked while crew calls held the server: %v", cal.started)
	}
}
