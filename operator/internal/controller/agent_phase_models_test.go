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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// phaseModelsReconciler serves one Ready Model on a Ready provider in namespace
// "ns", plus any extra objects.
func phaseModelsReconciler(t *testing.T, extra ...client.Object) *AgentReconciler {
	t.Helper()
	scheme := agentReconcileScheme(t)
	model := candModel("only", candModel14, true, nil)
	prov := &kubemootv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: candProvider, Namespace: "ns"},
		Spec:       kubemootv1alpha1.ModelProviderSpec{Endpoint: testOllamaURL},
		Status:     kubemootv1alpha1.ModelProviderStatus{Ready: true},
	}
	objs := append([]client.Object{&model, prov}, extra...)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
}

func phaseModelsAgent() *kubemootv1alpha1.Agent {
	return &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: candAgent, Namespace: "ns", Labels: map[string]string{labelCrew: candCrew}},
	}
}

// A triage rule no Model satisfies leaves triage unschedulable; the agent then
// runs triage on its mulling pick instead of failing.
func TestPickPhaseModels_TriageFallsBackToMulling(t *testing.T) {
	policy := &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "phase-pol", Namespace: "ns"},
		Spec: kubemootv1alpha1.CrewSchedulingPolicySpec{
			CrewRef: candCrew,
			Rules: []kubemootv1alpha1.SchedulingRule{
				{Phase: phaseMulling},
				{Phase: phaseTriage, Require: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "absent"}}},
			},
		},
	}
	r := phaseModelsReconciler(t, policy)

	mulling, triage, err := r.pickPhaseModels(context.Background(), phaseModelsAgent())
	if err != nil {
		t.Fatalf("mulling pick error: %v", err)
	}
	if mulling == nil || mulling.ModelID != candModel14 {
		t.Fatalf("mulling pick = %+v, want %s", mulling, candModel14)
	}
	if triage != mulling {
		t.Errorf("triage pick = %+v, want the mulling pick", triage)
	}
}

// With no schedulable Model the mulling pick is nil and carries the reason.
func TestPickPhaseModels_NoModelReturnsError(t *testing.T) {
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}

	mulling, triage, err := r.pickPhaseModels(context.Background(), phaseModelsAgent())
	if mulling != nil || triage != nil {
		t.Errorf("picks = %+v, %+v, want nil", mulling, triage)
	}
	if err == nil {
		t.Error("expected the mulling error")
	}
}
