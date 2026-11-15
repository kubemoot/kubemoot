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
	"context"
	"fmt"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// keywordCrew finds the crew that declares the keyword. No keyword or crew is hardcoded.
func keywordCrew(ctx context.Context, c client.Client, keyword string) (*kubemootv1alpha1.Crew, error) {
	var crews kubemootv1alpha1.CrewList
	if err := c.List(ctx, &crews, client.MatchingLabels{keywordCrewLabel: keyword}); err != nil {
		return nil, fmt.Errorf("list crews for keyword %q: %w", keyword, err)
	}
	if len(crews.Items) == 0 {
		return nil, fmt.Errorf("no crew declares %s=%s", keywordCrewLabel, keyword)
	}
	return &crews.Items[0], nil
}

// judgeCrewWindow is the context window, in tokens, a comparison document has to fit:
// the smallest window the model servers declare for any model an agent of the judge
// crew may run. Zero when none declares one, which leaves the document uncut.
func judgeCrewWindow(ctx context.Context, c client.Client, crew *kubemootv1alpha1.Crew) int {
	agents := &kubemootv1alpha1.AgentList{}
	if err := c.List(ctx, agents, client.InNamespace(crew.Namespace), client.MatchingLabels{labelCrew: crew.Name}); err != nil {
		logf.FromContext(ctx).Error(err, "deferred judge: listing the judge crew's agents failed", "crew", crew.Name)
		return 0
	}
	reader := &AgentReconciler{Client: c}
	smallest := 0
	for i := range agents.Items {
		smallest = smallerWindow(smallest, agentWindow(ctx, reader, &agents.Items[i]))
	}
	return smallest
}

// smallerWindow is the smaller of two windows, where zero means unknown and never wins.
func smallerWindow(a, b int) int {
	if a <= 0 {
		return max(b, 0)
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

// agentWindow is the smallest declared window among the models the agent's mulling
// rule admits, the same candidates the agent's per-call pick chooses from.
func agentWindow(ctx context.Context, r *AgentReconciler, agent *kubemootv1alpha1.Agent) int {
	_, rule := r.findPolicyAndRule(ctx, agent, phaseMulling)
	src, err := r.modelSource(ctx, agent.Namespace)
	if err != nil {
		logf.FromContext(ctx).Error(err, "deferred judge: listing models failed", "agent", agent.Name)
		return 0
	}
	smallest := 0
	for _, m := range admittedModels(rule, src.Models) {
		smallest = smallerWindow(smallest, servedWindow(ctx, r, agent.Namespace, m))
	}
	return smallest
}

// servedWindow is the context each request to the model gets on its server: the window
// of the loaded model, else the one calibration measured, else the server's own setting,
// and only when the server reports none the catalog's. For Ollama the catalog value is
// the model's trained context, which the server may serve far less of.
func servedWindow(ctx context.Context, r *AgentReconciler, namespace string, m *kubemootv1alpha1.Model) int {
	server, ok := r.serverOf(ctx, namespace, m)
	if !ok {
		return m.Spec.ContextLength
	}
	if w := servedWindowOf(server, m.Spec.Model); w > 0 {
		return w
	}
	return m.Spec.ContextLength
}

// servedWindowOf is what the server's status says each request to model gets, zero when it says nothing.
func servedWindowOf(server *kubemootv1alpha1.ModelServer, model string) int {
	if c := server.Status.Capacity; c != nil {
		if w := loadedContextLengths(c.LoadedModels)[model]; w > 0 {
			return w
		}
	}
	if w := calibratedWindows(server)[model]; w > 0 {
		return w
	}
	if c := server.Status.Capacity; c != nil {
		return c.ContextLength
	}
	return 0
}
