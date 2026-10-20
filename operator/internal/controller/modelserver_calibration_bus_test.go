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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// busCalibrator is a fakeCalibrator whose bus can be down: busErr is what BusReady
// and Idle report until the test clears it.
type busCalibrator struct {
	fakeCalibrator
	busErr error
}

func (b *busCalibrator) BusReady() error { return b.busErr }

func (b *busCalibrator) Idle(string) (bool, error) {
	if b.busErr != nil {
		return false, b.busErr
	}
	return b.idle, nil
}

var errNoBucket = fmt.Errorf("%w: reading tickets: %w", bus.ErrNotReady, nats.ErrStreamNotFound)

func busReconciler(t *testing.T, cal Calibrator, p *aiv1alpha1.ModelServer) *ModelServerReconciler {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	return &ModelServerReconciler{Client: cli, Scheme: scheme, Calibration: cal}
}

func calibratedCondition(p *aiv1alpha1.ModelServer) *metav1.Condition {
	return meta.FindStatusCondition(p.Status.Conditions, conditionTypeCalibrated)
}

// A bucket that does not exist yet is a state to wait out: nothing starts, the
// Calibrated condition says what it waits for, and once the bucket appears the
// calibration starts with no one's help.
func TestRequestCalibration_waitsForTheBucketsThenStarts(t *testing.T) {
	cal := &busCalibrator{busErr: errNoBucket}
	p := calServer(nil, "0.12.0")
	r := busReconciler(t, cal, p)

	got := r.requestCalibration(context.Background(), p)
	if !errors.Is(got.err(), bus.ErrNotReady) {
		t.Fatalf("waiting cause = %v", got)
	}
	if len(cal.started) != 0 || p.Status.CalibrationRequest != nil {
		t.Errorf("started with no bus: %v %+v", cal.started, p.Status.CalibrationRequest)
	}
	setCalibratedCondition(p, got.err())
	assertWaitingForBus(t, calibratedCondition(p))

	cal.busErr = nil
	if got = r.requestCalibration(context.Background(), p); got != nil {
		t.Errorf("still waiting with the bus ready: %v", got)
	}
	if len(cal.started) != 1 || p.Status.CalibrationRequest == nil || p.Status.CalibrationRequest.Key != needKeyNew {
		t.Errorf("did not start once the buckets appeared: %v %+v", cal.started, p.Status.CalibrationRequest)
	}
}

func assertWaitingForBus(t *testing.T, cond *metav1.Condition) {
	t.Helper()
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != aiv1alpha1.CalibrationReasonWaitingForBus {
		t.Errorf("condition while waiting: %+v", cond)
	}
}

// NATS unreachable while a paused calibration waits to resume: it keeps waiting,
// then resumes when NATS is back and the server idle.
func TestRequestCalibration_resumesWhenNATSComesBack(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Minute))
	paused := metav1.NewTime(time.Now())
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseWaiting,
		Reason: aiv1alpha1.CalibrationReasonWaitingForBus, UpdatedAt: &paused}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}, busErr: fmt.Errorf("%w: %w", bus.ErrNotReady, nats.ErrNoServers)}
	r := busReconciler(t, cal, p)

	if got := r.requestCalibration(context.Background(), p); !errors.Is(got.err(), bus.ErrNotReady) || len(cal.started) != 0 {
		t.Fatalf("NATS down: cause %v, started %v", got, cal.started)
	}
	cal.busErr = nil
	if got := r.requestCalibration(context.Background(), p); got != nil || len(cal.started) != 1 {
		t.Errorf("NATS back: cause %v, started %v", got, cal.started)
	}
}

// A Failed calibration is retried when it failed on the bus, and stays Failed when
// the models could not be measured.
func TestShouldAsk_distinguishesABusFailureFromAMeasurementFailure(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failedAt := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	cases := []struct {
		name, message string
		want          bool
	}{
		{"the bucket did not exist", "no model could be measured: qwen3:8b: reading tickets: nats: stream not found", true},
		{"NATS was unreachable", "no model could be measured: qwen3:8b: reading tickets: nats: timeout", true},
		{"the engine failed", "no model could be measured: qwen3:8b: engine returned 500", false},
		{"the model does not fit", "no model could be measured: qwen3:8b: out of memory", false},
	}
	for _, c := range cases {
		p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt, Message: c.message}, "")
		p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
		got, err := shouldAsk(p, calibrationNeed{Key: needKeyNew}, func() (bool, error) { return true, nil })
		if err != nil || got != c.want {
			t.Errorf("%s: ask %v (err %v), want %v", c.name, got, err, c.want)
		}
	}
}

