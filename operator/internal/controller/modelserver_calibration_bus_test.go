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
	"sigs.k8s.io/controller-runtime/pkg/client"
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

func busReconciler(t *testing.T, cal Calibrator, p *aiv1alpha1.ModelServer) (*ModelServerReconciler, client.Client) {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	return &ModelServerReconciler{Client: cli, Scheme: scheme, Calibration: cal}, cli
}

func calibratedCondition(t *testing.T, cli client.Client) *metav1.Condition {
	t.Helper()
	stored := &aiv1alpha1.ModelServer{}
	if err := cli.Get(context.Background(), client.ObjectKey{Namespace: defaultOperatorNamespace, Name: calTestServer}, stored); err != nil {
		t.Fatal(err)
	}
	return meta.FindStatusCondition(stored.Status.Conditions, conditionTypeCalibrated)
}

// A bucket that does not exist yet is a state to wait out: nothing starts, the
// Calibrated condition says what it waits for, and once the bucket appears the
// calibration starts with no one's help.
func TestRequestCalibration_waitsForTheBucketsThenStarts(t *testing.T) {
	cal := &busCalibrator{busErr: errNoBucket}
	p := calServer(nil, "0.12.0")
	r, cli := busReconciler(t, cal, p)

	if got := r.requestCalibration(context.Background(), p); got != aiv1alpha1.CalibrationReasonWaitingForBus {
		t.Fatalf("waiting reason = %q", got)
	}
	if len(cal.started) != 0 || p.Status.CalibrationRequest != nil {
		t.Errorf("started with no bus: %v %+v", cal.started, p.Status.CalibrationRequest)
	}
	assertWaitingForBus(t, calibratedCondition(t, cli))

	cal.busErr = nil
	if got := r.requestCalibration(context.Background(), p); got != "" {
		t.Errorf("still waiting with the bus ready: %q", got)
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
	r, _ := busReconciler(t, cal, p)

	if got := r.requestCalibration(context.Background(), p); got != aiv1alpha1.CalibrationReasonWaitingForBus || len(cal.started) != 0 {
		t.Fatalf("NATS down: reason %q, started %v", got, cal.started)
	}
	cal.busErr = nil
	if got := r.requestCalibration(context.Background(), p); got != "" || len(cal.started) != 1 {
		t.Errorf("NATS back: reason %q, started %v", got, cal.started)
	}
}

// A server that already holds results keeps its True condition while a newer need waits.
func TestWaitForBus_keepsACalibratedCondition(t *testing.T) {
	p := calServer(calibrated("0.12.0", calTestModelA), "0.12.0")
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: conditionTypeCalibrated,
		Status: metav1.ConditionTrue, Reason: calibrationPhaseCalibrated})
	r, cli := busReconciler(t, &busCalibrator{}, p)
	r.waitForBus(context.Background(), p, errNoBucket)
	if cond := calibratedCondition(t, cli); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Errorf("a calibrated server's condition changed: %+v", cond)
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
	r, _ := busReconciler(t, cal, p)
	if got := r.requestCalibration(context.Background(), p); got != "" || len(cal.started) != 1 {
		t.Errorf("did not recalibrate: reason %q, started %v", got, cal.started)
	}
}

func TestRequestCalibration_aMeasurementFailureStaysFailed(t *testing.T) {
	asked := metav1.NewTime(time.Now().Add(-time.Hour))
	failedAt := metav1.NewTime(time.Now().Add(-30 * time.Minute))
	p := calServer(&aiv1alpha1.ServerCalibration{Phase: calibrationPhaseFailed, UpdatedAt: &failedAt,
		Message: "no model could be measured: qwen3:8b: engine returned 500"}, "")
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: asked}
	cal := &busCalibrator{fakeCalibrator: fakeCalibrator{idle: true}}
	r, _ := busReconciler(t, cal, p)
	if got := r.requestCalibration(context.Background(), p); got != "" || len(cal.started) != 0 {
		t.Errorf("a measurement failure was retried: reason %q, started %v", got, cal.started)
	}
}

func TestRetryWhileWaiting(t *testing.T) {
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Minute}, aiv1alpha1.CalibrationReasonWaitingForBus); got.RequeueAfter != calibrationRetry {
		t.Errorf("waiting requeue = %v", got.RequeueAfter)
	}
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Second}, aiv1alpha1.CalibrationReasonWaitingForBus); got.RequeueAfter != 5*time.Second {
		t.Errorf("a sooner requeue was lengthened: %v", got.RequeueAfter)
	}
	if got := retryWhileWaiting(ctrl.Result{RequeueAfter: 5 * time.Minute}, ""); got.RequeueAfter != 5*time.Minute {
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
	r, _ := busReconciler(t, cal, p)
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

// A WaitingForBus condition the operator set is cleared once the bus holds nothing back.
func TestRequestCalibration_clearsAStaleBusWait(t *testing.T) {
	p := calServer(nil, "0.12.0")
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: conditionTypeCalibrated,
		Status: metav1.ConditionUnknown, Reason: aiv1alpha1.CalibrationReasonWaitingForBus})
	// The need is already requested and the run is not paused, so nothing is asked.
	p.Status.CalibrationRequest = &aiv1alpha1.CalibrationRequest{Key: needKeyNew, RequestedAt: metav1.Now()}
	r, cli := busReconciler(t, &busCalibrator{}, p)
	r.requestCalibration(context.Background(), p)
	if c := calibratedCondition(t, cli); c != nil {
		t.Errorf("stale wait condition kept: %+v", c)
	}
}
