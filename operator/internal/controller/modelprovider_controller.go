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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
)

// ProviderStateBucket is the NATS KV bucket where per-provider live capacity
// state is published for agent-runtime ProviderSelector to read on each
// inference call. See tasks/notes/Epic - JIT GPU Scheduling.md for the
// architectural intent.
const ProviderStateBucket = "kubemoot_provider_state"

// ProviderState is the snapshot the operator publishes to NATS KV for each
// ModelProvider. Agents read it via ProviderSelector at inference time to
// pick the freest provider — JIT scheduling, emergent bin-packing.
//
// Fields mirror the subset of ModelProvider.Status.Capacity that the agent
// runtime needs to make a per-call provider decision. Kept narrow so the
// per-call read is small and fast.
type ProviderState struct {
	Name         string   `json:"name"`
	Endpoint     string   `json:"endpoint"`
	MaxParallel  int      `json:"maxParallel"`
	ActiveCount  int      `json:"activeCount"`
	QueueDepth   int      `json:"queueDepth"`
	LoadedModels []string `json:"loadedModels,omitempty"`
	Ready        bool     `json:"ready"`
	LastProbedAt string   `json:"lastProbedAt"`
	// v2 (VRAM-headroom JIT scheduling) fields — see kubemoot/docs/scheduler.md
	// "JIT GPU Scheduling → Update (2026-05-28): VRAM-headroom tickets".
	// TotalVramMiB is the GPU's total VRAM (discovered from DCGM via Prometheus).
	// LoadedModelFootprintsMiB maps loaded model name → MiB it currently occupies
	// (from Ollama /api/ps size_vram). Agents subtract this from total to get the
	// budget available for per-call ticket claims (TicketManager).
	TotalVramMiB             int64            `json:"totalVramMiB,omitempty"`
	LoadedModelFootprintsMiB map[string]int64 `json:"loadedModelFootprintsMiB,omitempty"`
	// AvailableModelFootprintsMiB maps downloaded-but-not-loaded model name →
	// on-disk size in MiB (from Ollama /api/tags .size, bytes / 1024^2).
	// Used as a cold-load footprint proxy at true cold start, when the model
	// has never been loaded and LoadedModelFootprintsMiB has no entry for it.
	// The on-disk size is a reasonable upper bound for the VRAM footprint;
	// the actual VRAM usage (from /api/ps) replaces this once the model loads.
	// Allows the JIT fit-gate to reject an oversized model BEFORE loading it.
	AvailableModelFootprintsMiB map[string]int64 `json:"availableModelFootprintsMiB,omitempty"`
	// ContextLength is the engine's configured per-request context window, in
	// tokens, which a request to a model that is not loaded yet can expect. Zero
	// when the engine chooses its own default (Ollama picks one by VRAM size and
	// caps it at each model's trained context), because that value is only known
	// per model once it loads. Agents skip a provider whose context cannot hold a
	// call's prompt, because the engine cuts an oversized prompt without error.
	ContextLength int `json:"contextLength,omitempty"`
	// LoadedModelContextLengths maps loaded model name to the context window, in
	// tokens, each request to it gets (from Ollama /api/ps context_length).
	LoadedModelContextLengths map[string]int `json:"loadedModelContextLengths,omitempty"`
}

// ModelProviderReconciler reconciles a ModelProvider object
type ModelProviderReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	HTTPClient    *http.Client
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher
}

// OllamaVersionResponse represents the response from Ollama /api/version
type OllamaVersionResponse struct {
	Version string `json:"version"`
}

// prometheusQueryResponse represents a Prometheus instant query response
type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"` // [timestamp, "value"]
		} `json:"result"`
	} `json:"data"`
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// (deployments list/get is needed by countAssignedAgents to count agents
// per provider via the mulling/triage provider labels on agent deployments;
// chart RBAC already grants these on the operator's ClusterRole.)

// Reconcile is part of the main kubernetes reconciliation loop
func (r *ModelProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ModelProvider instance
	provider := &aiv1alpha1.ModelProvider{}
	if err := r.Get(ctx, req.NamespacedName, provider); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling ModelProvider", "name", provider.Name, "type", provider.Spec.Type)

	// Update status based on provider type
	switch provider.Spec.Type {
	case aiv1alpha1.ProviderTypeOllama:
		return r.reconcileOllama(ctx, provider)
	default:
		log.Info("Unsupported provider type", "type", provider.Spec.Type)
		return r.updateStatus(ctx, provider, false, "Unsupported", aiv1alpha1.UnsupportedProviderTypeMessage)
	}
}

