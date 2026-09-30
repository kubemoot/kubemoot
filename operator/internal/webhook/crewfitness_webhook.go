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

const errExpectedCrewFitness = "expected CrewFitness, got %T"

// +kubebuilder:webhook:path=/validate-kubemoot-ai-v1alpha1-crewfitness,mutating=false,failurePolicy=fail,sideEffects=None,groups=kubemoot.ai,resources=crewfitnesses,verbs=create;update,versions=v1alpha1,name=vcrewfitness.kubemoot.ai,admissionReviewVersions=v1

// CrewFitnessValidator validates CrewFitness resources.
type CrewFitnessValidator struct{}

func (v *CrewFitnessValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	cf, ok := obj.(*aiv1alpha1.CrewFitness)
	if !ok {
		return nil, fmt.Errorf(errExpectedCrewFitness, obj)
	}
	return validateCrewFitness(cf)
}

func (v *CrewFitnessValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	cf, ok := newObj.(*aiv1alpha1.CrewFitness)
	if !ok {
		return nil, fmt.Errorf(errExpectedCrewFitness, newObj)
	}

	oldCF, ok := oldObj.(*aiv1alpha1.CrewFitness)
	if !ok {
		return nil, fmt.Errorf(errExpectedCrewFitness, oldObj)
	}

	// Reject spec changes after completion
	phase := oldCF.Status.Phase
	if phase == aiv1alpha1.CrewFitnessPhasePassed ||
		phase == aiv1alpha1.CrewFitnessPhaseFailed ||
		phase == aiv1alpha1.CrewFitnessPhaseError {
		if cf.Spec.CrewRef != oldCF.Spec.CrewRef ||
			cf.Spec.TestRef != oldCF.Spec.TestRef ||
			cf.Spec.ConfigMapRef != oldCF.Spec.ConfigMapRef ||
			cf.Spec.TestContent != oldCF.Spec.TestContent {
			return nil, fmt.Errorf("spec is immutable after completion (phase=%s)", phase)
		}
	}

	return validateCrewFitness(cf)
}

func (v *CrewFitnessValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func validateCrewFitness(cf *aiv1alpha1.CrewFitness) (admission.Warnings, error) {
	if cf.Spec.CrewRef == "" {
		return nil, fmt.Errorf("spec.crewRef is required")
	}
	if cf.Spec.TestRef == "" {
		return nil, fmt.Errorf("spec.testRef is required")
	}
	if cf.Spec.ConfigMapRef == "" && cf.Spec.TestContent == "" {
		return nil, fmt.Errorf("either spec.configMapRef or spec.testContent is required")
	}
	if cf.Spec.ConfigMapRef != "" && cf.Spec.TestContent != "" {
		return nil, fmt.Errorf("spec.configMapRef and spec.testContent are mutually exclusive")
	}
	if cf.Spec.TTL != nil && cf.Spec.TTL.Duration <= 0 {
		return nil, fmt.Errorf("spec.ttl must be positive")
	}
	return nil, nil
}
