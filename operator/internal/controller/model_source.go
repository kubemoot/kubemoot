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
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// conditionTypeSourceCompatible is the Model condition that says whether
	// spec.source fits the engine of the Model's provider.
	conditionTypeSourceCompatible = "SourceCompatible"
	reasonSourceEngineMismatch    = "SourceEngineMismatch"
	reasonSourceFitsEngine        = "SourceFitsEngine"
)

// sourceMismatch describes why the Model's source does not fit a provider of the
// given type, or returns "" when it fits. An absent source is an Ollama tag, so
// it fits an ollama provider. A huggingFace source on a type that is not yet
// supported is left to the provider-type check, which reports that type.
func sourceMismatch(model *aiv1alpha1.Model, providerType aiv1alpha1.ProviderType) string {
	src := model.Spec.Source
	if src == nil {
		return ""
	}
	if src.HuggingFace != nil && providerType == aiv1alpha1.ProviderTypeOllama {
		return "source.huggingFace does not fit an ollama provider; use source.ollama or omit source"
	}
	if src.Ollama != nil && providerType != aiv1alpha1.ProviderTypeOllama {
		return fmt.Sprintf("source.ollama does not fit a %s provider; use the source for that engine", providerType)
	}
	return ""
}

// setSourceCondition records on the Model whether its source fits its provider.
func setSourceCondition(model *aiv1alpha1.Model, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
		Type:               conditionTypeSourceCompatible,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: model.Generation,
	})
}
