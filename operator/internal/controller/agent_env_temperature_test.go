/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// An agent that sets no temperature gets no KUBEMOOT_MODEL_TEMPERATURE, so the
// runtime sends none and the model server applies the model's own setting. An
// agent that sets one still gets it.
func TestTemperatureEnvRendering(t *testing.T) {
	const name = "KUBEMOOT_MODEL_TEMPERATURE"
	tools := workspaceTools{}

	unset := agentModelEnvVars(&kubemootv1alpha1.Agent{}, tools)
	if v, ok := envValue(unset, name); ok {
		t.Fatalf("unset temperature must render no %s, got %q", name, v)
	}

	for _, want := range []string{"0.1", "0", "1.5"} {
		agent := &kubemootv1alpha1.Agent{Spec: kubemootv1alpha1.AgentSpec{Temperature: want}}
		got, ok := envValue(agentModelEnvVars(agent, tools), name)
		if !ok || got != want {
			t.Errorf("temperature %q: want %s=%q, got %q present=%v", want, name, want, got, ok)
		}
	}
}
