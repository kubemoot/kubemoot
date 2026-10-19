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
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/internal/engine/vllm"
)

const (
	// conditionTypeServedNameFound is the Model condition that says whether the
	// provider's servers report the Model's spec.model as a served name.
	conditionTypeServedNameFound = "ServedNameFound"
	reasonServedNameReported     = "ServedNameReported"
	reasonServedNameNotReported  = "ServedNameNotReported"

	// conditionTypeServedRepoMatches is the Model condition that says whether the
	// server reports loading the repository the Model's huggingFace source names. It
	// is absent when the Model has no such source or the server does not say.
	conditionTypeServedRepoMatches = "ServedRepoMatches"
	reasonServedRepoMatches        = "ServedRepoMatches"
	reasonServedRepoDiffers        = "ServedRepoDiffers"
)

// reconcileVLLMModel reports a Model on a vllm provider. The operator pulls and
// downloads nothing for it: the vLLM server decides its own weights, so the Model
// is Available when its server answers (Loaded while the server is awake, Available
// while it sleeps, since a sleeping server wakes on demand) and Pending when it
// does not. spec.model must equal a served name the provider's servers report; a
// name no server reports is an Error, never guessed at.
func (r *ModelReconciler) reconcileVLLMModel(ctx context.Context, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	servers := provider.VLLMServers()
	if len(servers) == 0 {
		msg := fmt.Sprintf("Waiting for ModelProvider %s to report its vLLM servers", provider.Name)
		return r.updateModelStatus(ctx, model, statePending, false, msg, nil)
	}
	endpoint := provider.EndpointFor(model.Spec.Model)
	if endpoint == "" {
		msg := fmt.Sprintf("ModelProvider %s reports the served names %v, not %q: make spec.model equal the server's served name (--served-model-name)",
			provider.Name, servedNamesOf(servers), model.Spec.Model)
		setServedNameCondition(model, metav1.ConditionFalse, reasonServedNameNotReported, msg)
		return r.updateModelStatus(ctx, model, stateError, false, msg, nil)
	}
	setServedNameCondition(model, metav1.ConditionTrue, reasonServedNameReported, "A server of the provider reports this served name")
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
	checkServedRepo(ctx, httpClient, model, endpoint)

	state := stateAvailable
	if snap.ModelStates[model.Spec.Model] == engine.StateAwake {
		state = stateLoaded
	}
	return r.updateModelStatus(ctx, model, state, true, "The vLLM server answers", vllmModelInfo(model))
}

// servedNamesOf is the served names of the servers, for a message.
func servedNamesOf(servers []aiv1alpha1.VLLMServer) []string {
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i] = s.Model
	}
	return names
}

// setServedNameCondition records on the Model whether its served name is reported.
func setServedNameCondition(model *aiv1alpha1.Model, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&model.Status.Conditions, metav1.Condition{
		Type:               conditionTypeServedNameFound,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: model.Generation,
	})
}

// checkServedRepo compares the repository a huggingFace source names with the one
// the server says it loaded (the "root" of its /v1/models entry). The source is
// information about the server's weights, not a command to it, so a difference is
// reported on a condition and does not fail the Model. A Model with no
// huggingFace source, or a server that does not say where its weights came from,
// has no condition: the revision is never readable from the server.
func checkServedRepo(ctx context.Context, httpClient *http.Client, model *aiv1alpha1.Model, endpoint string) {
	src := model.Spec.Source
	if src == nil || src.HuggingFace == nil {
		meta.RemoveStatusCondition(&model.Status.Conditions, conditionTypeServedRepoMatches)
		return
	}
	root := servedRoot(ctx, httpClient, endpoint, model.Spec.Model)
	if root == "" {
		meta.RemoveStatusCondition(&model.Status.Conditions, conditionTypeServedRepoMatches)
		return
	}
	cond := metav1.Condition{
		Type: conditionTypeServedRepoMatches, Status: metav1.ConditionTrue, Reason: reasonServedRepoMatches,
		Message: "The server reports the repository the source names", ObservedGeneration: model.Generation,
	}
	if !strings.EqualFold(root, src.HuggingFace.Repo) {
		cond.Status, cond.Reason = metav1.ConditionFalse, reasonServedRepoDiffers
		cond.Message = fmt.Sprintf("The server reports %q, the source names %q", root, src.HuggingFace.Repo)
	}
	meta.SetStatusCondition(&model.Status.Conditions, cond)
}

// servedRoot is where the server at endpoint says it loaded model from; empty when
// the server does not answer or does not say.
func servedRoot(ctx context.Context, httpClient *http.Client, endpoint, model string) string {
	served, err := vllm.New(httpClient).ServedModels(ctx, endpoint)
	if err != nil {
		return ""
	}
	for _, m := range served {
		if m.ID == model {
			return m.Root
		}
	}
	return ""
}

// probeVLLMServer probes only the server that serves model, not the whole provider.
func (r *ModelReconciler) probeVLLMServer(ctx context.Context, httpClient *http.Client, provider *aiv1alpha1.ModelProvider, model string) (engine.Snapshot, error) {
	adapter, err := DefaultEngines().Adapter(provider.Spec.Type, httpClient)
	if err != nil {
		return engine.Snapshot{}, err
	}
	spec := provider.EffectiveSpec()
	spec.Servers = []aiv1alpha1.VLLMServer{{Model: model, Endpoint: spec.EndpointFor(model)}}
	return adapter.Probe(ctx, spec)
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
