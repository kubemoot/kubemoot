/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package tools

import (
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const embedModel = "nomic-embed-text:latest"

// resultOf is the recorded result for the model.
func resultOf(cal *aiv1alpha1.ServerCalibration, name string) *aiv1alpha1.ModelCalibration {
	for i := range cal.Models {
		if cal.Models[i].Model == name {
			return &cal.Models[i]
		}
	}
	return nil
}

// chatAndEmbedding is a server with a chat model the catalog lists and an embedding
// model a Model object binds to it (embedding models never join the catalog).
func chatAndEmbedding(t *testing.T) fixture {
	t.Helper()
	p := withAvailable(map[string]int64{model: 9_000, embedModel: 300})
	p.Status.Models = []aiv1alpha1.CatalogModel{{Name: model, Present: true}}
	bound := &aiv1alpha1.Model{ObjectMeta: metav1.ObjectMeta{Name: "embed", Namespace: crewNS},
		Spec: aiv1alpha1.ModelSpec{ServerRef: gpu, Model: embedModel}}
	f := newFixture(t, p, bound)
	f.eng.Sizes[model], f.eng.Sizes[embedModel] = 9_000, 300
	f.eng.Embedding = map[string]bool{embedModel: true}
	return f
}

// The chat model is measured, the embedding model is skipped as not applicable (never
// loaded or sent a request), and the server is Calibrated. The chat model is found
// through the catalog although no Model object names it.
func TestCalibrateServer_skipsEmbeddingModels_andMeasuresTheChatModel(t *testing.T) {
	f := chatAndEmbedding(t)

	out, err := calibrate(t, f, map[string]any{argServer: gpu})
	check(t, "both are targets", err == nil && len(out["models"].([]any)) == 2, out)

	cal := getServer(t, f).Status.Calibration
	chat, embed := resultOf(cal, model), resultOf(cal, embedModel)
	check(t, "calibrated", cal.Phase == PhaseCalibrated && cal.CompletedAt != nil && cal.Reason == "", cal)
	check(t, "chat measured", chat != nil && chat.Status == aiv1alpha1.ModelCalibrationMeasured &&
		fullyMeasured(*chat), chat)
	check(t, "embedding skipped, not failed", embed != nil && embed.Status == aiv1alpha1.ModelCalibrationSkipped &&
		embed.Fits && embed.Reason != "" && embed.GenerateTokensPerSecond == 0, embed)
	check(t, "the embedding model was never asked to generate", slices.Equal(f.eng.Unloads, []string{model}), f.eng.Unloads)
	check(t, "the message names the skip", strings.Contains(cal.Message, embedModel), cal.Message)
}

func assertFailedAndMeasured(t *testing.T, cal *aiv1alpha1.ServerCalibration) {
	t.Helper()
	failed, ok := resultOf(cal, model), resultOf(cal, small)
	check(t, "the failure is recorded with its reason", failed != nil &&
		failed.Status == aiv1alpha1.ModelCalibrationFailed && failed.Reason != "", failed)
	check(t, "the other model is measured", ok != nil && ok.Status == aiv1alpha1.ModelCalibrationMeasured &&
		ok.GenerateTokensPerSecond > 0, ok)
}

// One chat model failing does not stop the others: each has its own result, and the
// server is Failed only because a chat model could not be measured. Run again once
// the cause is gone, the failed model is measured and the server is Calibrated.
func TestCalibrateServer_oneFailureDoesNotStopTheOthers(t *testing.T) {
	f := newFixture(t, withAvailable(map[string]int64{model: 9_000, small: 800}))
	f.eng.Sizes[model], f.eng.Sizes[small] = 9_000, 800
	f.eng.FailModels = map[string]bool{model: true}

	_, err := calibrate(t, f, map[string]any{argServer: gpu, argMods: []string{model, small}})
	cal := getServer(t, f).Status.Calibration
	check(t, "failed because a chat model could not be measured", err == nil && cal.Phase == PhaseFailed &&
		cal.CompletedAt == nil && strings.Contains(cal.Message, model), cal)
	assertFailedAndMeasured(t, cal)

	f.eng.FailModels = nil
	_, err = calibrate(t, f, map[string]any{argServer: gpu, argMods: []string{model}})
	cal = getServer(t, f).Status.Calibration
	check(t, "measured on the next run", err == nil && cal.Phase == PhaseCalibrated &&
		resultOf(cal, model).Status == aiv1alpha1.ModelCalibrationMeasured && len(cal.Models) == 2, cal)
}

// A server with only embedding models has nothing to measure: it is Calibrated, with
// a message that says so, and the models are recorded as skipped.
func TestCalibrateServer_onlyEmbeddingModels_isCalibratedWithNothingToMeasure(t *testing.T) {
	f := newFixture(t, withAvailable(map[string]int64{embedModel: 300}))
	f.eng.Sizes[embedModel] = 300
	f.eng.Embedding = map[string]bool{embedModel: true}

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "calibrated", err == nil && cal.Phase == PhaseCalibrated && cal.CompletedAt != nil, cal)
	check(t, "skipped, with nothing to measure said", len(cal.Models) == 1 &&
		cal.Models[0].Status == aiv1alpha1.ModelCalibrationSkipped &&
		strings.Contains(cal.Message, "no model that chats"), cal)
	check(t, "nothing was sent", f.eng.Generates == 0, f.eng.Generates)
}