// reconcileOllama handles Ollama provider reconciliation
func (r *ModelProviderReconciler) reconcileOllama(ctx context.Context, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if provider.Spec.Endpoint == "" {
		return r.updateStatus(ctx, provider, false, "Failed", "Endpoint is required for Ollama provider")
	}

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	// Check Ollama connectivity by calling /api/version
	versionURL := fmt.Sprintf("%s/api/version", provider.Spec.Endpoint)
	resp, err := httpClient.Get(versionURL)
	if err != nil {
		log.Error(err, "Failed to connect to Ollama", "endpoint", provider.Spec.Endpoint)
		return r.updateStatus(ctx, provider, false, "Failed", fmt.Sprintf("Failed to connect to Ollama: %v", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return r.updateStatus(ctx, provider, false, "Failed", fmt.Sprintf("Ollama returned status %d", resp.StatusCode))
	}

	var versionResp OllamaVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&versionResp); err != nil {
		log.Error(err, "Failed to parse Ollama version response")
		return r.updateStatus(ctx, provider, false, "Failed", "Failed to parse Ollama version")
	}

	log.Info("Successfully connected to Ollama", "version", versionResp.Version)

	provider.Status.ProviderInfo = &aiv1alpha1.ProviderInfo{
		Version:     versionResp.Version,
		LastChecked: time.Now().UTC().Format(time.RFC3339),
	}

	// Discover capacity when scheduler is enabled
	if r.ConfigCache != nil && r.ConfigCache.IsSchedulerEnabled() {
		r.discoverCapacity(ctx, provider, httpClient)
	}

	return r.updateStatus(ctx, provider, true, "Ready", "Connected to Ollama")
}

// discoverCapacity probes the Ollama pod and Prometheus for capacity information
func (r *ModelProviderReconciler) discoverCapacity(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) {
	log := logf.FromContext(ctx)

	if provider.Status.Capacity == nil {
		provider.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{}
	}
	now := metav1.Now()
	provider.Status.Capacity.LastProbed = &now

	// 1-4. Inspect the backing Ollama pod (env vars, node, DCGM metrics).
	r.discoverPodCapacity(ctx, provider, httpClient)

	// 5. Call /api/ps for loaded models and VRAM usage
	r.discoverLoadedModels(ctx, provider, httpClient)

	// 5b. Call /api/tags for available (downloaded) models
	r.discoverAvailableModels(ctx, provider, httpClient)

	// 6. Count agents assigned to this provider
	r.countAssignedAgents(ctx, provider)

	log.Info("Discovered provider capacity",
		"provider", provider.Name,
		"maxParallel", provider.Status.Capacity.MaxParallel,
		"vramTotalMiB", provider.Status.Capacity.VRAMTotalMiB,
		"vramUsedMiB", provider.Status.Capacity.VRAMUsedMiB,
		"agentCount", provider.Status.Capacity.AgentCount,
		"nodeName", provider.Status.Capacity.NodeName,
		"loadedModels", len(provider.Status.Capacity.LoadedModels),
		"availableModels", len(provider.Status.Capacity.AvailableModels),
	)
}

// discoverPodCapacity resolves the backing Ollama pod from the provider
// endpoint and reads node name, OLLAMA_NUM_PARALLEL, and DCGM GPU metrics
// into provider.Status.Capacity. Best-effort: missing pieces are logged at
// V(1) and skipped, leaving the corresponding capacity fields untouched.
func (r *ModelProviderReconciler) discoverPodCapacity(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) {
	log := logf.FromContext(ctx)

	// 1. Parse service name/namespace from endpoint URL
	svcName, svcNamespace := parseServiceFromEndpoint(provider.Spec.Endpoint, provider.Namespace)
	if svcName == "" {
		log.V(1).Info("Could not parse service from endpoint", "endpoint", provider.Spec.Endpoint)
		return
	}

	// 2. Find the backing pod via EndpointSlices
	podName, podNamespace := r.findBackingPod(ctx, svcName, svcNamespace)
	if podName == "" {
		return
	}

	// 3. Read pod env vars and node name
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: podNamespace}, pod); err != nil {
		log.V(1).Info("Failed to get Ollama pod", "pod", podName, "error", err)
		return
	}

	provider.Status.Capacity.NodeName = pod.Spec.NodeName
	r.applyEngineEnvFromPod(ctx, pod, provider)

	// 4. Query Prometheus for DCGM metrics if configured
	schedulerConfig := r.ConfigCache.GetSchedulerConfig()
	if schedulerConfig != nil && schedulerConfig.PrometheusEndpoint != "" && pod.Spec.NodeName != "" {
		r.queryDCGMMetrics(ctx, httpClient, schedulerConfig.PrometheusEndpoint, pod.Spec.NodeName, provider)
	}
}

