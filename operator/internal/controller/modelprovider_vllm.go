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
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// reconcileVLLM handles a provider of vLLM servers that someone else runs, listed in
// spec.servers or found behind the Service spec.serviceRef names. The
// operator starts nothing and downloads nothing: the provider is Ready when at
// least one of its servers answers, and each Model on it is Available when its own
// server does. Version, storage, pod and calibration handling are Ollama's and do
// not apply: a vllm provider has no single endpoint or pod.
func (r *ModelProviderReconciler) reconcileVLLM(ctx context.Context, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Each reconcile publishes only what its own probe read.
	r.probes.Delete(provider.Name)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	if done, result, err := r.discoverServers(ctx, provider, httpClient); done {
		return result, err
	}
	servers := provider.VLLMServers()
	if len(servers) == 0 {
		return r.updateStatus(ctx, provider, false, phaseFailed, "a vllm provider needs spec.serviceRef or spec.servers")
	}

	snap, err := r.probeSnapshot(ctx, provider, httpClient)
	if err != nil {
		log.Error(err, "No vLLM server answered", "provider", provider.Name)
		return r.updateStatus(ctx, provider, false, "Failed", fmt.Sprintf("No vLLM server answered: %v", err))
	}

	provider.Status.ProviderInfo = &aiv1alpha1.ProviderInfo{LastChecked: time.Now().UTC().Format(time.RFC3339)}
	if r.ConfigCache != nil && r.ConfigCache.IsSchedulerEnabled() {
		r.countAssignedAgents(ctx, provider)
	}

	message := fmt.Sprintf("%d of %d vLLM servers answering", len(snap.Available), len(servers))
	return r.updateStatus(ctx, provider, true, phaseReady, message)
}

// discoverServers finds the servers of a provider with a serviceRef and records them
// in its status. It reports done, with the status already written, when the Service
// cannot be read or no port reports a model yet; a provider in servers mode is never
// done here.
func (r *ModelProviderReconciler) discoverServers(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) (bool, ctrl.Result, error) {
	if provider.Spec.ServiceRef == nil {
		return false, ctrl.Result{}, nil
	}
	servers, skipped, err := discoverVLLMServers(ctx, r.Client, httpClient, provider)
	if err != nil {
		// What was found before stays in the status: a Service that cannot be read right
		// now is not a Service whose servers are gone.
		result, err := r.updateStatus(ctx, provider, false, phaseFailed, err.Error())
		return true, result, err
	}
	provider.Status.VLLMServers = servers
	if len(servers) == 0 {
		msg := fmt.Sprintf("No port of Service %s reports a model yet (the first start downloads and compiles each model): %v", serviceKey(provider), skipped)
		result, err := r.updateStatus(ctx, provider, false, statePending, msg)
		return true, result, err
	}
	return false, ctrl.Result{}, nil
}
