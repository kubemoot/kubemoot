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
	"regexp"

	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-crew,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=crews,verbs=create;update,versions=v1alpha1,name=vcrew.kubemoot.ai,admissionReviewVersions=v1

// CrewValidator validates Crew resources.
type CrewValidator struct{}

var _ admission.Validator[*aiv1alpha1.Crew] = &CrewValidator{}

var dns1123Regex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func (v *CrewValidator) ValidateCreate(ctx context.Context, crew *aiv1alpha1.Crew) (admission.Warnings, error) {
	return validateCrew(crew)
}

func (v *CrewValidator) ValidateUpdate(ctx context.Context, _, crew *aiv1alpha1.Crew) (admission.Warnings, error) {
	return validateCrew(crew)
}

func (v *CrewValidator) ValidateDelete(ctx context.Context, _ *aiv1alpha1.Crew) (admission.Warnings, error) {
	return nil, nil
}

func validateCrew(crew *aiv1alpha1.Crew) (admission.Warnings, error) {
	name := crew.Name

	if len(name) > 58 {
		return nil, fmt.Errorf("crew name %q exceeds 58 characters (operator adds crew- prefix, total must be under 63)", name)
	}

	if !dns1123Regex.MatchString(name) {
		return nil, fmt.Errorf("crew name %q is not DNS-1123 compliant: must consist of lowercase alphanumeric characters or '-', and must start and end with an alphanumeric character", name)
	}

	var warnings admission.Warnings
	if crew.Spec.Discussion != nil && crew.Spec.Discussion.IsEnabled() {
		hasCoordinator := false
		// Check labels for coordinator hint — but since we can't query agents here,
		// just warn about the common pitfall
		_ = hasCoordinator
		warnings = append(warnings, "discussion is enabled — ensure a coordinator agent exists for this crew, otherwise discussions may fail")
	}

	return warnings, nil
}
