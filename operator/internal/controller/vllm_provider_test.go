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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

const (
	vllmProviderName = "vllm-gpu"
	vllmModel14      = "qwen3:14b"
	vllmEndpoint14   = "http://vllm-14b.models:8000"
	vllmModel8       = "qwen3:8b"
	vllmRevision     = "31c69efc"
	vllmQuantization = "awq"
)

// fakeVLLM is one vLLM server. A zero value answers as an awake, healthy server
// with no metrics.
type fakeVLLM struct {
	asleep    bool
	unhealthy bool
	// metrics is the /metrics page; empty answers 404.
	metrics string
}

func (f fakeVLLM) start(t *testing.T, served string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health" && f.unhealthy:
			w.WriteHeader(http.StatusServiceUnavailable)
		case r.URL.Path == "/health":
		case r.URL.Path == "/v1/models":
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q}]}`, served)
		case r.URL.Path == "/is_sleeping":
			_, _ = fmt.Fprintf(w, `{"is_sleeping":%t}`, f.asleep)
		case r.URL.Path == "/metrics" && f.metrics != "":
			_, _ = io.WriteString(w, f.metrics)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// vllmMetrics is a /metrics page with these values.
func vllmMetrics(running, waiting int, usage float64) string {
	return fmt.Sprintf("# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running{engine=\"0\"} %d.0\n"+
		"# TYPE vllm:num_requests_waiting gauge\nvllm:num_requests_waiting{engine=\"0\"} %d.0\n"+
		"# TYPE vllm:kv_cache_usage_perc gauge\nvllm:kv_cache_usage_perc{engine=\"0\"} %g\n", running, waiting, usage)
}

// vllmProvider is a vllm provider over the given model-to-endpoint servers.
func vllmProvider(servers ...aiv1alpha1.VLLMServer) *aiv1alpha1.ModelProvider {
	return &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: vllmProviderName, Namespace: testNS1},
		Spec: aiv1alpha1.ModelProviderSpec{
			Type: aiv1alpha1.ProviderTypeVLLM,
			VLLM: &aiv1alpha1.VLLMSpec{Servers: servers},
		},
	}
}

func server(model, endpoint string) aiv1alpha1.VLLMServer {
	return aiv1alpha1.VLLMServer{Model: model, Endpoint: endpoint}
}

// reconcileVLLMProvider reconciles p and returns it as stored with the reconciler,
// whose kept snapshot feeds the state publish.
func reconcileVLLMProvider(t *testing.T, p *aiv1alpha1.ModelProvider) (*aiv1alpha1.ModelProvider, *ModelProviderReconciler) {
	t.Helper()
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(p).WithStatusSubresource(p).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	key := types.NamespacedName{Name: p.Name, Namespace: p.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := &aiv1alpha1.ModelProvider{}
	if err := cli.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	return got, r
}

// publishedState is the state the provider would publish after its reconcile,
// decoded into the generated contract type with no field the schema lacks.
func publishedState(t *testing.T, p *aiv1alpha1.ModelProvider, r *ModelProviderReconciler) bus.ProviderState {
	t.Helper()
	state := buildProviderState(p)
	if snap, ok := r.probes.Load(p.Name); ok {
		applySnapshotToState(&state, snap.(engine.Snapshot))
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var contract bus.ProviderState
	if err := dec.Decode(&contract); err != nil {
		t.Fatalf("state does not match the contract: %v\n%s", err, raw)
	}
	return contract
}

func TestDefaultEnginesRegistersOllamaAndVLLM(t *testing.T) {
	for _, typ := range []aiv1alpha1.ProviderType{aiv1alpha1.ProviderTypeOllama, aiv1alpha1.ProviderTypeVLLM} {
		if _, err := DefaultEngines().Adapter(typ, http.DefaultClient); err != nil {
			t.Errorf("%s must be registered: %v", typ, err)
		}
	}
	if _, err := DefaultEngines().Adapter("openai", http.DefaultClient); err == nil {
		t.Error("openai has no adapter")
	}
}

func TestVLLMProviderReconcilePublishesStatesAndRequestCounts(t *testing.T) {
	awake := fakeVLLM{metrics: vllmMetrics(3, 2, 0.4)}.start(t, vllmModel14)
	asleep := fakeVLLM{asleep: true, metrics: vllmMetrics(0, 0, 0)}.start(t, vllmModel8)
	p, r := reconcileVLLMProvider(t, vllmProvider(server(vllmModel14, awake.URL), server(vllmModel8, asleep.URL)))

	if !p.Status.Ready || p.Status.Message != "2 of 2 vLLM servers answering" {
		t.Errorf("status = ready %v %q", p.Status.Ready, p.Status.Message)
	}
	state := publishedState(t, p, r)
	if state.Engine != "vllm" || state.Endpoint != "" {
		t.Errorf("engine %q endpoint %q: a provider of several servers has no single endpoint", state.Engine, state.Endpoint)
	}
	if state.ModelEndpoints[vllmModel14] != awake.URL || state.ModelEndpoints[vllmModel8] != asleep.URL {
		t.Errorf("modelEndpoints = %v", state.ModelEndpoints)
	}
	wantStates(t, state)
	wantRequestCounts(t, state, 3, 2, 0.4)
}

// wantStates fails unless the first model is awake and loaded and the second asleep.
func wantStates(t *testing.T, state bus.ProviderState) {
	t.Helper()
	if state.ModelStates[vllmModel14] != "awake" || state.ModelStates[vllmModel8] != "asleep" {
		t.Errorf("modelStates = %v", state.ModelStates)
	}
	if len(state.LoadedModels) != 1 || state.LoadedModels[0] != vllmModel14 {
		t.Errorf("loadedModels = %v, want only the awake model", state.LoadedModels)
	}
}

// wantRequestCounts fails unless the state carries these request counts and cache use.
func wantRequestCounts(t *testing.T, state bus.ProviderState, running, waiting int64, usage float64) {
	t.Helper()
	if state.RequestsRunning == nil || state.RequestsWaiting == nil || state.KvCacheUsage == nil {
		t.Fatalf("counts missing: %+v", state)
	}
	if *state.RequestsRunning != running || *state.RequestsWaiting != waiting || *state.KvCacheUsage != usage {
		t.Errorf("counts = %d/%d usage %v, want %d/%d %v (sum, sum, max)",
			*state.RequestsRunning, *state.RequestsWaiting, *state.KvCacheUsage, running, waiting, usage)
	}
}

func TestVLLMProviderReconcileKeepsAReportedZeroAndLeavesAbsentMetricsOut(t *testing.T) {
	zero := fakeVLLM{metrics: vllmMetrics(0, 0, 0)}.start(t, vllmModel14)
	p, r := reconcileVLLMProvider(t, vllmProvider(server(vllmModel14, zero.URL)))
	state := publishedState(t, p, r)
	if state.RequestsRunning == nil || *state.RequestsRunning != 0 || state.RequestsWaiting == nil || state.KvCacheUsage == nil {
		t.Errorf("a reported 0 must be published: %+v", state)
	}

	silent := fakeVLLM{}.start(t, vllmModel14)
	p, r = reconcileVLLMProvider(t, vllmProvider(server(vllmModel14, silent.URL)))
	state = publishedState(t, p, r)
	if state.RequestsRunning != nil || state.RequestsWaiting != nil || state.KvCacheUsage != nil {
		t.Errorf("metrics the server lacks must stay absent: %+v", state)
	}
}

func TestVLLMProviderIsReadyWhileOneServerAnswers(t *testing.T) {
	up := fakeVLLM{}.start(t, vllmModel14)
	down := fakeVLLM{unhealthy: true}.start(t, vllmModel8)
	p, r := reconcileVLLMProvider(t, vllmProvider(server(vllmModel14, up.URL), server(vllmModel8, down.URL)))

	if !p.Status.Ready || p.Status.Message != "1 of 2 vLLM servers answering" {
		t.Errorf("status = ready %v %q", p.Status.Ready, p.Status.Message)
	}
	state := publishedState(t, p, r)
	if _, listed := state.ModelStates[vllmModel8]; listed {
		t.Errorf("a server that does not answer has no state: %v", state.ModelStates)
	}
}

func TestVLLMProviderFailsWhenNoServerAnswers(t *testing.T) {
	down := fakeVLLM{unhealthy: true}.start(t, vllmModel14)
	p, r := reconcileVLLMProvider(t, vllmProvider(server(vllmModel14, down.URL)))
	if p.Status.Ready || p.Status.Phase != "Failed" {
		t.Errorf("status = ready %v phase %q, want Failed", p.Status.Ready, p.Status.Phase)
	}
	if _, kept := r.probes.Load(p.Name); kept {
		t.Error("a provider with no answering server publishes no engine fields")
	}
}

func TestVLLMProviderWithoutServersFails(t *testing.T) {
	p, _ := reconcileVLLMProvider(t, vllmProvider())
	if p.Status.Ready || p.Status.Phase != "Failed" {
		t.Errorf("status = ready %v phase %q, want Failed", p.Status.Ready, p.Status.Phase)
	}
}

// Accelerator memory in use is read from the loaded models only when the engine
// reports it; vLLM does not, so a value found another way is kept.
func TestApplySnapshotToCapacityKeepsMemoryUseTheEngineDoesNotReport(t *testing.T) {
	capacity := &aiv1alpha1.DiscoveredCapacity{VRAMUsedMiB: 4096}
	applySnapshotToCapacity(capacity, engine.Snapshot{LoadedKnown: true, Loaded: []aiv1alpha1.LoadedModel{{Name: vllmModel14}}})
	if capacity.VRAMUsedMiB != 4096 || len(capacity.LoadedModels) != 1 {
		t.Errorf("capacity = %+v, want loaded models set and memory use kept", capacity)
	}
	applySnapshotToCapacity(capacity, engine.Snapshot{LoadedKnown: true, LoadedSizesKnown: true})
	if capacity.VRAMUsedMiB != 0 {
		t.Errorf("an engine that reports sizes and loads nothing uses 0, got %d", capacity.VRAMUsedMiB)
	}
}

func TestEndpointForNamesTheServerOfAVLLMModel(t *testing.T) {
	vllm := vllmProvider(server(vllmModel14, "http://a:8000"), server(vllmModel8, "http://b:8000"))
	ollama := aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: "http://o:11434"}
	cases := map[string]struct{ got, want string }{
		"first server":              {vllm.Spec.EndpointFor(vllmModel14), "http://a:8000"},
		"second server":             {vllm.Spec.EndpointFor(vllmModel8), "http://b:8000"},
		"model with no server":      {vllm.Spec.EndpointFor("other"), ""},
		"ollama has one endpoint":   {ollama.EndpointFor(vllmModel14), "http://o:11434"},
		"vllm without a vllm block": {(&aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeVLLM}).EndpointFor("x"), ""},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: EndpointFor = %q, want %q", name, c.got, c.want)
		}
	}
}

// vllmModelCR is a Model on the vllm provider with a Hugging Face source.
func vllmModelCR(name, served string) *aiv1alpha1.Model {
	return &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testCrewNamespace},
		Spec: aiv1alpha1.ModelSpec{
			Model:       served,
			ProviderRef: vllmProviderName,
			Source: &aiv1alpha1.ModelSource{HuggingFace: &aiv1alpha1.HuggingFaceSource{
				Repo: "Qwen/Qwen3-14B-AWQ", Revision: vllmRevision, Quantization: vllmQuantization,
			}},
		},
	}
}

// reconcileVLLMModelCR reconciles a Model against a Ready vllm provider through
// the finalizer pass and the status pass, and returns the Model as stored.
func reconcileVLLMModelCR(t *testing.T, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) *aiv1alpha1.Model {
	t.Helper()
	provider.Status.Ready = true
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).
		WithObjects(model, provider).WithStatusSubresource(model, provider).Build()
	r := &ModelReconciler{Client: cl}
	key := types.NamespacedName{Name: model.Name, Namespace: model.Namespace}
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	got := &aiv1alpha1.Model{}
	if err := cl.Get(context.Background(), key, got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestVLLMModelIsAvailableWhenItsServerAnswers(t *testing.T) {
	cases := map[string]struct {
		server    fakeVLLM
		wantState string
		wantReady bool
	}{
		"awake server":    {fakeVLLM{}, stateLoaded, true},
		"sleeping server": {fakeVLLM{asleep: true}, stateAvailable, true},
		"unhealthy":       {fakeVLLM{unhealthy: true}, statePending, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := c.server.start(t, vllmModel14)
			got := reconcileVLLMModelCR(t, vllmModelCR("m", vllmModel14), vllmProvider(server(vllmModel14, srv.URL)))
			if got.Status.State != c.wantState || got.Status.Ready != c.wantReady {
				t.Errorf("state %q ready %v, want %q %v (%s)", got.Status.State, got.Status.Ready, c.wantState, c.wantReady, got.Status.Message)
			}
			if got.Status.Endpoint != srv.URL {
				t.Errorf("status.endpoint = %q, want the model's server %q", got.Status.Endpoint, srv.URL)
			}
		})
	}
}

func TestVLLMModelNeverPulls(t *testing.T) {
	var pulls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pull" {
			pulls++
		}
		if r.URL.Path == "/v1/models" {
			_, _ = fmt.Fprintf(w, `{"data":[{"id":%q}]}`, vllmModel14)
		}
	}))
	defer srv.Close()
	got := reconcileVLLMModelCR(t, vllmModelCR("m", vllmModel14), vllmProvider(server(vllmModel14, srv.URL)))
	if pulls != 0 || got.Status.Pull != nil {
		t.Errorf("the operator pulls nothing for vllm: pulls=%d pull=%+v", pulls, got.Status.Pull)
	}
	if got.Status.ModelInfo == nil || got.Status.ModelInfo.Quantization != vllmQuantization {
		t.Errorf("the source's quantization is reported, got %+v", got.Status.ModelInfo)
	}
}

func TestVLLMModelWithoutAServerOrAFittingSourceIsAnError(t *testing.T) {
	srv := fakeVLLM{}.start(t, vllmModel14)
	provider := vllmProvider(server(vllmModel14, srv.URL))

	noServer := reconcileVLLMModelCR(t, vllmModelCR("m", vllmModel8), provider.DeepCopy())
	if noServer.Status.State != stateError || noServer.Status.Ready {
		t.Errorf("a served name with no server: state %q ready %v", noServer.Status.State, noServer.Status.Ready)
	}

	noSource := vllmModelCR("m2", vllmModel14)
	noSource.Spec.Source = nil
	got := reconcileVLLMModelCR(t, noSource, provider.DeepCopy())
	if got.Status.State != stateError || got.Status.Ready {
		t.Errorf("no source on vllm: state %q ready %v", got.Status.State, got.Status.Ready)
	}

	ollamaSrc := vllmModelCR("m3", vllmModel14)
	ollamaSrc.Spec.Source = &aiv1alpha1.ModelSource{Ollama: &aiv1alpha1.OllamaSource{Tag: vllmModel14}}
	got = reconcileVLLMModelCR(t, ollamaSrc, provider.DeepCopy())
	if got.Status.State != stateError || got.Status.Ready {
		t.Errorf("ollama source on vllm: state %q ready %v", got.Status.State, got.Status.Ready)
	}
}

// An agent bound to a Model on a vllm provider gets that Model's own server as
// its endpoint and the served name as its model.
func TestAgentOnAVLLMModelGetsTheModelsServerAndServedName(t *testing.T) {
	scheme := agentReconcileScheme(t)
	provider := vllmProvider(server(vllmModel14, vllmEndpoint14), server(vllmModel8, "http://vllm-8b.models:8000"))
	provider.Namespace = "ns"
	provider.Status.Ready = true
	model := vllmModelCR("fourteen", vllmModel14)
	model.Namespace = "ns"
	model.Status.Ready = true
	objs := []client.Object{provider, model}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := &AgentReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	agent := &aiv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: testK8sAgent, Namespace: "ns"}}

	pick, err := r.pickModel(context.Background(), agent, testMulling)
	if err != nil {
		t.Fatalf("pickModel: %v", err)
	}
	if pick.ModelID != vllmModel14 || pick.Endpoint != vllmEndpoint14 {
		t.Errorf("pick = %q at %q, want the served name at the model's own server", pick.ModelID, pick.Endpoint)
	}
	env := r.buildEnvVars(context.Background(), agent, pick, pick, 8080)
	if v, _ := envValue(env, "KUBEMOOT_MODEL_ENDPOINT"); v != vllmEndpoint14 {
		t.Errorf("KUBEMOOT_MODEL_ENDPOINT = %q", v)
	}
	if v, _ := envValue(env, "KUBEMOOT_MODEL_MODEL"); v != vllmModel14 {
		t.Errorf("KUBEMOOT_MODEL_MODEL = %q", v)
	}
}
