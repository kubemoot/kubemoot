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

package v1alpha1

import (
	"math"
	"strconv"
	"strings"
)

// The model catalog is the set of models a cluster offers, merged from what its
// model servers report and the Model objects its owner keeps in the operator's
// namespace. Each entry carries labels that scheduling rules select on. These
// are the label keys and values the catalog produces.
const (
	// LabelSize is the size class of a model, derived from its total parameter
	// count. A Model that carries this label overrides the derived class.
	LabelSize = "kubemoot.ai/size"
	// LabelContextTokens is the context window in tokens, as an integer string.
	LabelContextTokens = "kubemoot.ai/context-tokens"
	// LabelFamily is the model family the engine reports (for example qwen3).
	LabelFamily = "kubemoot.ai/family"
	// LabelEngine is the type of the server that serves the model.
	LabelEngine = "kubemoot.ai/engine"
	// CapabilityLabelPrefix starts the label of an ability: capability/tool-calling.
	CapabilityLabelPrefix = "capability/"
)

// The size classes.
const (
	SizeSmall  = "small"
	SizeMedium = "medium"
	SizeLarge  = "large"
)

// Size class boundaries in total parameters: small is below SizeMediumFrom,
// medium is from SizeMediumFrom up to but not including SizeLargeFrom, large is
// SizeLargeFrom and above. They are constants so that a class means the same on
// every cluster.
const (
	SizeMediumFrom int64 = 12_000_000_000
	SizeLargeFrom  int64 = 30_000_000_000
)

// The abilities a model can have, as the labels and Model.spec.capabilities
// name them.
const (
	AbilityToolCalling = "tool-calling"
	AbilityReasoning   = "reasoning"
	AbilityVision      = "vision"
)

// Where a catalog entry's facts came from (CatalogModel.Source).
const (
	CatalogSourceModel      = "model"
	CatalogSourceOllamaShow = "ollama-show"
	CatalogSourceOllamaTags = "ollama-tags"
	CatalogSourceService    = "service"
	CatalogSourceNone       = "none"
)

// SizeClassFor is the size class of a model with the given total parameter
// count, or "" when the count is unknown (zero or negative).
func SizeClassFor(parameters int64) string {
	switch {
	case parameters <= 0:
		return ""
	case parameters < SizeMediumFrom:
		return SizeSmall
	case parameters < SizeLargeFrom:
		return SizeMedium
	}
	return SizeLarge
}

// parameterUnits are the suffixes of a declared or reported parameter count.
var parameterUnits = map[byte]float64{'K': 1e3, 'M': 1e6, 'B': 1e9, 'T': 1e12}

// ParseParameterCount reads a parameter count such as "14B", "0.5B", "751.63M"
// or "8.2B" (the form Model.spec.parameters and Ollama's details.parameter_size
// use). The unit letter is case-insensitive. It reports false for an empty or
// malformed value.
func ParseParameterCount(s string) (int64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 2 {
		return 0, false
	}
	unit, ok := parameterUnits[s[len(s)-1]]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || n <= 0 || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return int64(math.Round(n * unit)), true
}

// CatalogModel is one model in a cluster's catalog, as a ModelServer reports
// it: what the server's engine said about the model merged with the Model
// object the cluster owner keeps for it, if any. Labels is what scheduling
// rules select on.
type CatalogModel struct {
	// Name is the model name on the server: the Ollama tag, or the vLLM served
	// name.
	Name string `json:"name"`

	// Present is true when the server has the model now: downloaded (Ollama)
	// or answering (vLLM). An entry that is not present is known from a Model
	// object only.
	// +optional
	Present bool `json:"present,omitempty"`

	// Source says where the parameter count came from: model (a Model's
	// spec.parameters), ollama-show, ollama-tags, service (the kubemoot-vllm
	// chart's annotation), or none.
	// +optional
	Source string `json:"source,omitempty"`

	// ParameterCount is the total parameter count, 0 when unknown.
	// +optional
	ParameterCount int64 `json:"parameterCount,omitempty"`

	// Size is the size class (small, medium, large), empty when the size is
	// unknown.
	// +optional
	Size string `json:"size,omitempty"`

	// ContextLength is the context window in tokens, 0 when unknown.
	// +optional
	ContextLength int `json:"contextLength,omitempty"`

	// Capabilities are the model's abilities (tool-calling, reasoning, vision).
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// VRAMMib is the accelerator memory in MiB the Model object declares the model
	// needs, 0 when none does.
	// +optional
	VRAMMib int32 `json:"vramMib,omitempty"`

	// ModelRef names the Model object that contributed to the entry, as
	// namespace/name.
	// +optional
	ModelRef string `json:"modelRef,omitempty"`

	// Labels are the effective labels scheduling rules match: the size, context,
	// family, engine and capability labels plus every label on the Model object.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}