// A calibration that Failed with a NATS error is asked again by itself, once.
func TestRequestCalibration_recoversAFailureCausedByTheBus(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failedAt := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt,
		Message: "no model could be measured: qwen3:8b: reading tickets: nats: stream not found"}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r := busReconciler(t, cal, p)
	if got := r.requestCalibration(context.Background(), p); got != nil || len(cal.started) != 1 {
		t.Errorf("did not recalibrate: cause %v, started %v", got, cal.started)
	}
}

func TestRequestCalibration_aMeasurementFailureStaysFailed(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failedAt := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt,
		Message: "no model could be measured: qwen3:8b: engine returned 500"}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r := busReconciler(t, cal, p)
	if got := r.requestCalibration(context.Background(), p); got != nil || len(cal.started) != 0 {
		t.Errorf("a measurement failure was retried: cause %v, started %v", got, cal.started)
	}
}

func TestRetryWhileWaiting(t *testing.T) {
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Minute}, &busWait{errNoBucket}); got.RequeueAfter != calibrationRetry {
		t.Errorf("waiting requeue = %v", got.RequeueAfter)
	}
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Second}, &busWait{errNoBucket}); got.RequeueAfter != 5*time.Second {
		t.Errorf("a sooner requeue was lengthened: %v", got.RequeueAfter)
	}
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Minute}, nil); got.RequeueAfter != 5*time.Minute {
		t.Errorf("requeue changed with nothing waiting: %v", got.RequeueAfter)
	}
}

// MCPCalibrator reads an unreachable NATS as not ready, never as an idle server, and
// has nothing to wait for when NATS is not configured.
func TestMCPCalibrator_unreachableNATSIsNotReady(t *testing.T) {
	down := MCPCalibrator{Publisher: kubemootnats.NewPublisher("nats://127.0.0.1:1")}
	if err := down.BusReady(); !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("BusReady with NATS down: %v", err)
	}
	if idle, err := down.Idle(calTestServer); idle || !errors.Is(err, bus.ErrNotReady) {
		t.Errorf("Idle with NATS down: %v %v", idle, err)
	}
	if err := (MCPCalibrator{}).BusReady(); err != nil {
		t.Errorf("BusReady without NATS configured: %v", err)
	}
}

// The retry of a bus failure is once per request: a second Failed under the retry
// mark stays Failed, so it cannot loop.
func TestRequestCalibration_aBusFailureIsRetriedOnlyOnce(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failedAt := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt,
		Message: "no model could be measured: qwen3:8b: nats: timeout"}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r := busReconciler(t, cal, p)
	r.requestCalibration(context.Background(), p)
	if len(cal.started) != 1 || !strings.HasSuffix(p.Status.CalibrationRequest.Reason, busRetryNote) {
		t.Fatalf("first retry: %v %+v", cal.started, p.Status.CalibrationRequest)
	}
	failedAgain := metav1.NewTime(time.Now().Add(time.Minute))
	p.Status.Calibration = &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAgain,
		Message: "no model could be measured: qwen3:8b: nats: timeout"}
	r.requestCalibration(context.Background(), p)
	if len(cal.started) != 1 {
		t.Errorf("retried a second time: %v", cal.started)
	}
}

