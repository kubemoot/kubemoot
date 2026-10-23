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

// applyContextWindows publishes the context window of each model the engine
// serves. The engine's own report (snapshot) comes first because it is live; a
// vLLM model the engine did not report falls back to the window calibration
// measured. A window already published from another source (Ollama /api/ps) is
// never replaced. For a vLLM server, when every published window is the same, that window is also
// the server-wide ContextLength, which covers a model the agent has no entry for.
func applyContextWindows(state *ServerState, server *aiv1alpha1.ModelServer, reported map[string]int) {
	for model, window := range reported {
		setContextWindow(state, model, window)
	}
	if server.Spec.Type != aiv1alpha1.ServerTypeVLLM {
		return
	}
	if server.Status.Calibration != nil {
		for _, m := range server.Status.Calibration.Models {
			setContextWindow(state, m.Model, m.ContextLength)
		}
	}
	if state.ContextLength <= 0 {
		state.ContextLength = commonWindow(state.LoadedModelContextLengths)
	}
}

// setContextWindow records window for model unless one is already published or
// the window is unknown (zero or less).
func setContextWindow(state *ServerState, model string, window int) {
	if window <= 0 {
		return
	}
	if _, known := state.LoadedModelContextLengths[model]; known {
		return
	}
	if state.LoadedModelContextLengths == nil {
		state.LoadedModelContextLengths = map[string]int{}
	}
	state.LoadedModelContextLengths[model] = window
}

// commonWindow is the window every model shares; zero when there are none or
// they differ.
func commonWindow(windows map[string]int) int {
	common := 0
	for _, w := range windows {
		if common != 0 && w != common {
			return 0
		}
		common = w
	}
	return common
}
