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

package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	pathShow = "/api/show"
	keyModel = "model"
)

// The capability strings /api/show lists in "capabilities" (Ollama's
// types/model/capability.go).
const (
	capCompletion = "completion"
	capTools      = "tools"
	capThinking   = "thinking"
	capVision     = "vision"
	capEmbedding  = "embedding"
)

// ShowInfo is what /api/show says about one model, reduced to the facts the
// catalog uses.
type ShowInfo struct {
	// ParameterCount is general.parameter_count from model_info, 0 when absent.
	ParameterCount int64
	// ParameterSize is details.parameter_size, such as "8.2B".
	ParameterSize string
	// Family is details.family.
	Family string
	// ContextLength is <architecture>.context_length from model_info, 0 when absent.
	ContextLength int
	// EmbeddingLength is <architecture>.embedding_length from model_info: the
	// vector size of an embedding model, 0 when absent.
	EmbeddingLength int32
	// Capabilities are the abilities in Kubemoot's names (tool-calling,
	// reasoning, vision), sorted as the engine lists them.
	Capabilities []string
	// Embedding is true for a model that only embeds: it lists "embedding" and
	// not "completion". An Ollama that lists no capabilities cannot say, so such a
	// model reports false.
	Embedding bool
}

// showResponse is the part of the /api/show answer this package reads.
type showResponse struct {
	Details struct {
		Family        string `json:"family"`
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
	ModelInfo    map[string]any `json:"model_info"`
	Capabilities []string       `json:"capabilities"`
}

// Show asks the Ollama server at endpoint about one model with POST /api/show.
func Show(ctx context.Context, client *http.Client, endpoint, model string) (ShowInfo, error) {
	raw, err := json.Marshal(map[string]any{keyModel: model})
	if err != nil {
		return ShowInfo{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+pathShow, bytes.NewReader(raw))
	if err != nil {
		return ShowInfo{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ShowInfo{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ShowInfo{}, &StatusError{Code: resp.StatusCode}
	}
	var out showResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ShowInfo{}, &DecodeError{URL: endpoint + pathShow, Err: err}
	}
	return showInfoOf(out), nil
}

// showInfoOf reduces an /api/show answer to a ShowInfo.
func showInfoOf(r showResponse) ShowInfo {
	info := ShowInfo{
		ParameterSize:  r.Details.ParameterSize,
		Family:         r.Details.Family,
		ParameterCount: int64Of(r.ModelInfo["general.parameter_count"]),
	}
	if arch, ok := r.ModelInfo["general.architecture"].(string); ok && arch != "" {
		info.ContextLength = int(int64Of(r.ModelInfo[arch+".context_length"]))
		info.EmbeddingLength = int32(max(0, min(int64Of(r.ModelInfo[arch+".embedding_length"]), math.MaxInt32)))
	}
	info.Capabilities, info.Embedding = abilitiesOf(r.Capabilities)
	return info
}

// int64Of reads a JSON number (decoded as float64) as an integer; 0 for anything else.
func int64Of(v any) int64 {
	f, ok := v.(float64)
	if !ok || f < 0 {
		return 0
	}
	return int64(f)
}

// abilitiesOf maps Ollama's capability strings to Kubemoot's ability names and
// tells whether the model only embeds.
func abilitiesOf(caps []string) (abilities []string, embeddingOnly bool) {
	if len(caps) == 0 {
		return nil, false
	}
	var completion, embedding bool
	for _, c := range caps {
		switch c {
		case capCompletion:
			completion = true
		case capEmbedding:
			embedding = true
		case capTools:
			abilities = append(abilities, aiv1alpha1.AbilityToolCalling)
		case capThinking:
			abilities = append(abilities, aiv1alpha1.AbilityReasoning)
		case capVision:
			abilities = append(abilities, aiv1alpha1.AbilityVision)
		}
	}
	return abilities, embedding && !completion
}
