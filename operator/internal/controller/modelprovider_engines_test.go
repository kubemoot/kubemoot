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
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

func TestProbeEngineKeepsTheSnapshotForThePublish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ps":
			_, _ = w.Write([]byte(`{"models":[{"name":"m:14b","size_vram":1048576}]}`))
		case testPathAPITags:
			_, _ = w.Write([]byte(`{"models":[{"name":"m:14b","size":2097152},{"name":"o:8b","size":1048576}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	provider := &aiv1alpha1.ModelProvider{
		Spec:   aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
		Status: aiv1alpha1.ModelProviderStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}},
	}
	provider.Name = "p"
	r := &ModelProviderReconciler{}
	r.probeEngine(context.Background(), provider, srv.Client())

	stored, ok := r.probes.Load("p")
	if !ok {
		t.Fatal("the snapshot must be kept for the state publish")
	}
	state := buildProviderState(provider)
	applySnapshotToState(&state, stored.(engine.Snapshot))
	if state.ModelStates["m:14b"] != "awake" || state.ModelStates["o:8b"] != "available" {
		t.Errorf("ModelStates = %v", state.ModelStates)
	}
	if state.RequestsRunning != nil || state.RequestsWaiting != nil || state.KVCacheUsage != nil {
		t.Errorf("Ollama leaves the request counts and cache use absent: %+v", state)
	}
}

func TestProbeEngineUnknownTypeLeavesCapacityAlone(t *testing.T) {
	provider := &aiv1alpha1.ModelProvider{
		Spec: aiv1alpha1.ModelProviderSpec{Type: "bogus", Endpoint: "http://x"},
		Status: aiv1alpha1.ModelProviderStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{
			LoadedModels: []aiv1alpha1.LoadedModel{{Name: "earlier-model"}},
		}},
	}
	provider.Name = "p"
	r := &ModelProviderReconciler{}
	r.probeEngine(context.Background(), provider, http.DefaultClient)
	if len(provider.Status.Capacity.LoadedModels) != 1 {
		t.Error("an unknown type must not touch the capacity")
	}
	if _, ok := r.probes.Load("p"); ok {
		t.Error("an unknown type keeps no snapshot")
	}
}

func TestProbeEngineProviderThatDoesNotAnswerKeepsEarlierCapacity(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	provider := &aiv1alpha1.ModelProvider{
		Spec: aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: url},
		Status: aiv1alpha1.ModelProviderStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{
			LoadedModels: []aiv1alpha1.LoadedModel{{Name: "earlier-model"}},
		}},
	}
	provider.Name = "p"
	r := &ModelProviderReconciler{}
	r.probeEngine(context.Background(), provider, http.DefaultClient)
	if len(provider.Status.Capacity.LoadedModels) != 1 {
		t.Error("a probe that failed must leave the earlier capacity")
	}
	if _, ok := r.probes.Load("p"); ok {
		t.Error("a failed probe keeps no snapshot")
	}
}

func TestApplySnapshotToStateCarriesEveryContractField(t *testing.T) {
	running, waiting, cache, parked := 2, 5, 0.75, int64(4096)
	state := ProviderState{Name: "p"}
	applySnapshotToState(&state, engine.Snapshot{
		ModelStates:         map[string]engine.ModelState{"a": engine.StateAsleep},
		RequestsRunning:     &running,
		RequestsWaiting:     &waiting,
		KVCacheUsage:        &cache,
		SwitchCostMs:        map[string]int64{"a": 900},
		HostMemoryParkedMiB: &parked,
	})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	// The published state must decode into the generated contract type with no
	// field the schema lacks.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var contract bus.ProviderState
	if err := dec.Decode(&contract); err != nil {
		t.Fatalf("state does not match the contract: %v\n%s", err, raw)
	}
	if contract.ModelStates["a"] != stateAsleepName || *contract.RequestsRunning != 2 || *contract.RequestsWaiting != 5 ||
		*contract.KvCacheUsage != 0.75 || contract.SwitchCostMs["a"] != 900 || *contract.HostMemoryParkedMiB != 4096 {
		t.Errorf("contract = %+v", contract)
	}
}

func TestApplySnapshotToStateOmitsWhatTheEngineDoesNotReport(t *testing.T) {
	state := ProviderState{Name: "p"}
	applySnapshotToState(&state, engine.Snapshot{})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"modelStates", "requestsRunning", "requestsWaiting", "kvCacheUsage", "switchCostMs", "hostMemoryParkedMiB"} {
		if bytes.Contains(raw, []byte(field)) {
			t.Errorf("%s must be absent: %s", field, raw)
		}
	}
}

// A snapshot from an earlier reconcile is not published once a later reconcile
// finds the provider not ready.
const silentProvider = "silent-provider"

func TestReconcileDropsTheEarlierSnapshotWhenTheProviderStopsAnswering(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: silentProvider, Namespace: testNS1},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: url},
	}
	scheme := agentReconcileScheme(t)
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(provider).WithStatusSubresource(provider).Build()
	r := &ModelProviderReconciler{Client: cli, Scheme: scheme, ConfigCache: NewConfigCache()}
	r.probes.Store(silentProvider, engine.Snapshot{ModelStates: map[string]engine.ModelState{"m": engine.StateAwake}})

	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: silentProvider, Namespace: testNS1}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.probes.Load(silentProvider); ok {
		t.Error("a provider that stopped answering must not keep its earlier snapshot")
	}
}

// A reported 0 is not the same as not reported: it must survive the published
// JSON and the generated contract type.
func TestZeroEngineValuesSurviveTheContract(t *testing.T) {
	zeroInt, zeroInt64, zeroCache := 0, int64(0), 0.0
	state := ProviderState{Name: "p"}
	applySnapshotToState(&state, engine.Snapshot{
		RequestsRunning: &zeroInt, RequestsWaiting: &zeroInt, KVCacheUsage: &zeroCache,
		HostMemoryParkedMiB: &zeroInt64, SwitchCostMs: map[string]int64{"a": 0},
	})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var contract bus.ProviderState
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	assertZerosKept(t, raw, contract)
}

// assertZerosKept checks every optional number a snapshot reported as 0 is still
// present in the decoded contract and in its re-encoding.
func assertZerosKept(t *testing.T, raw []byte, contract bus.ProviderState) {
	t.Helper()
	if contract.RequestsRunning == nil || contract.RequestsWaiting == nil || contract.KvCacheUsage == nil ||
		contract.HostMemoryParkedMiB == nil {
		t.Fatalf("a reported 0 became absent: %s", raw)
	}
	if v, ok := contract.SwitchCostMs["a"]; !ok || v != 0 {
		t.Errorf("a 0 switch cost must stay in the map: %s", raw)
	}
	again, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requestsRunning", "requestsWaiting", "kvCacheUsage", "hostMemoryParkedMiB"} {
		if !bytes.Contains(again, []byte(`"`+field+`":0`)) {
			t.Errorf("%s: 0 lost on re-encode: %s", field, again)
		}
	}
}

func TestIsModelLoadedReadsTheAdapterAndKeepsItsLeniency(t *testing.T) {
	ps := "{\"models\":[{\"name\":\"m:14b\"}]}"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(ps))
	}))
	defer srv.Close()
	r := &ModelReconciler{}
	ctx := context.Background()
	if ok, err := r.isModelLoaded(ctx, srv.Client(), srv.URL, "m:14b"); err != nil || !ok {
		t.Errorf("loaded model: ok=%v err=%v", ok, err)
	}
	if ok, err := r.isModelLoaded(ctx, srv.Client(), srv.URL, "other"); err != nil || ok {
		t.Errorf("other model: ok=%v err=%v", ok, err)
	}
}

func TestIsModelLoadedCountsAnUnusableAnswerAsNotLoaded(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"no /api/ps": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"unreadable": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("garbled")) },
	} {
		srv := httptest.NewServer(handler)
		ok, err := (&ModelReconciler{}).isModelLoaded(context.Background(), srv.Client(), srv.URL, "m:14b")
		srv.Close()
		if err != nil || ok {
			t.Errorf("%s: ok=%v err=%v, want not loaded and no error", name, ok, err)
		}
	}
}

func TestIsModelLoadedFailsWhenTheServerIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if _, err := (&ModelReconciler{}).isModelLoaded(context.Background(), srv.Client(), srv.URL, "m:14b"); err == nil {
		t.Error("an unreachable server is an error")
	}
}
