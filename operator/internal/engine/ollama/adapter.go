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

// Package ollama is the engine adapter for an Ollama server: it reads the loaded
// models from /api/ps and the downloaded ones from /api/tags, loads a model with
// a prompt-less /api/generate, and releases it with keep_alive 0.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	pathPS       = "/api/ps"
	pathTags     = "/api/tags"
	pathGenerate = "/api/generate"
)

// TagsResponse is Ollama's /api/tags answer.
type TagsResponse struct {
	Models []ModelInfo `json:"models"`
}

// ModelInfo is one downloaded model in /api/tags.
type ModelInfo struct {
	Name       string       `json:"name"`
	ModifiedAt string       `json:"modified_at"`
	Size       int64        `json:"size"`
	Digest     string       `json:"digest"`
	Details    ModelDetails `json:"details"`
}

// ModelDetails is the format and size detail of a downloaded model.
type ModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// RunningModelsResponse is Ollama's /api/ps answer.
type RunningModelsResponse struct {
	Models []RunningModel `json:"models"`
}

// RunningModel is one loaded model in /api/ps.
type RunningModel struct {
	Name          string `json:"name"`
	SizeVRAM      int64  `json:"size_vram"`
	Size          int64  `json:"size"`
	ContextLength int    `json:"context_length"`
}

// Adapter talks to one Ollama server over HTTP.
type Adapter struct {
	client *http.Client
}

var _ engine.Adapter = (*Adapter)(nil)

// New returns an Adapter that calls Ollama with client.
func New(client *http.Client) *Adapter {
	return &Adapter{client: client}
}

// Factory is the engine.Factory for Ollama.
func Factory(client *http.Client) engine.Adapter {
	return New(client)
}

// Probe reads /api/ps and /api/tags. Each is best effort: the snapshot marks
// which of the two answered, and Probe fails only when neither did. Ollama
// exports no request counts or cache use, so those stay unknown, and the agents'
// tickets keep the live counts.
func (a *Adapter) Probe(ctx context.Context, spec aiv1alpha1.ModelProviderSpec) (engine.Snapshot, error) {
	endpoint := spec.Endpoint
	var snap engine.Snapshot

	ps, psErr := ListRunning(ctx, a.client, endpoint)
	if psErr == nil {
		snap.LoadedKnown = true
		snap.LoadedSizesKnown = true
		snap.Loaded = loadedModels(ps)
	}
	tags, tagsErr := ListTags(ctx, a.client, endpoint)
	if tagsErr == nil {
		snap.AvailableKnown = true
		snap.Available = availableModels(tags)
	}
	if psErr != nil && tagsErr != nil {
		return engine.Snapshot{}, errors.Join(psErr, tagsErr)
	}
	snap.ModelStates = modelStates(ps, tags, psErr == nil)
	return snap, nil
}

// Ready loads model with the provider's default keep-alive, the call a first
// request would make.
func (a *Adapter) Ready(ctx context.Context, spec aiv1alpha1.ModelProviderSpec, model string) error {
	return a.generate(ctx, spec.Endpoint, map[string]any{keyModel: model})
}

// Release unloads model at once with keep_alive 0.
func (a *Adapter) Release(ctx context.Context, spec aiv1alpha1.ModelProviderSpec, model string) error {
	return a.generate(ctx, spec.Endpoint, map[string]any{keyModel: model, "keep_alive": 0})
}

// generate posts a prompt-less /api/generate request, which loads or unloads a model.
func (a *Adapter) generate(ctx context.Context, endpoint string, body map[string]any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+pathGenerate, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ollama %s returned status %d", pathGenerate, resp.StatusCode)
	}
	return nil
}

// StatusError is an Ollama answer with a status other than 200.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("Ollama returned status %d", e.Code) }

// DecodeError is an Ollama answer that is not the JSON the endpoint documents.
type DecodeError struct {
	URL string
	Err error
}

func (e *DecodeError) Error() string { return fmt.Sprintf("decode %s: %v", e.URL, e.Err) }
func (e *DecodeError) Unwrap() error { return e.Err }

// ListTags lists the models downloaded on the Ollama server at endpoint.
func ListTags(ctx context.Context, client *http.Client, endpoint string) ([]ModelInfo, error) {
	tags, err := getJSON[TagsResponse](ctx, client, endpoint+pathTags)
	return tags.Models, err
}

// ListRunning lists the models loaded on the Ollama server at endpoint.
func ListRunning(ctx context.Context, client *http.Client, endpoint string) ([]RunningModel, error) {
	ps, err := getJSON[RunningModelsResponse](ctx, client, endpoint+pathPS)
	return ps.Models, err
}

// getJSON GETs url and decodes a 200 answer into T.
func getJSON[T any](ctx context.Context, client *http.Client, url string) (T, error) {
	var out T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, &StatusError{Code: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, &DecodeError{URL: url, Err: err}
	}
	return out, nil
}

// loadedModels maps /api/ps models to the operator's loaded-model record. The
// result is nil when nothing is loaded; Snapshot.LoadedKnown tells that from no answer.
func loadedModels(ps []RunningModel) []aiv1alpha1.LoadedModel {
	if len(ps) == 0 {
		return nil
	}
	loaded := make([]aiv1alpha1.LoadedModel, 0, len(ps))
	for _, m := range ps {
		loaded = append(loaded, aiv1alpha1.LoadedModel{
			Name:          m.Name,
			SizeVRAM:      m.SizeVRAM,
			Size:          m.Size,
			ContextLength: m.ContextLength,
		})
	}
	return loaded
}

// availableModels maps /api/tags models to the operator's available-model record.
func availableModels(tags []ModelInfo) []aiv1alpha1.AvailableModel {
	if len(tags) == 0 {
		return nil
	}
	available := make([]aiv1alpha1.AvailableModel, 0, len(tags))
	for _, m := range tags {
		available = append(available, aiv1alpha1.AvailableModel{Name: m.Name, SizeBytes: m.Size})
	}
	return available
}

// modelStates marks every loaded model awake and every other downloaded model
// available. Without /api/ps the loaded ones cannot be told apart, so it returns
// nil and the states stay unknown.
func modelStates(ps []RunningModel, tags []ModelInfo, psKnown bool) map[string]engine.ModelState {
	if !psKnown {
		return nil
	}
	states := make(map[string]engine.ModelState, len(ps)+len(tags))
	for _, m := range tags {
		states[m.Name] = engine.StateAvailable
	}
	for _, m := range ps {
		states[m.Name] = engine.StateAwake
	}
	return states
}