func TestConcludeRecipe(t *testing.T) {
	measured := aiv1alpha1.ModelCalibration{Model: "a", Status: aiv1alpha1.ModelCalibrationMeasured}
	legacy := aiv1alpha1.ModelCalibration{Model: "b"}
	skipped := aiv1alpha1.ModelCalibration{Model: "e", Status: aiv1alpha1.ModelCalibrationSkipped}
	failed := aiv1alpha1.ModelCalibration{Model: "f", Status: aiv1alpha1.ModelCalibrationFailed, Reason: "boom"}
	cases := []struct {
		name    string
		results []aiv1alpha1.ModelCalibration
		phase   string
	}{
		{"every chat model measured", []aiv1alpha1.ModelCalibration{measured, legacy, skipped}, PhaseCalibrated},
		{"nothing to measure", []aiv1alpha1.ModelCalibration{skipped}, PhaseCalibrated},
		{"a chat model failed", []aiv1alpha1.ModelCalibration{measured, failed, skipped}, PhaseFailed},
		{"every chat model failed", []aiv1alpha1.ModelCalibration{failed}, PhaseFailed},
	}
	for _, c := range cases {
		if phase, message := concludeRecipe(c.results); phase != c.phase || message == "" {
			t.Errorf("%s: %s %q", c.name, phase, message)
		}
	}
}

// When the engine cannot say whether a model embeds, the model is tried as a chat
// model: it is measured if it works, and a failure names that the check failed too.
func TestCalibrateServer_anEmbeddingCheckThatFails_fallsBackToMeasuring(t *testing.T) {
	f := newFixture(t, withAvailable(map[string]int64{model: 9_000}))
	f.eng.Sizes[model] = 9_000
	f.eng.EmbeddingErr = errors.New("show: status 500")

	_, err := calibrate(t, f, map[string]any{argServer: gpu})
	cal := getServer(t, f).Status.Calibration
	check(t, "measured despite the failed check", err == nil && cal.Phase == PhaseCalibrated &&
		resultOf(cal, model).Status == aiv1alpha1.ModelCalibrationMeasured, cal)

	f.eng.FailModels = map[string]bool{model: true}
	_, err = calibrate(t, f, map[string]any{argServer: gpu})
	cal = getServer(t, f).Status.Calibration
	check(t, "a failure says the check failed too", err == nil && cal.Phase == PhaseFailed &&
		strings.Contains(resultOf(cal, model).Reason, "could not check"), cal)
}

// record_calibration accepts a per-model status and refuses an unknown one.
func TestRecordCalibration_acceptsAModelStatus(t *testing.T) {
	f := newFixture(t, readyServer())
	args := func(status string) map[string]any {
		return map[string]any{argServer: gpu, argPh: PhaseCalibrated, argMods: []map[string]any{
			{argMod: embedModel, argFits: true, "status": status, "reason": "embedding"}}}
	}
	_, err := call(t, f.set, ToolRecordCalibration, args(aiv1alpha1.ModelCalibrationSkipped))
	cal := getServer(t, f).Status.Calibration
	check(t, "skipped recorded", err == nil && resultOf(cal, embedModel).Status == aiv1alpha1.ModelCalibrationSkipped, cal)
	_, err = call(t, f.set, ToolRecordCalibration, args("Bogus"))
	check(t, "unknown status refused", err != nil, err)
}
