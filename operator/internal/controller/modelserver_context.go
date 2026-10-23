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
	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// applyContextWindows publishes the context window of each model a vLLM server
// serves. The engine's own report (reported, from /v1/models) is the source. A
// model the engine was asked about and gave no window for publishes none, because
// a calibrated window can be stale. Only a model whose server could not be asked
// at all (absent from reported) falls back to the window calibration measured.
// The server-wide ContextLength is set only when every served model has a window
// and they are all the same, so a model without one never inherits another's.
// Other engines publish their windows elsewhere and are left alone.
func applyContextWindows(state *ServerState, server *aiv1alpha1.ModelServer, reported map[string]int) {
	if server.Spec.Type != aiv1alpha1.ServerTypeVLLM {
		return
	}
	calibrated := calibratedWindows(server)
	served := servedModelNames(server)
	for _, model := range served {
		window, asked := reported[model]
		if !asked {
			window = calibrated[model]
		}
		setContextWindow(state, model, window)
	}
	if state.ContextLength <= 0 {
		state.ContextLength = commonWindow(state.LoadedModelContextLengths, served)
	}
}

// servedModelNames is every served name of the server's vLLM servers.
func servedModelNames(server *aiv1alpha1.ModelServer) []string {
	servers := server.VLLMServers()
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Model)
	}
	return names
}

// calibratedWindows is the window calibration measured for each model.
func calibratedWindows(server *aiv1alpha1.ModelServer) map[string]int {
	out := map[string]int{}
	if server.Status.Calibration == nil {
		return out
	}
	for _, m := range server.Status.Calibration.Models {
		out[m.Model] = m.ContextLength
	}
	return out
}

// setContextWindow records window for model; an unknown window (zero or less) records nothing.
func setContextWindow(state *ServerState, model string, window int) {
	if window <= 0 {
		return
	}
	if state.LoadedModelContextLengths == nil {
		state.LoadedModelContextLengths = map[string]int{}
	}
	state.LoadedModelContextLengths[model] = window
}

// commonWindow is the one window every named model has; zero when there are none,
// when any model has no window, or when they differ.
func commonWindow(windows map[string]int, models []string) int {
	common := 0
	for _, m := range models {
		w := windows[m]
		if w <= 0 || (common != 0 && w != common) {
			return 0
		}
		common = w
	}
	return common
}
