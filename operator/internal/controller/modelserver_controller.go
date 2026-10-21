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
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/internal/engine/vllm"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/pkg/perf"
)

// ServerStateBucket is the NATS KV bucket where per-server live capacity
// state is published for agent-runtime ServerSelector to read on each
// inference call. See tasks/notes/Epic - JIT GPU Scheduling.md for the
// architectural intent.
const ServerStateBucket = "kubemoot_server_state"

// ServerState is the snapshot the operator publishes to NATS KV for each
// ModelServer. Agents read it via ServerSelector at inference time to
// pick the freest server — JIT scheduling, emergent bin-packing.
//
// Fields mirror the subset of ModelServer.Status.Capacity that the agent
// runtime needs to make a per-call server decision. Kept narrow so the
// per-call read is small and fast.
type ServerState struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	// ModelEndpoints is the server URL for each model, by model, for a server that
	// runs one vLLM server per model; Endpoint is empty then. Absent when every
	// model is served at Endpoint.
	ModelEndpoints map[string]string `json:"modelEndpoints,omitempty"`
	// Engine is the model server's engine type (ModelServer spec.type); agents
	// pick how to call the server by it.
	Engine       string   `json:"engine,omitempty"`
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
	// per model once it loads. Agents skip a server whose context cannot hold a
	// call's prompt, because the engine cuts an oversized prompt without error.
	ContextLength int `json:"contextLength,omitempty"`
	// LoadedModelContextLengths maps loaded model name to the context window, in
	// tokens, each request to it gets (from Ollama /api/ps context_length).
	LoadedModelContextLengths map[string]int `json:"loadedModelContextLengths,omitempty"`
	// ModelSpeeds is what each model calibration found to fit costs on the
	// server: its calibrated load time, prompt and generation speed, and the
	// observed generation speed of crew calls since (see modelSpeeds).
	ModelSpeeds map[string]ModelSpeed `json:"modelSpeeds,omitempty"`
	// The fields below are what the server's engine adapter reports (see
	// engine.Snapshot). Each is absent when the engine does not report it.
	ModelStates         map[string]string `json:"modelStates,omitempty"`
	RequestsRunning     *int              `json:"requestsRunning,omitempty"`
	RequestsWaiting     *int              `json:"requestsWaiting,omitempty"`
	KVCacheUsage        *float64          `json:"kvCacheUsage,omitempty"`
	SwitchCostMs        map[string]int64  `json:"switchCostMs,omitempty"`
	HostMemoryParkedMiB *int64            `json:"hostMemoryParkedMiB,omitempty"`
}

// ModelSpeed is one model's entry in ServerState.ModelSpeeds.
type ModelSpeed struct {
	LoadMs                          int64 `json:"loadMs,omitempty"`
	PromptTokensPerSecond           int64 `json:"promptTokensPerSecond,omitempty"`
	GenerateTokensPerSecond         int64 `json:"generateTokensPerSecond"`
	ObservedGenerateTokensPerSecond int64 `json:"observedGenerateTokensPerSecond,omitempty"`
	ObservedSamples                 int   `json:"observedSamples,omitempty"`
	// LoadFactors is how much longer crew calls took as more of them shared the
	// server (see perf.Contention); absent with too few calls to tell.
	LoadFactors []perf.LoadFactor `json:"loadFactors,omitempty"`
}

// ModelServerReconciler reconciles a ModelServer object
type ModelServerReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	HTTPClient    *http.Client
	ConfigCache   *ConfigCache
	NATSPublisher *kubemootnats.Publisher

	// Pulls is the tracker shared with the Model controllers; it reports the disk
	// held by unfinished downloads. Nil counts none.
	Pulls *PullTracker

	// Calibration starts server calibrations; nil starts none.
	Calibration Calibrator
	// Drift watches calibrated servers for drift and asks the scheduling crew to
	// judge it; nil watches none.
	Drift DriftWatch

	// Engines picks the adapter that probes a server by its spec.type; nil uses
	// DefaultEngines.
	Engines *engine.Registry

	// probes holds each server's last engine snapshot until the state publish
	// reads it, by server name.
	probes sync.Map

	// shows keeps Ollama's /api/show answer per model digest (endpoint|name|digest),
	// so a model is shown once, not on every probe.
	shows sync.Map

	// switches holds how long each model last took to become ready, by server,
	// as the model lifecycle server observed it; the state publish carries it.
	switches switchLog
}

