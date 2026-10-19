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
	"fmt"
	"net/http"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

// reconcileVLLMModel reports a Model on a vllm provider. The operator pulls and
// downloads nothing for it: the vLLM server loads its own weights, so the Model is
// Available when its server answers (Loaded while the server is awake, Available
// while it sleeps, since a sleeping server wakes on demand) and Pending when it
// does not.
func (r *ModelReconciler) reconcileVLLMModel(ctx context.Context, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	endpoint := provider.Spec.EndpointFor(model.Spec.Model)
	if endpoint == "" {
		msg := fmt.Sprintf("ModelProvider %s lists no vLLM server for served name %q: add it to spec.vllm.servers", provider.Name, model.Spec.Model)
		return r.updateModelStatus(ctx, model, stateError, false, msg, nil)
	}
	model.Status.Endpoint = endpoint

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	snap, err := r.probeVLLMServer(ctx, httpClient, provider, model.Spec.Model)
	if err != nil {
		msg := fmt.Sprintf("Waiting for the vLLM server at %s to answer: %v", endpoint, err)
		return r.updateModelStatus(ctx, model, statePending, false, msg, nil)
	}

	state := stateAvailable
	if snap.ModelStates[model.Spec.Model] == engine.StateAwake {
		state = stateLoaded
	}
	return r.updateModelStatus(ctx, model, state, true, "The vLLM server answers", vllmModelInfo(model))
}

// probeVLLMServer probes only the server that serves model, not the whole provider.
func (r *ModelReconciler) probeVLLMServer(ctx context.Context, httpClient *http.Client, provider *aiv1alpha1.ModelProvider, model string) (engine.Snapshot, error) {
	adapter, err := DefaultEngines().Adapter(provider.Spec.Type, httpClient)
	if err != nil {
		return engine.Snapshot{}, err
	}
	spec := provider.Spec.DeepCopy()
	spec.VLLM.Servers = []aiv1alpha1.VLLMServer{{Model: model, Endpoint: provider.Spec.EndpointFor(model)}}
	return adapter.Probe(ctx, *spec)
}

// vllmModelInfo is what the Model's own source says about it; nil when it says
// nothing the status carries.
func vllmModelInfo(model *aiv1alpha1.Model) *aiv1alpha1.ModelInfo {
	src := model.Spec.Source
	if src == nil || src.HuggingFace == nil || src.HuggingFace.Quantization == "" {
		return nil
	}
	return &aiv1alpha1.ModelInfo{Quantization: src.HuggingFace.Quantization}
}
