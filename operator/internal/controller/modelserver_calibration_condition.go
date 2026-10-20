/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// conditionTypeCalibrated is the condition that says whether the server is calibrated.
	conditionTypeCalibrated = "Calibrated"
	// reasonRecalibrationWaitingForBus is the reason of a server calibrated before
	// whose newer calibration waits for the bus.
	reasonRecalibrationWaitingForBus = "RecalibrationWaitingForBus"
)

// setCalibratedCondition sets the server's Calibrated condition in memory from its
// calibration record and why a calibration waits for the bus (nil when it does not).
// The operator is the only writer of this condition: accelerator-mcp reports its run
// in status.calibration (phase, reason, message) and nothing else, so the condition
// cannot flap between two writers. The reconcile's status update stores it.
func setCalibratedCondition(p *aiv1alpha1.ModelServer, waiting error) {
	cond := calibratedConditionFor(p.Status.Calibration, waiting)
	if cond == nil {
		meta.RemoveStatusCondition(&p.Status.Conditions, conditionTypeCalibrated)
		return
	}
	meta.SetStatusCondition(&p.Status.Conditions, *cond)
}

// calibratedConditionFor is the one definition of the Calibrated condition:
//
//   - Waiting for the bus, with results from an earlier calibration: True, reason
//     RecalibrationWaitingForBus. The earlier results are still valid and in use, so
//     the server stays Calibrated and the reason says a newer calibration waits.
//   - Waiting for the bus, never calibrated: Unknown, reason WaitingForBus.
//   - Otherwise by the calibration phase: Calibrated is True; Calibrating, Waiting
//     (crew calls hold the server) and Failed are False with the phase as the reason.
//   - No calibration record and nothing waiting: no condition.
//
// The message comes from the current cause on every call, so it never goes stale.
func calibratedConditionFor(cal *aiv1alpha1.ServerCalibration, waiting error) *metav1.Condition {
	busWait := waiting != nil || (cal != nil && cal.Reason == aiv1alpha1.CalibrationReasonWaitingForBus)
	if busWait {
		return busWaitCondition(cal, waiting)
	}
	if cal == nil || cal.Phase == "" {
		return nil
	}
	cond := &metav1.Condition{Type: conditionTypeCalibrated, Status: metav1.ConditionFalse,
		Reason: cal.Phase, Message: cal.Message}
	if cal.Phase == calibrationPhaseCalibrated {
		cond.Status = metav1.ConditionTrue
	}
	return cond
}

func busWaitCondition(cal *aiv1alpha1.ServerCalibration, waiting error) *metav1.Condition {
	message := "waiting for the bus"
	switch {
	case waiting != nil:
		message = "waiting for the bus: " + waiting.Error() +
			"; the calibration continues when NATS and its scheduler buckets are ready"
	case cal != nil && cal.Message != "":
		message = cal.Message
	}
	if cal != nil && cal.CompletedAt != nil {
		return &metav1.Condition{Type: conditionTypeCalibrated, Status: metav1.ConditionTrue,
			Reason: reasonRecalibrationWaitingForBus, Message: message}
	}
	return &metav1.Condition{Type: conditionTypeCalibrated, Status: metav1.ConditionUnknown,
		Reason: aiv1alpha1.CalibrationReasonWaitingForBus, Message: message}
}
