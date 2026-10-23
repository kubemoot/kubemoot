/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"maps"
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func vllmServerCalibrated(models ...aiv1alpha1.ModelCalibration) *aiv1alpha1.ModelServer {
	return &aiv1alpha1.ModelServer{
		Spec:   aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM},
		Status: aiv1alpha1.ModelServerStatus{Calibration: &aiv1alpha1.ServerCalibration{Models: models}},
	}
}

func TestVLLMWindowsComeFromTheEngineReport(t *testing.T) {
	var state ServerState
	applyContextWindows(&state, vllmServerCalibrated(), map[string]int{candModel14: 32768})
	if !maps.Equal(state.LoadedModelContextLengths, map[string]int{candModel14: 32768}) || state.ContextLength != 32768 {
		t.Errorf("state = %+v", state)
	}
}

func TestVLLMModelsWithDifferentWindowsPublishNoServerWideWindow(t *testing.T) {
	var state ServerState
	applyContextWindows(&state, vllmServerCalibrated(), map[string]int{candModel14: 32768, candModel32: 16384})
	if state.LoadedModelContextLengths[candModel14] != 32768 || state.LoadedModelContextLengths[candModel32] != 16384 {
		t.Errorf("per-model windows = %v", state.LoadedModelContextLengths)
	}
	if state.ContextLength != 0 {
		t.Errorf("windows differ, so ContextLength must stay 0, got %d", state.ContextLength)
	}
}

func TestVLLMCalibrationFillsWhatTheEngineDidNotReport(t *testing.T) {
	server := vllmServerCalibrated(
		aiv1alpha1.ModelCalibration{Model: candModel14, ContextLength: 4096},
		aiv1alpha1.ModelCalibration{Model: candModel32, ContextLength: 32768},
	)
	var state ServerState
	applyContextWindows(&state, server, map[string]int{candModel14: 32768})
	if state.LoadedModelContextLengths[candModel14] != 32768 {
		t.Errorf("the live window must win over calibration: %v", state.LoadedModelContextLengths)
	}
	if state.LoadedModelContextLengths[candModel32] != 32768 || state.ContextLength != 32768 {
		t.Errorf("calibration fallback missing: %+v", state)
	}
}

func TestVLLMWithNoWindowAnywherePublishesNone(t *testing.T) {
	var state ServerState
	applyContextWindows(&state, vllmServerCalibrated(aiv1alpha1.ModelCalibration{Model: "m"}), nil)
	if state.LoadedModelContextLengths != nil || state.ContextLength != 0 {
		t.Errorf("an unknown window must stay unknown: %+v", state)
	}
}

func TestOllamaWindowsAreLeftAlone(t *testing.T) {
	server := &aiv1alpha1.ModelServer{
		Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama},
		Status: aiv1alpha1.ModelServerStatus{Calibration: &aiv1alpha1.ServerCalibration{
			Models: []aiv1alpha1.ModelCalibration{{Model: selModel8, ContextLength: 4096}},
		}},
	}
	state := ServerState{LoadedModelContextLengths: map[string]int{candModel14: 8192}}
	applyContextWindows(&state, server, nil)
	if !maps.Equal(state.LoadedModelContextLengths, map[string]int{candModel14: 8192}) || state.ContextLength != 0 {
		t.Errorf("the Ollama path must not change: %+v", state)
	}
}

func TestAWindowAlreadyPublishedIsKept(t *testing.T) {
	state := ServerState{LoadedModelContextLengths: map[string]int{"m": 1000}}
	applyContextWindows(&state, vllmServerCalibrated(), map[string]int{"m": 2000})
	if state.LoadedModelContextLengths["m"] != 1000 {
		t.Errorf("a published window is never replaced: %v", state.LoadedModelContextLengths)
	}
}
