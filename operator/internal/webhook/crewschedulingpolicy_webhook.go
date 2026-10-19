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

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-crewschedulingpolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=crewschedulingpolicies,verbs=create;update,versions=v1alpha1,name=vcrewschedulingpolicy.kubemoot.ai,admissionReviewVersions=v1

// CrewSchedulingPolicyValidator validates CrewSchedulingPolicy resources. The
// expressions a rule carries are the Kubernetes NodeSelectorRequirement type, whose
// operator and value rules a CRD schema cannot express within its cost budget, so
// they are checked here: an operator that is not known, Gt or Lt without exactly one
// integer value, In or NotIn without values, Exists or DoesNotExist with values.
type CrewSchedulingPolicyValidator struct{}

var _ admission.Validator[*aiv1alpha1.CrewSchedulingPolicy] = &CrewSchedulingPolicyValidator{}

func (v *CrewSchedulingPolicyValidator) ValidateCreate(_ context.Context, p *aiv1alpha1.CrewSchedulingPolicy) (admission.Warnings, error) {
	return validateCrewSchedulingPolicy(p)
}

func (v *CrewSchedulingPolicyValidator) ValidateUpdate(_ context.Context, _, p *aiv1alpha1.CrewSchedulingPolicy) (admission.Warnings, error) {
	return validateCrewSchedulingPolicy(p)
}

func (v *CrewSchedulingPolicyValidator) ValidateDelete(_ context.Context, _ *aiv1alpha1.CrewSchedulingPolicy) (admission.Warnings, error) {
	return nil, nil
}

func validateCrewSchedulingPolicy(p *aiv1alpha1.CrewSchedulingPolicy) (admission.Warnings, error) {
	for i := range p.Spec.Rules {
		rule := &p.Spec.Rules[i]
		if err := validateRequirements(fmt.Sprintf("spec.rules[%d].requireExpressions", i), rule.RequireExpressions); err != nil {
			return nil, err
		}
		for j, term := range rule.Prefer {
			path := fmt.Sprintf("spec.rules[%d].prefer[%d]", i, j)
			if err := validateRequirements(path+".expressions", term.Expressions); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// validateRequirements checks each requirement in the list, naming its path.
func validateRequirements(path string, reqs []corev1.NodeSelectorRequirement) error {
	for k, r := range reqs {
		if _, err := aiv1alpha1.RequirementOf(r); err != nil {
			return fmt.Errorf("%s[%d]: %w", path, k, err)
		}
	}
	return nil
}