// OllamaVersionResponse represents the response from Ollama /api/version
type OllamaVersionResponse struct {
	Version string `json:"version"`
}

// prometheusStatusSuccess is the status of a Prometheus query that succeeded.
const prometheusStatusSuccess = "success"

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

// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelservers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=modelservers/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubemoot.ai,resources=agents,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// (deployments list/get is needed by countAssignedAgents to count agents
// per server via the mulling/triage server labels on agent deployments;
// chart RBAC already grants these on the operator's ClusterRole.)

// Reconcile is part of the main kubernetes reconciliation loop
func (r *ModelServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the ModelServer instance
	server := &aiv1alpha1.ModelServer{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		if apierrors.IsNotFound(err) {
			r.removeServerState(ctx, req.Name)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling ModelServer", "name", server.Name, "type", server.Spec.Type)

	// Update status based on server type
	switch server.Spec.Type {
	case aiv1alpha1.ServerTypeOllama:
		return r.reconcileOllama(ctx, server)
	case aiv1alpha1.ServerTypeVLLM:
		return r.reconcileVLLM(ctx, server)
	default:
		log.Info("Unsupported server type", "type", server.Spec.Type)
		return r.updateStatus(ctx, server, false, "Unsupported", aiv1alpha1.UnsupportedServerTypeMessage)
	}
}

// reconcileOllama handles Ollama server reconciliation
func (r *ModelServerReconciler) reconcileOllama(ctx context.Context, server *aiv1alpha1.ModelServer) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Each reconcile publishes only what its own probe read, so a server that
	// stops answering, or whose capacity discovery is off, publishes no engine fields.
	r.probes.Delete(server.Name)

	if server.Spec.Endpoint == "" {
		return r.updateStatus(ctx, server, false, "Failed", "Endpoint is required for Ollama server")
	}

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}

	// Check Ollama connectivity by calling /api/version
	versionURL := fmt.Sprintf("%s/api/version", server.Spec.Endpoint)
	resp, err := httpClient.Get(versionURL)
	if err != nil {
		log.Error(err, "Failed to connect to Ollama", "endpoint", server.Spec.Endpoint)
		return r.updateStatus(ctx, server, false, "Failed", fmt.Sprintf("Failed to connect to Ollama: %v", err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return r.updateStatus(ctx, server, false, "Failed", fmt.Sprintf("Ollama returned status %d", resp.StatusCode))
	}

	var versionResp OllamaVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&versionResp); err != nil {
		log.Error(err, "Failed to parse Ollama version response")
		return r.updateStatus(ctx, server, false, "Failed", "Failed to parse Ollama version")
	}

	log.Info("Successfully connected to Ollama", "version", versionResp.Version)

	server.Status.ServerInfo = &aiv1alpha1.ServerInfo{
		Version:     versionResp.Version,
		LastChecked: time.Now().UTC().Format(time.RFC3339),
	}

	r.discoverStorage(ctx, server, httpClient)

	// Discover capacity when scheduler is enabled
	if r.ConfigCache != nil && r.ConfigCache.IsSchedulerEnabled() {
		r.discoverCapacity(ctx, server, httpClient)
	}

	r.discoverOllamaCatalog(ctx, server, httpClient)

	waiting := r.requestCalibration(ctx, server)
	r.reviewDrift(ctx, server)

	result, err := r.updateStatusWaiting(ctx, server, true, phaseReady, "Connected to Ollama", waiting.err())
	return retryWhileWaiting(result, waiting), err
}

// discoverCapacity probes the Ollama pod and Prometheus for capacity information
func (r *ModelServerReconciler) discoverCapacity(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) {
	log := logf.FromContext(ctx)

	if server.Status.Capacity == nil {
		server.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{}
	}
	now := metav1.Now()
	server.Status.Capacity.LastProbed = &now

	// 1-4. Inspect the backing Ollama pod (env vars, node, DCGM metrics).
	r.discoverPodCapacity(ctx, server, httpClient)

	// 5. Ask the server's engine adapter for loaded and available models
	r.probeEngine(ctx, server, httpClient)

	// 6. Count agents assigned to this server
	r.countAssignedAgents(ctx, server)

	log.Info("Discovered server capacity",
		"server", server.Name,
		"maxParallel", server.Status.Capacity.MaxParallel,
		"vramTotalMiB", server.Status.Capacity.VRAMTotalMiB,
		"vramUsedMiB", server.Status.Capacity.VRAMUsedMiB,
		"agentCount", server.Status.Capacity.AgentCount,
		"nodeName", server.Status.Capacity.NodeName,
		"loadedModels", len(server.Status.Capacity.LoadedModels),
		"availableModels", len(server.Status.Capacity.AvailableModels),
	)
}

// discoverPodCapacity resolves the backing Ollama pod from the server
// endpoint and reads node name, OLLAMA_NUM_PARALLEL, and DCGM GPU metrics
// into server.Status.Capacity. Best-effort: missing pieces are logged at
// V(1) and skipped, leaving the corresponding capacity fields untouched.
func (r *ModelServerReconciler) discoverPodCapacity(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) {
	pod := r.findOllamaPod(ctx, server)
	if pod == nil {
		return
	}

	server.Status.Capacity.NodeName = pod.Spec.NodeName
	r.applyEngineEnvFromPod(ctx, pod, server)

	// Query Prometheus for DCGM metrics if configured
	schedulerConfig := r.ConfigCache.GetSchedulerConfig()
	if schedulerConfig != nil && schedulerConfig.PrometheusEndpoint != "" && pod.Spec.NodeName != "" {
		r.queryDCGMMetrics(ctx, httpClient, schedulerConfig.PrometheusEndpoint, pod.Spec.NodeName, server)
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
func (r *ModelServerReconciler) applyEngineEnvFromPod(ctx context.Context, pod *corev1.Pod, server *aiv1alpha1.ModelServer) {
	log := logf.FromContext(ctx)
	for _, container := range pod.Spec.Containers {
		for _, env := range container.Env {
			set, known := engineEnvCapacity[env.Name]
			if !known {
				continue
			}
			if val, err := strconv.Atoi(env.Value); err == nil && val > 0 {
				set(server.Status.Capacity, val)
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
func (r *ModelServerReconciler) findBackingPod(ctx context.Context, svcName, svcNamespace string) (string, string) {
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
func (r *ModelServerReconciler) queryDCGMMetrics(ctx context.Context, httpClient *http.Client, prometheusEndpoint, nodeName string, server *aiv1alpha1.ModelServer) {
	log := logf.FromContext(ctx)

	// VRAM total = FB_FREE + FB_USED + FB_RESERVED, summed across GPUs
	// on the node (sum() handles the multi-GPU-per-node case gracefully;
	// single-GPU passthrough returns the lone series unchanged).
	vramQuery := fmt.Sprintf(
		`sum(DCGM_FI_DEV_FB_FREE{Hostname="%s"} + DCGM_FI_DEV_FB_USED{Hostname="%s"} + DCGM_FI_DEV_FB_RESERVED{Hostname="%s"})`,
		nodeName, nodeName, nodeName)
	vramTotal := r.queryPrometheusScalar(ctx, httpClient, prometheusEndpoint, vramQuery)
	if vramTotal > 0 {
		server.Status.Capacity.VRAMTotalMiB = int64(vramTotal)
		log.V(1).Info("Discovered VRAM total from DCGM", "vramTotalMiB", vramTotal)
	}

	// Query GPU model name (same Hostname label fix as above).
	gpuModel := r.queryPrometheusLabel(ctx, httpClient, prometheusEndpoint,
		fmt.Sprintf(`DCGM_FI_DEV_GPU_TEMP{Hostname="%s"}`, nodeName), "modelName")
	if gpuModel != "" {
		server.Status.Capacity.GPUModel = gpuModel
	}
}

// queryPrometheusScalar queries Prometheus for a single scalar value
func (r *ModelServerReconciler) queryPrometheusScalar(ctx context.Context, httpClient *http.Client, endpoint, query string) float64 {
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
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0
	}

	return prometheusScalarValue(body)
}

// prometheusScalarValue reads the first sample of a Prometheus instant-query
// response body, whose value is [timestamp, "value_string"]. It returns 0 when the
// body is not a successful query with a numeric first sample.
func prometheusScalarValue(body []byte) float64 {
	var promResp prometheusQueryResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return 0
	}
	if promResp.Status != prometheusStatusSuccess || len(promResp.Data.Result) == 0 {
		return 0
	}
	sample := promResp.Data.Result[0].Value
	if len(sample) < 2 {
		return 0
	}
	valStr, ok := sample[1].(string)
	if !ok {
		return 0
	}
	val, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return 0
	}
	return val
}

// queryPrometheusLabel queries Prometheus and extracts a label value from the first result
func (r *ModelServerReconciler) queryPrometheusLabel(ctx context.Context, httpClient *http.Client, endpoint, query, labelName string) string {
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
	defer func() { _ = resp.Body.Close() }()

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

	if promResp.Status != prometheusStatusSuccess || len(promResp.Data.Result) == 0 {
		return ""
	}

	return promResp.Data.Result[0].Metric[labelName]
}

// probeEngine asks the adapter for the server's spec.type to probe the server,
// records what it found in the server's capacity, and keeps the snapshot for the
// state publish. A reply the adapter could not read leaves the earlier capacity
// as it was.
func (r *ModelServerReconciler) probeEngine(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) {
	if _, err := r.probeSnapshot(ctx, server, httpClient); err != nil {
		logf.FromContext(ctx).V(1).Info("Engine probe failed", "server", server.Name, "error", err)
	}
}

// probeSnapshot probes the server through its engine adapter, applies the
// snapshot to the server's capacity (created when absent), and keeps the
// snapshot for the state publish. It returns the adapter's error when the probe
// gave no usable answer.
func (r *ModelServerReconciler) probeSnapshot(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) (engine.Snapshot, error) {
	adapter, err := r.engines().Adapter(server.Spec.Type, httpClient)
	if err != nil {
		return engine.Snapshot{}, err
	}
	snap, err := adapter.Probe(ctx, server.EffectiveSpec())
	if err != nil {
		return engine.Snapshot{}, err
	}
	if server.Status.Capacity == nil {
		server.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{}
	}
	applySnapshotToCapacity(server.Status.Capacity, snap)
	r.probes.Store(server.Name, snap)
	return snap, nil
}

// engines is the registry that picks the adapter for a server type.
func (r *ModelServerReconciler) engines() *engine.Registry {
	if r.Engines != nil {
		return r.Engines
	}
	return DefaultEngines()
}

// applySnapshotToCapacity copies what a probe read into the server's discovered
// capacity: the loaded models with their accelerator memory use, and the models
// the server has. Whatever the probe did not read stays as it was.
func applySnapshotToCapacity(capacity *aiv1alpha1.DiscoveredCapacity, snap engine.Snapshot) {
	if snap.LoadedKnown {
		capacity.LoadedModels = snap.Loaded
		if snap.LoadedSizesKnown {
			capacity.VRAMUsedMiB = snap.VRAMUsedMiB()
		}
	}
	if snap.AvailableKnown {
		capacity.AvailableModels = snap.Available
	}
}

// countAssignedAgents counts how many agent deployments are currently
// targeting this server, summing across the mulling and triage phases.
// Each agent deployment carries two labels (`kubemoot.ai/mulling-server`
// and `kubemoot.ai/triage-server`) set by the AgentReconciler at deploy
// time; agents that use this server for both phases contribute 2 to the
// count (they use the server once per phase). The counts are for status
// and the dashboard; model selection does not read them.
//
// When/if Scheduler v2 lands and binding becomes per-discussion on NATS
// subjects rather than per-agent on Deployments, this should switch to
// counting active NATS-published bindings.
func (r *ModelServerReconciler) countAssignedAgents(ctx context.Context, server *aiv1alpha1.ModelServer) {
	name := server.Name
	mullingCount := 0
	triageCount := 0
	mullingList := &appsv1.DeploymentList{}
	if err := r.List(ctx, mullingList, client.MatchingLabels{labelMullingServer: name}); err == nil {
		mullingCount = len(mullingList.Items)
	}
	triageList := &appsv1.DeploymentList{}
	if err := r.List(ctx, triageList, client.MatchingLabels{labelTriageServer: name}); err == nil {
		triageCount = len(triageList.Items)
	}
	server.Status.Capacity.MullingAgentCount = mullingCount
	server.Status.Capacity.TriageAgentCount = triageCount
	server.Status.Capacity.AgentCount = mullingCount + triageCount
}

// updateStatus updates the ModelServer status
func (r *ModelServerReconciler) updateStatus(ctx context.Context, server *aiv1alpha1.ModelServer, ready bool, phase, message string) (ctrl.Result, error) {
	return r.updateStatusWaiting(ctx, server, ready, phase, message, nil)
}

// updateStatusWaiting is updateStatus for a reconcile whose calibration waits for
// the bus for the given cause (nil when it does not). It derives the Calibrated
// condition last, after every patch of the reconcile, so every path (including the
// early returns of an unreachable server) states it from the current record.
func (r *ModelServerReconciler) updateStatusWaiting(ctx context.Context, server *aiv1alpha1.ModelServer, ready bool, phase, message string, waiting error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	setCalibratedCondition(server, waiting)

	server.Status.Ready = ready
	server.Status.Phase = phase
	server.Status.Message = message

	condition := metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             phase,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&server.Status.Conditions, condition)

	if err := r.Status().Update(ctx, server); err != nil {
		log.Error(err, "Failed to update ModelServer status")
		return ctrl.Result{}, err
	}

	// Publish live state snapshot to NATS KV for the agent-runtime
	// ServerSelector to consume on each inference call. JIT-scheduling
	// foundation — see [[Epic - JIT GPU Scheduling]]. Errors are swallowed
	// to keep MP reconciliation resilient to NATS hiccups.
	r.publishStateToKV(ctx, server)

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

// publishStateToKV writes a snapshot of the server's live capacity to
// the NATS KV bucket the agent runtime reads on each inference call.
// Foundation of JIT GPU scheduling: agents pick the freest server per
// call by reading these snapshots, rather than the operator picking once
// at Agent reconcile time and freezing it into env vars.
//
// Errors are logged but never returned — server reconciliation must not
// block on NATS health. When NATS is not configured, the Publisher's KV
// methods no-op, which leaves agents on whatever they were previously
// configured against (legacy single-endpoint env vars).
func (r *ModelServerReconciler) publishStateToKV(ctx context.Context, server *aiv1alpha1.ModelServer) {
	observed, load := r.observed(ctx, server)
	speeds := modelSpeeds(server.Status.Calibration, observed, load)
	recordServerMetrics(server, speeds)
	if r.NATSPublisher == nil {
		return
	}
	log := logf.FromContext(ctx)

	state := buildServerState(server)
	state.ModelSpeeds = speeds
	if snap, ok := r.probes.Load(server.Name); ok {
		applySnapshotToState(&state, snap.(engine.Snapshot))
	}
	state.SwitchCostMs = mergeSwitchCosts(state.SwitchCostMs, r.switches.For(server.Name))

	payload, err := json.Marshal(state)
	if err != nil {
		log.V(1).Info("Failed to marshal server state", "server", server.Name, "error", err)
		return
	}
	if err := r.NATSPublisher.PutKVValue(ServerStateBucket, server.Name, payload); err != nil {
		log.V(1).Info("Failed to publish server state to NATS KV", "server", server.Name, "error", err)
	}
}

// removeServerState deletes the server's entry from the state bucket once no
// ModelServer of that name exists in any namespace. Like publishStateToKV it never
// fails the reconcile.
func (r *ModelServerReconciler) removeServerState(ctx context.Context, name string) {
	forgetServerMetrics(name)
	r.switches.Forget(name)
	r.probes.Delete(name)
	if r.NATSPublisher == nil {
		return
	}
	deleteOrphanedServerState(ctx, r.Client, r.NATSPublisher, name)
}

// deleteOrphanedServerState deletes the state key unless a ModelServer still has that name.
func deleteOrphanedServerState(ctx context.Context, c client.Reader, store serverStateStore, name string) {
	exists, err := serverNameExists(ctx, c, name)
	if err != nil || exists {
		return
	}
	if err := store.DeleteKVKey(ServerStateBucket, name); err != nil {
		logf.FromContext(ctx).V(1).Info("Failed to delete server state from NATS KV", "server", name, "error", err)
	}
}

// serverNameExists reports whether any ModelServer carries the name.
func serverNameExists(ctx context.Context, c client.Reader, name string) (bool, error) {
	list := &aiv1alpha1.ModelServerList{}
	if err := c.List(ctx, list); err != nil {
		return false, err
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return true, nil
		}
	}
	return false, nil
}

// buildServerState is the single place the published ServerState snapshot is
// assembled from a ModelServer: identity and readiness, discovered capacity when
// the operator has it, and the declared memory budget when it does not. A CPU
// server, or a GPU host without DCGM metrics, never gets a discovered total, and
// without one every agent refuses cold loads on it (the fit gate needs a budget).
func buildServerState(server *aiv1alpha1.ModelServer) ServerState {
	state := ServerState{
		Name:         server.Name,
		Endpoint:     server.Spec.Endpoint,
		Engine:       string(server.Spec.Type),
		Ready:        server.Status.Ready,
		LastProbedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if server.Spec.Type == aiv1alpha1.ServerTypeVLLM {
		state.ModelEndpoints = modelEndpoints(server.VLLMServers())
	}
	if cap := server.Status.Capacity; cap != nil {
		applyCapacityToState(&state, cap)
	}
	if state.TotalVramMiB <= 0 && server.Spec.Scheduling != nil {
		state.TotalVramMiB = server.Spec.Scheduling.MemoryMiB
	}
	if state.MaxParallel < 1 {
		state.MaxParallel = defaultMaxParallel(server.Spec.Type) // defensive: agents divide by this
	}
	return state
}

// defaultMaxParallel is the parallelism published for a server that discovered
// none. A vLLM server has no fixed slots: it batches requests, so one is not a
// limit a reader may serialize on; it publishes the engine's own ceiling on
// concurrently scheduled sequences, and the load fields (requestsWaiting,
// kvCacheUsage) say whether it has room. Every other engine publishes the minimum.
func defaultMaxParallel(t aiv1alpha1.ServerType) int {
	if t == aiv1alpha1.ServerTypeVLLM {
		return vllm.DefaultMaxSequences
	}
	return 1
}

// modelEndpoints maps each served model to its server's endpoint; nil without servers.
func modelEndpoints(servers []aiv1alpha1.VLLMServer) map[string]string {
	if len(servers) == 0 {
		return nil
	}
	out := make(map[string]string, len(servers))
	for _, s := range servers {
		out[s.Model] = s.Endpoint
	}
	return out
}

// applySnapshotToState copies the fields an engine adapter reports into the
// published state. A field the engine did not report stays absent.
func applySnapshotToState(state *ServerState, snap engine.Snapshot) {
	if len(snap.ModelStates) > 0 {
		state.ModelStates = make(map[string]string, len(snap.ModelStates))
		for model, st := range snap.ModelStates {
			state.ModelStates[model] = string(st)
		}
	}
	state.RequestsRunning = snap.RequestsRunning
	state.RequestsWaiting = snap.RequestsWaiting
	state.KVCacheUsage = snap.KVCacheUsage
	if len(snap.SwitchCostMs) > 0 {
		state.SwitchCostMs = snap.SwitchCostMs
	}
	state.HostMemoryParkedMiB = snap.HostMemoryParkedMiB
}

// applyCapacityToState maps the subset of DiscoveredCapacity the agent
// runtime needs into the ServerState snapshot published to NATS KV.
// cap must be non-nil.
func applyCapacityToState(state *ServerState, cap *aiv1alpha1.DiscoveredCapacity) {
	state.MaxParallel = cap.MaxParallel
	// ActiveCount/QueueDepth: v1 inputs, now superseded by the v2
	// VRAM-headroom ticket model. Agents stopped reading these in
	// the 2026-05-28 ServerSelector rewrite; left at 0 here for
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
			state.LoadedModelFootprintsMiB[m.Name] = residentFootprintMiB(m, cap.VRAMTotalMiB > 0)
		}
	}
	if avail := availableFootprints(state.LoadedModelFootprintsMiB, cap.AvailableModels); len(avail) > 0 {
		state.AvailableModelFootprintsMiB = avail
	}
	state.ContextLength = cap.ContextLength
	state.LoadedModelContextLengths = loadedContextLengths(cap.LoadedModels)
}

// residentFootprintMiB is the memory a loaded model holds against the server's
// budget, in MiB. /api/ps reports bytes. A GPU host (VRAM discovered) counts
// size_vram only. A server without a discovered VRAM total budgets the
// declared spec.scheduling.memoryMiB, and a CPU server reports size_vram 0, so
// there the model's whole size counts.
func residentFootprintMiB(m aiv1alpha1.LoadedModel, vramDiscovered bool) int64 {
	bytes := m.SizeVRAM
	if bytes <= 0 && !vramDiscovered {
		bytes = m.Size
	}
	return bytes / (1024 * 1024)
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
func (r *ModelServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.ModelServer{}).
		Named("modelserver").
		// A server that names a Service by spec.serviceRef finds its servers
		// from the Service's ports, so a change to the Service reconciles it.
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(r.servicesToServers)).
		// A server's catalog merges the Model objects in its namespace that name it,
		// so adding, changing or removing one reconciles the server at once.
		Watches(&aiv1alpha1.Model{}, handler.EnqueueRequestsFromMapFunc(modelToServer),
			builder.WithPredicates(modelBindingChanged())).
		// Reconcile ModelServers concurrently. Each reconcile blocks on a chain
		// of HTTP probes (api/version, capacity discovery via api/ps, api/tags, and
		// Prometheus), each up to the client timeout. With a single worker, a
		// newly-created server queues behind the slow reconciles of existing
		// servers and can miss its readiness window under load (the smoke-test
		// ModelServer not becoming Ready in 60s). Matches the Model controller.
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

// observed is what crew calls showed for each model on the server since its
// calibration; none when the history cannot be read or there is no calibration.
func (r *ModelServerReconciler) observed(ctx context.Context,
	server *aiv1alpha1.ModelServer) ([]perf.Observation, map[string][]perf.LoadFactor) {
	cal := server.Status.Calibration
	if r.NATSPublisher == nil || !r.NATSPublisher.Configured() || cal == nil ||
		cal.Phase != calibrationPhaseCalibrated || cal.CompletedAt == nil {
		return nil, nil
	}
	entries, err := perf.ReadHistory(r.NATSPublisher, server.Name)
	if err != nil {
		logf.FromContext(ctx).V(1).Info("could not read the performance history", "server", server.Name, "err", err)
		return nil, nil
	}
	_, since := perf.CalibratedSpeeds(cal)
	return perf.Observe(entries, server.Name, since), perf.Contention(entries, server.Name, since, calibratedSpeeds(cal))
}

// calibratedSpeeds are the prompt and generation speeds of each model calibration found to fit.
func calibratedSpeeds(cal *aiv1alpha1.ServerCalibration) map[string]perf.Speeds {
	out := map[string]perf.Speeds{}
	for _, m := range cal.Models {
		if m.Fits && m.HasSpeeds() {
			out[m.Model] = perf.Speeds{PromptTokensPerSecond: m.PromptTokensPerSecond,
				GenerateTokensPerSecond: m.GenerateTokensPerSecond}
		}
	}
	return out
}

// modelSpeeds is, for each model calibration found to fit, its calibrated costs, the
// observed generation speed of solo crew calls once there are enough of them to trust
// (perf.MinSamples), and how much slower calls ran as more shared the server.
func modelSpeeds(cal *aiv1alpha1.ServerCalibration, observed []perf.Observation,
	load map[string][]perf.LoadFactor) map[string]ModelSpeed {
	if cal == nil {
		return nil
	}
	seen := make(map[string]perf.Observation, len(observed))
	for _, o := range observed {
		seen[o.Model] = o
	}
	out := map[string]ModelSpeed{}
	for _, m := range cal.Models {
		if !m.Fits || !m.HasSpeeds() || m.GenerateTokensPerSecond <= 0 {
			continue
		}
		speed := ModelSpeed{LoadMs: m.LoadMs, PromptTokensPerSecond: m.PromptTokensPerSecond,
			GenerateTokensPerSecond: m.GenerateTokensPerSecond, LoadFactors: load[m.Model]}
		if o, ok := seen[m.Model]; ok && o.SoloSamples >= perf.MinSamples {
			speed.ObservedGenerateTokensPerSecond, speed.ObservedSamples = o.SoloTokensPerSecond, o.SoloSamples
		}
		out[m.Model] = speed
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
