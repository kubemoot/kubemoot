/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// A scheduler bucket that does not exist yet (the NATS bootstrap has not run) is a
// state to wait out: the run is Waiting with the reason WaitingForBus and the
// Calibrated condition is Unknown, not Failed. Once the bucket exists, the next
// run calibrates and the wait is gone.
func TestCalibrateServer_waitsForTheBucket_thenCalibrates(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.kv.ListErr = fmt.Errorf("reading tickets: %w", bus.AsNotReady(nats.ErrStreamNotFound))

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "waiting for the bus", err == nil && cal.Phase == PhaseWaiting &&
		cal.Reason == aiv1alpha1.CalibrationReasonWaitingForBus && strings.Contains(cal.Message, "waiting for the bus"), cal)
	check(t, "no model was loaded for a probe that could not claim a ticket", len(f.eng.Unloads) == 0, f.eng.Unloads)

	f.kv.ListErr = nil
	_, err = calibrate(t, f, map[string]any{argServer: gpu})
	cal = getServer(t, f).Status.Calibration
	check(t, "calibrated with no manual reset", err == nil && cal.Phase == PhaseCalibrated && cal.Reason == "", cal)
}

// NATS unreachable is the same wait, not a failure.
func TestCalibrateServer_waitsWhileNATSIsUnreachable(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.kv.ListErr = bus.AsNotReady(nats.ErrNoServers)

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "waiting, not failed", err == nil && cal.Phase == PhaseWaiting &&
		cal.Reason == aiv1alpha1.CalibrationReasonWaitingForBus, cal)
}

// A measurement that fails for a reason of its own stays Failed, with no wait reason.
func TestCalibrateServer_aGenuineFailureStaysFailed(t *testing.T) {
	f := newFixture(t, readyServer())
	f.eng.Sizes[model] = 9_000
	f.eng.Fail = true

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "failed", err == nil && cal.Phase == PhaseFailed && cal.Reason == "", cal)
	check(t, "this server writes no condition", len(getServer(t, f).Status.Conditions) == 0, nil)
}
