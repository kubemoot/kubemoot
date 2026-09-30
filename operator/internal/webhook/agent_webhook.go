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

package webhook

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-agent,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=agents,verbs=create;update,versions=v1alpha1,name=vagent.kubemoot.ai,admissionReviewVersions=v1

// AgentValidator validates Agent resources.
type AgentValidator struct{}

func (v *AgentValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	agent, ok := obj.(*aiv1alpha1.Agent)
	if !ok {
		return nil, fmt.Errorf("expected Agent, got %T", obj)
	}
	return validateAgent(agent)
}

func (v *AgentValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	agent, ok := newObj.(*aiv1alpha1.Agent)
	if !ok {
		return nil, fmt.Errorf("expected Agent, got %T", newObj)
	}
	return validateAgent(agent)
}

func (v *AgentValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateAgent(agent *aiv1alpha1.Agent) (admission.Warnings, error) {
	// Scheduler v2: agents declare spec.capabilities (open string set) rather
	// than specific model bindings. No validation on capabilities yet — they
	// are validated indirectly by CrewSchedulingPolicy require/prefer rules
	// at scheduling time. Returning no-op preserves the webhook surface for
	// future v2-era validation without rejecting any current Agent shapes.
	_ = agent
	return nil, nil
}
