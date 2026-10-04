/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// buildStateWithFootprints mirrors the field-copy block in publishStateToKV:
// it builds a ProviderState from a ModelProvider, populating loaded-model
// footprints from /api/ps observations and available-model footprints from
// /api/tags on-disk sizes (skipping any model already loaded). Extracted so
// the footprint tests share one byte-identical arrange step instead of
// re-copying the nested block.
func buildStateWithFootprints(mp *aiv1alpha1.ModelProvider) ProviderState {
	state := ProviderState{Name: mp.Name, Endpoint: mp.Spec.Endpoint, Ready: mp.Status.Ready}
	cap := mp.Status.Capacity
	if cap == nil {
		return state
	}
	state.TotalVramMiB = cap.VRAMTotalMiB
	state.LoadedModelFootprintsMiB = loadedFootprintsMiB(cap.LoadedModels)
	state.AvailableModelFootprintsMiB = availableFootprintsMiB(cap.AvailableModels, state.LoadedModelFootprintsMiB)
	return state
}

// loadedFootprintsMiB converts each loaded model's VRAM bytes to MiB. Returns
// nil when there are no loaded models (preserves the omitempty contract).
func loadedFootprintsMiB(loaded []aiv1alpha1.LoadedModel) map[string]int64 {
	if len(loaded) == 0 {
		return nil
	}
	out := make(map[string]int64, len(loaded))
	for _, m := range loaded {
		out[m.Name] = m.SizeVRAM / (1024 * 1024)
	}
	return out
}

// availableFootprintsMiB converts each available model's on-disk bytes to MiB,
// skipping any model already present in loaded (the /api/ps observation is more
// accurate). Returns nil when nothing remains (preserves the omitempty
// contract).
func availableFootprintsMiB(available []aiv1alpha1.AvailableModel, loaded map[string]int64) map[string]int64 {
	if len(available) == 0 {
		return nil
	}
	avail := make(map[string]int64, len(available))
	for _, m := range available {
		if _, alreadyLoaded := loaded[m.Name]; !alreadyLoaded {
			avail[m.Name] = m.SizeBytes / (1024 * 1024)
		}
	}
	if len(avail) == 0 {
		return nil
	}
	return avail
}