// The Calibrated condition has one definition, derived from the calibration record
// and the bus wait. A server calibrated before stays True while a newer calibration
// waits for the bus, with a reason that says so.
func TestCalibratedConditionFor(t *testing.T) {
	done := metav1.Now()
	results := &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseCalibrated, CompletedAt: &done}
	pausedOnBus := &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseWaiting,
		Reason: aiv1alpha1.CalibrationReasonWaitingForBus, Message: "paused on the bus"}
	pausedAfterResults := &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseWaiting, CompletedAt: &done,
		Reason: aiv1alpha1.CalibrationReasonWaitingForBus}
	cases := []struct {
		name    string
		cal     *aiv1alpha1.ServerCalibration
		waiting error
		want    metav1.ConditionStatus
		reason  string
	}{
		{"never calibrated, waiting", nil, errNoBucket, metav1.ConditionUnknown, aiv1alpha1.CalibrationReasonWaitingForBus},
		{"a run paused on the bus", pausedOnBus, nil, metav1.ConditionUnknown, aiv1alpha1.CalibrationReasonWaitingForBus},
		{"calibrated before, a new need waits", results, errNoBucket, metav1.ConditionTrue, reasonRecalibrationWaitingForBus},
		{"calibrated before, a rerun paused on the bus", pausedAfterResults, nil, metav1.ConditionTrue, reasonRecalibrationWaitingForBus},
		{"calibrated", results, nil, metav1.ConditionTrue, calibrationPhaseCalibrated},
		{"calibrating", &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseCalibrating}, nil, metav1.ConditionFalse, calibrationPhaseCalibrating},
		{"paused for crew calls", &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseWaiting}, nil, metav1.ConditionFalse, calibrationPhaseWaiting},
		{"failed", &aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed}, nil, metav1.ConditionFalse, calibrationPhaseFailed},
	}
	for _, c := range cases {
		got := calibratedConditionFor(c.cal, c.waiting)
		if got == nil || got.Status != c.want || got.Reason != c.reason {
			t.Errorf("%s: %+v, want %s/%s", c.name, got, c.want, c.reason)
		}
	}
	if got := calibratedConditionFor(nil, nil); got != nil {
		t.Errorf("no record and nothing waiting: %+v", got)
	}
}

// The wait message follows the current cause on every reconcile, and the condition
// goes back to the record's state once the bus is ready.
func TestSetCalibratedCondition_followsTheCurrentCause(t *testing.T) {
	p := calServer(nil, "0.12.0")
	setCalibratedCondition(p, errNoBucket)
	first := calibratedCondition(p).Message
	unreachable := fmt.Errorf("%w: %w", bus.ErrNotReady, nats.ErrNoServers)
	setCalibratedCondition(p, unreachable)
	second := calibratedCondition(p).Message
	if first == second || !strings.Contains(second, nats.ErrNoServers.Error()) || strings.Contains(second, nats.ErrStreamNotFound.Error()) {
		t.Errorf("the message did not follow the cause:\n%s\n%s", first, second)
	}
	setCalibratedCondition(p, nil)
	if c := calibratedCondition(p); c != nil {
		t.Errorf("a wait condition outlived the wait: %+v", c)
	}
}

// Every status update derives the Calibrated condition, so a stale wait condition is
// removed or restated on a path that never reaches the calibration step (a server
// that cannot be reached), and a server with results keeps True.
func TestUpdateStatus_derivesTheCalibratedCondition(t *testing.T) {
	stale := calServer(nil, "")
	meta.SetStatusCondition(&stale.Status.Conditions, metav1.Condition{Type: conditionTypeCalibrated,
		Status: metav1.ConditionUnknown, Reason: aiv1alpha1.CalibrationReasonWaitingForBus})
	r := busReconciler(t, nil, stale)
	if _, err := r.updateStatus(context.Background(), stale, false, "Failed", "cannot connect"); err != nil {
		t.Fatal(err)
	}
	if c := calibratedCondition(stale); c != nil {
		t.Errorf("a stale wait condition survived an early return: %+v", c)
	}

	done := metav1.Now()
	known := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseCalibrated, CompletedAt: &done}, "")
	r = busReconciler(t, nil, known)
	if _, err := r.updateStatusWaiting(context.Background(), known, true, phaseReady, "ok", errNoBucket); err != nil {
		t.Fatal(err)
	}
	c := calibratedCondition(known)
	if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reasonRecalibrationWaitingForBus {
		t.Errorf("a calibrated server waiting for the bus: %+v", c)
	}
}
