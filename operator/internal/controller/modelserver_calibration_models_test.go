/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"slices"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"
)

const (
	calChatModel   = "qwen3:8b"
	calEmbedModel  = "nomic-embed-text:latest"
	calBrokenModel = "broken:1b"
)

func skippedModel(name string) aiv1alpha1.ModelCalibration {
	return aiv1alpha1.ModelCalibration{Model: name, Fits: true, Status: aiv1alpha1.ModelCalibrationSkipped}
}

func failedModel() aiv1alpha1.ModelCalibration {
	return aiv1alpha1.ModelCalibration{Model: calBrokenModel, Fits: true, Status: aiv1alpha1.ModelCalibrationFailed, Reason: "boom"}
}

// A skipped model is answered: the operator does not ask again for it, so an
// embedding model on the server never keeps a calibration being requested.
func TestUncalibrated_aSkippedModelIsAnswered_aFailedOneIsNot(t *testing.T) {
	cal := &aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{
		{Model: calChatModel}, skippedModel(calEmbedModel), failedModel()}}
	got := uncalibrated(cal, []string{calChatModel, calEmbedModel, calBrokenModel})
	if !slices.Equal(got, []string{calBrokenModel}) {
		t.Errorf("uncalibrated = %v, want only the failed model", got)
	}
}

// A calibration whose only results are failures is still a server never calibrated,
// so a Failed run keeps its need and is retried when the server recovers.
func TestNeverCalibrated_countsOnlyAnsweredModels(t *testing.T) {
	if !neverCalibrated(&aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{failedModel()}}) {
		t.Error("only a failed model: should still count as never calibrated")
	}
	if neverCalibrated(&aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{skippedModel(calEmbedModel)}}) {
		t.Error("a skipped model is an answer")
	}
}

// Speeds come only from models that were measured.
func TestCalibratedSpeeds_ignoresSkippedAndFailedModels(t *testing.T) {
	chat := aiv1alpha1.ModelCalibration{Model: calChatModel, Fits: true, GenerateTokensPerSecond: 50}
	speeds, _ := perf.CalibratedSpeeds(&aiv1alpha1.ServerCalibration{Models: []aiv1alpha1.ModelCalibration{
		chat, skippedModel(calEmbedModel), failedModel()}})
	if len(speeds) != 1 || speeds[calChatModel] != 50 {
		t.Errorf("speeds = %v", speeds)
	}
}