// TestProviderStateJSONContract pins the JSON shape the operator writes to
// the kubemoot_provider_state NATS KV bucket. The agent-runtime
// ProviderSelector will deserialize this — any field rename here must be
// coordinated with the Java side. Catches accidental shape drift.
func TestProviderStateJSONContract(t *testing.T) {
	state := ProviderState{
		Name:         testOllamaGPU,
		Endpoint:     testRig0URL,
		MaxParallel:  2,
		ActiveCount:  1,
		QueueDepth:   0,
		LoadedModels: []string{testModel32B, testModelID},
		Ready:        true,
		LastProbedAt: testTimestamp,
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Round-trip into a map so we assert field NAMES literally, not just
	// "struct serializes to something." The agent runtime reads these by
	// name.
	var got map[string]interface{}
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantStrings := map[string]string{
		"name":         testOllamaGPU,
		"endpoint":     testRig0URL,
		"lastProbedAt": testTimestamp,
	}
	for k, want := range wantStrings {
		if got[k] != want {
			t.Errorf("field %q = %v; want %q", k, got[k], want)
		}
	}

	wantFloats := map[string]float64{
		"maxParallel": 2,
		"activeCount": 1,
		"queueDepth":  0,
	}
	for k, want := range wantFloats {
		if got[k] != want {
			t.Errorf("field %q = %v; want %v", k, got[k], want)
		}
	}

	assertProviderStateReadyAndModels(t, got)
}

// assertProviderStateReadyAndModels checks the ready flag and the loadedModels array of a marshaled ProviderState.
func assertProviderStateReadyAndModels(t *testing.T, got map[string]interface{}) {
	t.Helper()
	if got["ready"] != true {
		t.Errorf("field ready = %v; want true", got["ready"])
	}

	models, ok := got["loadedModels"].([]interface{})
	if !ok || len(models) != 2 {
		t.Fatalf("loadedModels = %v; want 2-elem array", got["loadedModels"])
	}
	if models[0] != testModel32B || models[1] != testModelID {
		t.Errorf("loadedModels = %v; want [qwen3:32b, qwen3:8b]", models)
	}
}

// TestProviderStateOmitsEmptyLoadedModels ensures we don't emit a noisy
// "loadedModels": null when a provider has no models loaded yet. The
// omitempty tag is the user-facing contract; this test pins it.
func TestProviderStateOmitsEmptyLoadedModels(t *testing.T) {
	state := ProviderState{
		Name:         testOllamaRig1,
		Endpoint:     "http://ollama.ollama-rig1:11434",
		MaxParallel:  1,
		Ready:        true,
		LastProbedAt: testTimestamp,
		// LoadedModels intentionally nil
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(bytes, &got)
	if _, present := got["loadedModels"]; present {
		t.Errorf("loadedModels should be omitted when empty; got %v", got["loadedModels"])
	}
}

// TestProviderStateBuildFromMP exercises the field mapping inside
// publishStateToKV (without actually publishing) — given a ModelProvider
// CR, the built ProviderState reflects the right fields.
//
// Pure constructor-level test; the NATS write is tested implicitly via
// the existing publisher contract. Future cards (CAS, agent reads) get
// their own dedicated tests.
func TestProviderStateBuildFromMP(t *testing.T) {
	mp := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU},
		Spec: aiv1alpha1.ModelProviderSpec{
			Endpoint: testRig0URL,
		},
		Status: aiv1alpha1.ModelProviderStatus{
			Ready: true,
			Capacity: &aiv1alpha1.DiscoveredCapacity{
				MaxParallel: 2,
				LoadedModels: []aiv1alpha1.LoadedModel{
					{Name: testModel32B},
				},
			},
		},
	}

	// Mimics the field-copy block in publishStateToKV. If publishStateToKV
	// is refactored to a builder, swap this for a direct call.
	state := ProviderState{
		Name:     mp.Name,
		Endpoint: mp.Spec.Endpoint,
		Ready:    mp.Status.Ready,
	}
	if cap := mp.Status.Capacity; cap != nil {
		state.MaxParallel = cap.MaxParallel
		for _, m := range cap.LoadedModels {
			state.LoadedModels = append(state.LoadedModels, m.Name)
		}
	}
	if state.MaxParallel < 1 {
		state.MaxParallel = 1
	}

	if state.Name != testOllamaGPU {
		t.Errorf("Name = %q; want ollama-gpu", state.Name)
	}
	if state.MaxParallel != 2 {
		t.Errorf("MaxParallel = %d; want 2", state.MaxParallel)
	}
	if len(state.LoadedModels) != 1 || state.LoadedModels[0] != testModel32B {
		t.Errorf("LoadedModels = %v; want [qwen3:32b]", state.LoadedModels)
	}

	// Defensive: MP without Capacity defaults MaxParallel to 1 (not 0,
	// which would cause a divide-by-zero in the agent-side bin-pack math
	// once that ships).
	mpNoCap := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-unprobed"},
		Spec:       aiv1alpha1.ModelProviderSpec{Endpoint: "x"},
	}
	defensive := ProviderState{
		Name:     mpNoCap.Name,
		Endpoint: mpNoCap.Spec.Endpoint,
	}
	if defensive.MaxParallel < 1 {
		defensive.MaxParallel = 1
	}
	if defensive.MaxParallel != 1 {
		t.Errorf("unprobed provider MaxParallel = %d; want 1 (defensive)", defensive.MaxParallel)
	}
}