// engineEnvCapacity maps the Ollama pod env vars the scheduler reads to the
// capacity field each one sets.
var engineEnvCapacity = map[string]func(*aiv1alpha1.DiscoveredCapacity, int){
	"OLLAMA_NUM_PARALLEL":   func(c *aiv1alpha1.DiscoveredCapacity, v int) { c.MaxParallel = v },
	"OLLAMA_CONTEXT_LENGTH": func(c *aiv1alpha1.DiscoveredCapacity, v int) { c.ContextLength = v },
}

// applyEngineEnvFromPod scans the pod's container env vars for the engine
// settings in engineEnvCapacity and records each positive integer value.
func (r *ModelProviderReconciler) applyEngineEnvFromPod(ctx context.Context, pod *corev1.Pod, provider *aiv1alpha1.ModelProvider) {
	log := logf.FromContext(ctx)
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			set, known := engineEnvCapacity[env.Name]
			if !known {
				continue
			}
			if val, err := strconv.Atoi(env.Value); err == nil && val > 0 {
				set(provider.Status.Capacity, val)
				log.V(1).Info("Discovered engine setting", "name", env.Name, "value", val)
			}
		}
	}
}

// parseServiceFromEndpoint extracts service name and namespace from an endpoint URL.
// Example: "http://ollama.ollama-rig0:11434" → ("ollama", "ollama")
// Example: "http://ollama.ollama-rig0.svc.cluster.local:11434" → ("ollama", "ollama")
func parseServiceFromEndpoint(endpoint, defaultNamespace string) (string, string) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", ""
	}

	host := parsed.Hostname()
	parts := strings.SplitN(host, ".", 3)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	if len(parts) == 1 {
		return parts[0], defaultNamespace
	}
	return "", ""
}

// findBackingPod resolves a Service to its backing Pod name via EndpointSlices
func (r *ModelProviderReconciler) findBackingPod(ctx context.Context, svcName, svcNamespace string) (string, string) {
	log := logf.FromContext(ctx)

	sliceList := &discoveryv1.EndpointSliceList{}
	if err := r.List(ctx, sliceList,
		client.InNamespace(svcNamespace),
		client.MatchingLabels{"kubernetes.io/service-name": svcName},
	); err != nil {
		log.V(1).Info("Failed to list EndpointSlices", "service", svcName, "error", err)
		return "", ""
	}

	for _, slice := range sliceList.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.TargetRef != nil && endpoint.TargetRef.Kind == "Pod" {
				ns := svcNamespace
				if endpoint.TargetRef.Namespace != "" {
					ns = endpoint.TargetRef.Namespace
				}
				return endpoint.TargetRef.Name, ns
			}
		}
	}

	return "", ""
}

// queryDCGMMetrics queries Prometheus for DCGM GPU metrics.
//
// DCGM exporter labels GPU series with "Hostname" (capitalised), not
// "kubernetes_node". A previous version queried by kubernetes_node and
// returned 0 results, leaving Capacity.VRAMTotalMiB at 0 — which broke
// the v2 VRAM-headroom JIT scheduler (preTicketHeadroomMiB → 0 → every
// per-call selection fell back to the static endpoint). Confirmed by
// inspecting actual DCGM series labels on this cluster (2026-05-28).
//
// DCGM exporter on this cluster does not export DCGM_FI_DEV_FB_TOTAL,
// only FB_FREE, FB_USED, FB_RESERVED. Total = sum of the three, which
// matches per Hostname because the three series share label sets, so
// PromQL element-wise + works without ignoring().
func (r *ModelProviderReconciler) queryDCGMMetrics(ctx context.Context, httpClient *http.Client, prometheusEndpoint, nodeName string, provider *aiv1alpha1.ModelProvider) {
	log := logf.FromContext(ctx)

	// VRAM total = FB_FREE + FB_USED + FB_RESERVED, summed across GPUs
	// on the node (sum() handles the multi-GPU-per-node case gracefully;
	// single-GPU passthrough returns the lone series unchanged).
	vramQuery := fmt.Sprintf(
		`sum(DCGM_FI_DEV_FB_FREE{Hostname="%s"} + DCGM_FI_DEV_FB_USED{Hostname="%s"} + DCGM_FI_DEV_FB_RESERVED{Hostname="%s"})`,
		nodeName, nodeName, nodeName)
	vramTotal := r.queryPrometheusScalar(ctx, httpClient, prometheusEndpoint, vramQuery)
	if vramTotal > 0 {
		provider.Status.Capacity.VRAMTotalMiB = int64(vramTotal)
		log.V(1).Info("Discovered VRAM total from DCGM", "vramTotalMiB", vramTotal)
	}

	// Query GPU model name (same Hostname label fix as above).
	gpuModel := r.queryPrometheusLabel(ctx, httpClient, prometheusEndpoint,
		fmt.Sprintf(`DCGM_FI_DEV_GPU_TEMP{Hostname="%s"}`, nodeName), "modelName")
	if gpuModel != "" {
		provider.Status.Capacity.GPUModel = gpuModel
	}
}

