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

// reconcileVLLM handles a server of vLLM servers that someone else runs, listed in
// spec.servers or found behind the Service spec.serviceRef names. The
// operator starts nothing and downloads nothing: the server is Ready when at
// least one of its servers answers, and each Model on it is Available when its own
// server does. Version, storage and pod handling are Ollama's and do
// not apply: a vllm server has no single endpoint or pod.
func (r *ModelServerReconciler) reconcileVLLM(ctx context.Context, server *aiv1alpha1.ModelServer) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Each reconcile publishes only what its own probe read.
	r.probes.Delete(server.Name)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	if done, result, err := r.discoverServers(ctx, server, httpClient); done {
		return result, err
	}
	servers := server.VLLMServers()
	if len(servers) == 0 {
		return r.updateStatus(ctx, server, false, phaseFailed, "a vllm server needs spec.serviceRef or spec.servers")
	}

	snap, err := r.probeSnapshot(ctx, server, httpClient)
	if err != nil {
		log.Error(err, "No vLLM server answered", "server", server.Name)
		return r.updateStatus(ctx, server, false, "Failed", fmt.Sprintf("No vLLM server answered: %v", err))
	}

	server.Status.ServerInfo = &aiv1alpha1.ServerInfo{LastChecked: time.Now().UTC().Format(time.RFC3339)}
	if r.ConfigCache != nil && r.ConfigCache.IsSchedulerEnabled() {
		r.countAssignedAgents(ctx, server)
	}

	r.publishCatalog(ctx, server, r.vllmFacts(ctx, httpClient, server, snap))

	// A vLLM server is calibrated like an Ollama one: accelerator-mcp measures each bound
	// model through its server, waking a sleeping one through the operator's lifecycle path.
	r.requestCalibration(ctx, server)

	message := fmt.Sprintf("%d of %d vLLM servers answering", len(snap.Available), len(servers))
	return r.updateStatus(ctx, server, true, phaseReady, message)
}

// discoverServers finds the vLLM servers of a ModelServer with a serviceRef and records them
// in its status. It reports done, with the status already written, when the Service
// cannot be read or no port reports a model yet; a server in servers mode is never
// done here.
func (r *ModelServerReconciler) discoverServers(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) (bool, ctrl.Result, error) {
	if server.Spec.ServiceRef == nil {
		return false, ctrl.Result{}, nil
	}
	servers, skipped, err := discoverVLLMServers(ctx, r.Client, httpClient, server)
	if err != nil {
		// What was found before stays in the status: a Service that cannot be read right
		// now is not a Service whose servers are gone.
		result, err := r.updateStatus(ctx, server, false, phaseFailed, err.Error())
		return true, result, err
	}
	server.Status.VLLMServers = servers
	if len(servers) == 0 {
		msg := fmt.Sprintf("No port of Service %s reports a model yet (the first start downloads and compiles each model): %v", serviceKey(server), skipped)
		result, err := r.updateStatus(ctx, server, false, statePending, msg)
		return true, result, err
	}
	return false, ctrl.Result{}, nil
}