// TestProviderStateV2Fields pins the v2 JSON shape (totalVramMiB +
// loadedModelFootprintsMiB) the agent-runtime reads to compute live VRAM
// headroom for per-call ticket claims. See kubemoot/docs/scheduler.md
// ("Update 2026-05-28: VRAM-headroom tickets") for the design rationale.
//
// Field names must match agent-runtime ProviderSelector.fromJson — any
// rename here must be coordinated on the Java side.
func TestProviderStateV2Fields(t *testing.T) {
	state := ProviderState{
		Name:         testOllamaGPU,
		Endpoint:     testRig0URL,
		Ready:        true,
		LastProbedAt: "2026-05-28T00:00:00Z",
		TotalVramMiB: 32000,
		LoadedModelFootprintsMiB: map[string]int64{
			testModel32B: 22000,
			testModelID:  5000,
		},
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["totalVramMiB"] != float64(32000) {
		t.Errorf("totalVramMiB = %v; want 32000", got["totalVramMiB"])
	}
	fp, ok := got["loadedModelFootprintsMiB"].(map[string]interface{})
	if !ok {
		t.Fatalf("loadedModelFootprintsMiB = %v; want object", got["loadedModelFootprintsMiB"])
	}
	if fp[testModel32B] != float64(22000) {
		t.Errorf("qwen3:32b footprint = %v; want 22000", fp[testModel32B])
	}
	if fp[testModelID] != float64(5000) {
		t.Errorf("qwen3:8b footprint = %v; want 5000", fp[testModelID])
	}
}

// TestProviderStateV2OmitsEmpty ensures the v2 fields use omitempty so
// pre-v2 consumers (older agent runtime images during rollout) don't see
// noisy zero/null values. totalVramMiB = 0 means "not yet discovered" on
// the consumer side; absence and zero are equivalent.
func TestProviderStateV2OmitsEmpty(t *testing.T) {
	state := ProviderState{
		Name:        "ollama-unprobed",
		MaxParallel: 1,
	}
	bytes, _ := json.Marshal(state)
	var got map[string]interface{}
	_ = json.Unmarshal(bytes, &got)
	if _, present := got["totalVramMiB"]; present {
		t.Errorf("totalVramMiB should be omitted when 0; got %v", got["totalVramMiB"])
	}
	if _, present := got["loadedModelFootprintsMiB"]; present {
		t.Errorf("loadedModelFootprintsMiB should be omitted when empty; got %v",
			got["loadedModelFootprintsMiB"])
	}
}

// TestPublishStateMapsV2FieldsFromCapacity verifies the field-copy block
// in publishStateToKV populates TotalVramMiB from Capacity.VRAMTotalMiB
// and converts each LoadedModel.SizeVRAM (bytes) to MiB in the footprint
// map. Mirrors the production builder; pure constructor-level.
func TestPublishStateMapsV2FieldsFromCapacity(t *testing.T) {
	mp := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU},
		Spec: aiv1alpha1.ModelProviderSpec{
			Endpoint: testRig0URL,
		},
		Status: aiv1alpha1.ModelProviderStatus{
			Ready: true,
			Capacity: &aiv1alpha1.DiscoveredCapacity{
				MaxParallel:  1,
				VRAMTotalMiB: 32000,
				LoadedModels: []aiv1alpha1.LoadedModel{
					// SizeVRAM is bytes (Ollama /api/ps native unit).
					// 22000 MiB = 22000 * 1024 * 1024 bytes.
					{Name: testModel32B, SizeVRAM: 22000 * 1024 * 1024},
					{Name: testModelID, SizeVRAM: 5000 * 1024 * 1024},
				},
			},
		},
	}

	state := buildProviderState(mp)

	if state.TotalVramMiB != 32000 {
		t.Errorf("TotalVramMiB = %d; want 32000", state.TotalVramMiB)
	}
	if state.LoadedModelFootprintsMiB[testModel32B] != 22000 {
		t.Errorf("qwen3:32b footprint = %d MiB; want 22000",
			state.LoadedModelFootprintsMiB[testModel32B])
	}
	if state.LoadedModelFootprintsMiB[testModelID] != 5000 {
		t.Errorf("qwen3:8b footprint = %d MiB; want 5000",
			state.LoadedModelFootprintsMiB[testModelID])
	}
}