// queryPrometheusScalar queries Prometheus for a single scalar value
func (r *ModelProviderReconciler) queryPrometheusScalar(ctx context.Context, httpClient *http.Client, endpoint, query string) float64 {
	log := logf.FromContext(ctx)

	queryURL := fmt.Sprintf("%s/api/v1/query?query=%s", endpoint, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return 0
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Prometheus query failed", "error", err)
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	var promResp prometheusQueryResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return 0
	}

	if promResp.Status != "success" || len(promResp.Data.Result) == 0 {
		return 0
	}

	// Value is [timestamp, "value_string"]
	if len(promResp.Data.Result[0].Value) >= 2 {
		if valStr, ok := promResp.Data.Result[0].Value[1].(string); ok {
			if val, err := strconv.ParseFloat(valStr, 64); err == nil {
				return val
			}
		}
	}

	return 0
}

// queryPrometheusLabel queries Prometheus and extracts a label value from the first result
func (r *ModelProviderReconciler) queryPrometheusLabel(ctx context.Context, httpClient *http.Client, endpoint, query, labelName string) string {
	log := logf.FromContext(ctx)

	queryURL := fmt.Sprintf("%s/api/v1/query?query=%s", endpoint, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, nil)
	if err != nil {
		return ""
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Prometheus query failed", "error", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	var promResp prometheusQueryResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return ""
	}

	if promResp.Status != "success" || len(promResp.Data.Result) == 0 {
		return ""
	}

	return promResp.Data.Result[0].Metric[labelName]
}

// discoverLoadedModels calls Ollama /api/ps to get loaded models and VRAM usage
func (r *ModelProviderReconciler) discoverLoadedModels(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) {
	log := logf.FromContext(ctx)

	psURL := fmt.Sprintf("%s/api/ps", provider.Spec.Endpoint)
	req, err := http.NewRequestWithContext(ctx, "GET", psURL, nil)
	if err != nil {
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Failed to call /api/ps", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var psResp OllamaRunningModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&psResp); err != nil {
		log.V(1).Info("Failed to parse /api/ps response", "error", err)
		return
	}

	var loadedModels []aiv1alpha1.LoadedModel
	var totalVRAMUsed int64
	for _, m := range psResp.Models {
		loadedModels = append(loadedModels, aiv1alpha1.LoadedModel{
			Name:          m.Name,
			SizeVRAM:      m.SizeVRAM,
			Size:          m.Size,
			ContextLength: m.ContextLength,
		})
		totalVRAMUsed += m.SizeVRAM
	}

	provider.Status.Capacity.LoadedModels = loadedModels
	// Convert bytes to MiB
	provider.Status.Capacity.VRAMUsedMiB = totalVRAMUsed / (1024 * 1024)
}

