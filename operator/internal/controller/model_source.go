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
// given type, or returns "" when it fits. An ollama provider takes an ollama
// source, or none (an absent source is an Ollama tag). A vllm provider takes a
// huggingFace source and nothing else, because its servers load their own
// weights from a pinned repository. A source on a type that is not supported is
// left to the provider-type check, which reports that type.
func sourceMismatch(model *aiv1alpha1.Model, providerType aiv1alpha1.ProviderType) string {
	src := model.Spec.Source
	switch providerType {
	case aiv1alpha1.ProviderTypeOllama:
		return ollamaSourceMismatch(src)
	case aiv1alpha1.ProviderTypeVLLM:
		return vllmSourceMismatch(src)
	default:
		if src != nil && src.Ollama != nil {
			return fmt.Sprintf("source.ollama does not fit a %s provider; use the source for that engine", providerType)
		}
		return ""
	}
}

// ollamaSourceMismatch is the mismatch for an ollama provider: only a huggingFace source.
func ollamaSourceMismatch(src *aiv1alpha1.ModelSource) string {
	if src != nil && src.HuggingFace != nil {
		return "source.huggingFace does not fit an ollama provider; use source.ollama or omit source"
	}
	return ""
}

// vllmSourceMismatch is the mismatch for a vllm provider: an ollama source. A
// vLLM server decides its own weights, so a vllm Model needs no source; a
// huggingFace one is information about what the server runs.
func vllmSourceMismatch(src *aiv1alpha1.ModelSource) string {
	if src != nil && src.Ollama != nil {
		return "source.ollama does not fit a vllm provider; use source.huggingFace or omit source"
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