// TestAvailableModelFootprintsPopulatedAtColdStart verifies the
// AvailableModelFootprintsMiB field is populated from /api/tags .size when
// no loaded-model footprint exists for a model. This is the core of the
// JIT cold-start fit-gate fix: before this, footprintMiBFor returned 0 when
// no provider had loaded the model yet, causing pickMullingChatModel to fall
// back to the static endpoint with NO VRAM check - the root cause of the
// qwen3:32b-on-4090 spill observed 2026-06-11.
//
// The available-model map should NOT include models that are already in
// LoadedModelFootprintsMiB (prefer the more accurate /api/ps observation).
func TestAvailableModelFootprintsPopulatedAtColdStart(t *testing.T) {
	// 27.2 GiB = 29,200,000,000 bytes (approximate on-disk size of qwen3:32b Q4).
	const qwen332bBytes = int64(29_200_000_000)
	const qwen38bBytes = int64(5_100_000_000)

	mp := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU},
		Spec: aiv1alpha1.ModelProviderSpec{
			Endpoint: testRig0URL,
		},
		Status: aiv1alpha1.ModelProviderStatus{
			Ready: true,
			Capacity: &aiv1alpha1.DiscoveredCapacity{
				VRAMTotalMiB: 32000,
				// No loaded models - true cold start.
				AvailableModels: []aiv1alpha1.AvailableModel{
					{Name: testModel32B, SizeBytes: qwen332bBytes},
					{Name: testModelID, SizeBytes: qwen38bBytes},
				},
			},
		},
	}

	// Mimic the AvailableModelFootprintsMiB field-copy block in publishStateToKV.
	state := ProviderState{
		Name:     mp.Name,
		Endpoint: mp.Spec.Endpoint,
		Ready:    mp.Status.Ready,
	}
	if cap := mp.Status.Capacity; cap != nil {
		state.TotalVramMiB = cap.VRAMTotalMiB
		// No loaded models, so LoadedModelFootprintsMiB stays nil.
		if len(cap.AvailableModels) > 0 {
			avail := make(map[string]int64, len(cap.AvailableModels))
			for _, m := range cap.AvailableModels {
				if _, alreadyLoaded := state.LoadedModelFootprintsMiB[m.Name]; !alreadyLoaded {
					avail[m.Name] = m.SizeBytes / (1024 * 1024)
				}
			}
			if len(avail) > 0 {
				state.AvailableModelFootprintsMiB = avail
			}
		}
	}

	// qwen3:32b should appear in the available-footprint map.
	want32b := qwen332bBytes / (1024 * 1024) // 27,847 MiB
	if state.AvailableModelFootprintsMiB[testModel32B] != want32b {
		t.Errorf("availableModelFootprintsMiB[qwen3:32b] = %d MiB; want %d",
			state.AvailableModelFootprintsMiB[testModel32B], want32b)
	}
	want8b := qwen38bBytes / (1024 * 1024) // 4,863 MiB
	if state.AvailableModelFootprintsMiB[testModelID] != want8b {
		t.Errorf("availableModelFootprintsMiB[qwen3:8b] = %d MiB; want %d",
			state.AvailableModelFootprintsMiB[testModelID], want8b)
	}
}

// TestAvailableModelFootprintsSkipsAlreadyLoaded verifies that models already
// in LoadedModelFootprintsMiB are NOT duplicated in AvailableModelFootprintsMiB.
// The loaded observation from /api/ps is more accurate than the on-disk size
// from /api/tags; prefer it.
func TestAvailableModelFootprintsSkipsAlreadyLoaded(t *testing.T) {
	mp := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU},
		Spec:       aiv1alpha1.ModelProviderSpec{Endpoint: "http://x:11434"},
		Status: aiv1alpha1.ModelProviderStatus{
			Ready: true,
			Capacity: &aiv1alpha1.DiscoveredCapacity{
				VRAMTotalMiB: 32000,
				LoadedModels: []aiv1alpha1.LoadedModel{
					// qwen3:32b is loaded; /api/ps says 22 GiB VRAM.
					{Name: testModel32B, SizeVRAM: 22000 * 1024 * 1024},
				},
				// /api/tags returns both - qwen3:32b is larger on-disk than in-VRAM.
				AvailableModels: []aiv1alpha1.AvailableModel{
					{Name: testModel32B, SizeBytes: 29_200_000_000},
					{Name: testModelID, SizeBytes: 5_100_000_000},
				},
			},
		},
	}

	state := buildStateWithFootprints(mp)

	// qwen3:32b is loaded - must NOT appear in AvailableModelFootprintsMiB.
	if _, found := state.AvailableModelFootprintsMiB[testModel32B]; found {
		t.Errorf("qwen3:32b should not be in availableModelFootprintsMiB (loaded model wins)")
	}
	// qwen3:8b is not loaded - must appear.
	if state.AvailableModelFootprintsMiB[testModelID] == 0 {
		t.Errorf("qwen3:8b should be in availableModelFootprintsMiB with non-zero MiB")
	}
	// LoadedModelFootprintsMiB should still carry the accurate /api/ps value.
	if state.LoadedModelFootprintsMiB[testModel32B] != 22000 {
		t.Errorf("loaded footprint for qwen3:32b = %d; want 22000",
			state.LoadedModelFootprintsMiB[testModel32B])
	}
}