// discoverAvailableModels calls Ollama /api/tags to get downloaded (available) models
func (r *ModelProviderReconciler) discoverAvailableModels(ctx context.Context, provider *aiv1alpha1.ModelProvider, httpClient *http.Client) {
	log := logf.FromContext(ctx)

	tagsURL := fmt.Sprintf("%s/api/tags", provider.Spec.Endpoint)
	req, err := http.NewRequestWithContext(ctx, "GET", tagsURL, nil)
	if err != nil {
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.V(1).Info("Failed to call /api/tags", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var tagsResp OllamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		log.V(1).Info("Failed to parse /api/tags response", "error", err)
		return
	}

	var available []aiv1alpha1.AvailableModel
	for _, m := range tagsResp.Models {
		available = append(available, aiv1alpha1.AvailableModel{
			Name:      m.Name,
			SizeBytes: m.Size,
		})
	}
	provider.Status.Capacity.AvailableModels = available
}

// countAssignedAgents counts how many agent deployments are currently
// targeting this provider, summing across the mulling and triage phases.
// Each agent deployment carries two labels (`kubemoot.ai/mulling-provider`
// and `kubemoot.ai/triage-provider`) set by the AgentReconciler at deploy
// time; agents that use this provider for both phases contribute 2 to the
// count (they load the provider twice — once per phase model — which is
// what the scheduler's load penalty should see).
//
// When/if Scheduler v2 lands and binding becomes per-discussion on NATS
// subjects rather than per-agent on Deployments, this should switch to
// counting active NATS-published bindings.
func (r *ModelProviderReconciler) countAssignedAgents(ctx context.Context, provider *aiv1alpha1.ModelProvider) {
	name := provider.Name
	mullingCount := 0
	triageCount := 0
	mullingList := &appsv1.DeploymentList{}
	if err := r.List(ctx, mullingList, client.MatchingLabels{"kubemoot.ai/mulling-provider": name}); err == nil {
		mullingCount = len(mullingList.Items)
	}
	triageList := &appsv1.DeploymentList{}
	if err := r.List(ctx, triageList, client.MatchingLabels{"kubemoot.ai/triage-provider": name}); err == nil {
		triageCount = len(triageList.Items)
	}
	provider.Status.Capacity.MullingAgentCount = mullingCount
	provider.Status.Capacity.TriageAgentCount = triageCount
	provider.Status.Capacity.AgentCount = mullingCount + triageCount
}

// updateStatus updates the ModelProvider status
func (r *ModelProviderReconciler) updateStatus(ctx context.Context, provider *aiv1alpha1.ModelProvider, ready bool, phase, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	provider.Status.Ready = ready
	provider.Status.Phase = phase
	provider.Status.Message = message

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&provider.Status.Conditions, condition)

	if err := r.Status().Update(ctx, provider); err != nil {
		log.Error(err, "Failed to update ModelProvider status")
		return ctrl.Result{}, err
	}

	// Publish live state snapshot to NATS KV for the agent-runtime
	// ProviderSelector to consume on each inference call. JIT-scheduling
	// foundation — see [[Epic - JIT GPU Scheduling]]. Errors are swallowed
	// to keep MP reconciliation resilient to NATS hiccups.
	r.publishStateToKV(ctx, provider)

	// Requeue interval: use probe interval from scheduler config when enabled
	requeueAfter := 5 * time.Minute
	if !ready {
		requeueAfter = 30 * time.Second
	} else if r.ConfigCache != nil {
		if sched := r.ConfigCache.GetSchedulerConfig(); sched != nil && sched.Enabled && sched.ProbeIntervalSeconds > 0 {
			requeueAfter = time.Duration(sched.ProbeIntervalSeconds) * time.Second
		}
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// publishStateToKV writes a snapshot of the provider's live capacity to
// the NATS KV bucket the agent runtime reads on each inference call.
// Foundation of JIT GPU scheduling: agents pick the freest provider per
// call by reading these snapshots, rather than the operator picking once
// at Agent reconcile time and freezing it into env vars.
//
// Errors are logged but never returned — provider reconciliation must not
// block on NATS health. When NATS is not configured, the Publisher's KV
// methods no-op, which leaves agents on whatever they were previously
// configured against (legacy single-endpoint env vars).
func (r *ModelProviderReconciler) publishStateToKV(ctx context.Context, provider *aiv1alpha1.ModelProvider) {
	if r.NATSPublisher == nil {
		return
	}
	log := logf.FromContext(ctx)

	state := buildProviderState(provider)

	payload, err := json.Marshal(state)
	if err != nil {
		log.V(1).Info("Failed to marshal provider state", "provider", provider.Name, "error", err)
		return
	}
	if err := r.NATSPublisher.PutKVValue(ProviderStateBucket, provider.Name, payload); err != nil {
		log.V(1).Info("Failed to publish provider state to NATS KV", "provider", provider.Name, "error", err)
	}
}

// buildProviderState is the single place the published ProviderState snapshot is
// assembled from a ModelProvider: identity and readiness, discovered capacity when
// the operator has it, and the declared memory budget when it does not. A CPU
// provider, or a GPU host without DCGM metrics, never gets a discovered total, and
// without one every agent refuses cold loads on it (the fit gate needs a budget).
func buildProviderState(provider *aiv1alpha1.ModelProvider) ProviderState {
	state := ProviderState{
		Name:         provider.Name,
		Endpoint:     provider.Spec.Endpoint,
		Ready:        provider.Status.Ready,
		LastProbedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if cap := provider.Status.Capacity; cap != nil {
		applyCapacityToState(&state, cap)
	}
	if state.TotalVramMiB <= 0 && provider.Spec.Scheduling != nil {
		state.TotalVramMiB = provider.Spec.Scheduling.MemoryMiB
	}
	if state.MaxParallel < 1 {
		state.MaxParallel = 1 // defensive: agents divide by this
	}
	return state
}

// applyCapacityToState maps the subset of DiscoveredCapacity the agent
// runtime needs into the ProviderState snapshot published to NATS KV.
// cap must be non-nil.
func applyCapacityToState(state *ProviderState, cap *aiv1alpha1.DiscoveredCapacity) {
	state.MaxParallel = cap.MaxParallel
	// ActiveCount/QueueDepth: v1 inputs, now superseded by the v2
	// VRAM-headroom ticket model. Agents stopped reading these in
	// the 2026-05-28 ProviderSelector rewrite; left at 0 here for
	// backward compatibility with any consumer still on the v1
	// saturation path. See kubemoot/docs/scheduler.md for the v2
	// design and why activeCount was unobservable in v1.
	state.ActiveCount = 0
	state.QueueDepth = 0
	for _, m := range cap.LoadedModels {
		state.LoadedModels = append(state.LoadedModels, m.Name)
	}
	// v2 fields: publish total VRAM (from DCGM) and per-loaded-model
	// footprints (from Ollama /api/ps size_vram). Agents combine
	// these with in-flight ticket footprints to compute live
	// headroom at the per-call decision boundary.
	state.TotalVramMiB = cap.VRAMTotalMiB
	if len(cap.LoadedModels) > 0 {
		state.LoadedModelFootprintsMiB = make(map[string]int64, len(cap.LoadedModels))
		for _, m := range cap.LoadedModels {
			// LoadedModel.SizeVRAM is bytes (from /api/ps); convert to MiB.
			state.LoadedModelFootprintsMiB[m.Name] = m.SizeVRAM / (1024 * 1024)
		}
	}
	if avail := availableFootprints(state.LoadedModelFootprintsMiB, cap.AvailableModels); len(avail) > 0 {
		state.AvailableModelFootprintsMiB = avail
	}
	state.ContextLength = cap.ContextLength
	state.LoadedModelContextLengths = loadedContextLengths(cap.LoadedModels)
}

// loadedContextLengths maps each loaded model that reports a per-request
// context to that context; nil when none does.
func loadedContextLengths(models []aiv1alpha1.LoadedModel) map[string]int {
	var loaded map[string]int
	for _, m := range models {
		if m.ContextLength <= 0 {
			continue
		}
		if loaded == nil {
			loaded = make(map[string]int, len(models))
		}
		loaded[m.Name] = m.ContextLength
	}
	return loaded
}

// availableFootprints builds the cold-load footprint proxy map from /api/tags
// on-disk sizes, excluding any model already observed loaded in VRAM (the
// /api/ps observation is preferred when both exist). Returns nil when empty.
func availableFootprints(loaded map[string]int64, available []aiv1alpha1.AvailableModel) map[string]int64 {
	if len(available) == 0 {
		return nil
	}
	avail := make(map[string]int64, len(available))
	for _, m := range available {
		if _, alreadyLoaded := loaded[m.Name]; alreadyLoaded {
			continue
		}
		// SizeBytes is on-disk; convert to MiB as a cold-load proxy.
		avail[m.Name] = m.SizeBytes / (1024 * 1024)
	}
	if len(avail) == 0 {
		return nil // all available models already loaded; match the nil-when-empty contract
	}
	return avail
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.ModelProvider{}).
		Named("modelprovider").
		// Reconcile ModelProviders concurrently. Each reconcile blocks on a chain
		// of HTTP probes (api/version, capacity discovery via api/ps, api/tags, and
		// Prometheus), each up to the client timeout. With a single worker, a
		// newly-created provider queues behind the slow reconciles of existing
		// providers and can miss its readiness window under load (the smoke-test
		// ModelProvider not becoming Ready in 60s). Matches the Model controller.
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}