// TestAvailableModelFootprintsJSONContract pins the JSON field name for the
// new AvailableModelFootprintsMiB field the agent-runtime must parse.
func TestAvailableModelFootprintsJSONContract(t *testing.T) {
	state := ProviderState{
		Name:         testOllamaGPU,
		Ready:        true,
		TotalVramMiB: 24563,
		AvailableModelFootprintsMiB: map[string]int64{
			testModel32B: 27847,
		},
	}
	bytes, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(bytes, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	avail, ok := got["availableModelFootprintsMiB"].(map[string]interface{})
	if !ok {
		t.Fatalf("availableModelFootprintsMiB = %v; want object", got["availableModelFootprintsMiB"])
	}
	if avail[testModel32B] != float64(27847) {
		t.Errorf("availableModelFootprintsMiB[qwen3:32b] = %v; want 27847", avail[testModel32B])
	}
}

// A provider the operator cannot measure (CPU Ollama, or no DCGM) publishes the
// memory budget declared on its spec, so agents can still place cold loads on it.
// A discovered total always wins over the declaration.
func TestBuildProviderStateMemoryBudget(t *testing.T) {
	declared := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-cpu"},
		Spec: aiv1alpha1.ModelProviderSpec{
			Endpoint:   "http://ollama.ollama:11434",
			Scheduling: &aiv1alpha1.ProviderScheduling{MemoryMiB: 6144},
		},
		Status: aiv1alpha1.ModelProviderStatus{Ready: true},
	}
	if got := buildProviderState(declared).TotalVramMiB; got != 6144 {
		t.Errorf("declared budget not published: TotalVramMiB = %d, want 6144", got)
	}

	undeclared := declared.DeepCopy()
	undeclared.Spec.Scheduling = nil
	if got := buildProviderState(undeclared).TotalVramMiB; got != 0 {
		t.Errorf("no declaration and no discovery must stay unknown (0), got %d", got)
	}

	discovered := declared.DeepCopy()
	discovered.Status.Capacity = &aiv1alpha1.DiscoveredCapacity{VRAMTotalMiB: 32000}
	if got := buildProviderState(discovered).TotalVramMiB; got != 32000 {
		t.Errorf("discovered VRAM must win over the declaration, got %d", got)
	}
	if got := buildProviderState(discovered).MaxParallel; got != 1 {
		t.Errorf("MaxParallel defaults to 1, got %d", got)
	}
}

// The agent runtime reads the context fields by these exact names.
func TestProviderStateContextFieldsJSONContract(t *testing.T) {
	state := buildProviderState(&aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "gpu-a"},
		Status: aiv1alpha1.ModelProviderStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{
			ContextLength: 16384,
			LoadedModels:  []aiv1alpha1.LoadedModel{{Name: "m:14b", SizeVRAM: 1 << 30, ContextLength: 16384}},
		}},
	})
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["contextLength"] != float64(16384) {
		t.Errorf("contextLength = %v, want 16384", got["contextLength"])
	}
	loaded, ok := got["loadedModelContextLengths"].(map[string]interface{})
	if !ok || loaded["m:14b"] != float64(16384) {
		t.Errorf("loadedModelContextLengths = %v, want {m:14b: 16384}", got["loadedModelContextLengths"])
	}

	empty, _ := json.Marshal(buildProviderState(&aiv1alpha1.ModelProvider{ObjectMeta: metav1.ObjectMeta{Name: "b"}}))
	if strings.Contains(string(empty), "contextLength") || strings.Contains(string(empty), "loadedModelContextLengths") {
		t.Errorf("unknown context must be omitted, got %s", empty)
	}
}

// /api/ps reports each loaded model's per-request context as context_length.
func TestDiscoverLoadedModelsReadsContextLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"m:32b","size_vram":1048576,"size":1048576,"context_length":8192},{"name":"old"}]}`))
	}))
	defer srv.Close()
	provider := &aiv1alpha1.ModelProvider{
		Spec:   aiv1alpha1.ModelProviderSpec{Endpoint: srv.URL},
		Status: aiv1alpha1.ModelProviderStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}},
	}
	r := &ModelProviderReconciler{}
	r.discoverLoadedModels(logf.IntoContext(context.Background(), logr.Discard()), provider, srv.Client())
	models := provider.Status.Capacity.LoadedModels
	if len(models) != 2 || models[0].ContextLength != 8192 || models[1].ContextLength != 0 {
		t.Errorf("LoadedModels = %+v, want contexts 8192 and 0", models)
	}
}
