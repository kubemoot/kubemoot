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
	"net/http"
	"testing"

	"github.com/go-logr/logr"
	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func capacityScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		aiv1alpha1.AddToScheme, corev1.AddToScheme, discoveryv1.AddToScheme,
	} {
		if err := add(s); err != nil {
			t.Fatalf("add to scheme: %v", err)
		}
	}
	return s
}

// discoverCapacity resolves the backing pod via EndpointSlice, reads its node
// name and OLLAMA_NUM_PARALLEL. The /api/ps + /api/tags calls hit the (dead)
// provider endpoint and return fast; DCGM is skipped (no scheduler config).
func TestDiscoverCapacity_FromPod(t *testing.T) {
	scheme := capacityScheme(t)
	slice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "ollama-abc", Namespace: "rig0",
			Labels: map[string]string{"kubernetes.io/service-name": "ollama"},
		},
		Endpoints: []discoveryv1.Endpoint{{
			TargetRef: &corev1.ObjectReference{Kind: "Pod", Name: "ollama-0", Namespace: "rig0"},
		}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-0", Namespace: "rig0"},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node-1",
			Containers: []corev1.Container{{
				Name: "ollama",
				Env:  []corev1.EnvVar{{Name: "OLLAMA_NUM_PARALLEL", Value: "4"}},
			}},
		},
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(slice, pod).Build()
	r := &ModelProviderReconciler{Client: cli, ConfigCache: NewConfigCache()}
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "prov", Namespace: "rig0"},
		Spec:       aiv1alpha1.ModelProviderSpec{Endpoint: "http://ollama.rig0:11434"},
	}
	ctx := logf.IntoContext(context.Background(), logr.Discard())
	r.discoverCapacity(ctx, provider, &http.Client{})

	if provider.Status.Capacity == nil {
		t.Fatal("discoverCapacity should initialise Capacity")
	}
	if provider.Status.Capacity.NodeName != "gpu-node-1" {
		t.Errorf("NodeName = %q, want gpu-node-1", provider.Status.Capacity.NodeName)
	}
	if provider.Status.Capacity.MaxParallel != 4 {
		t.Errorf("MaxParallel = %d, want 4 (from OLLAMA_NUM_PARALLEL)", provider.Status.Capacity.MaxParallel)
	}
	if provider.Status.Capacity.LastProbed == nil {
		t.Error("discoverCapacity should stamp LastProbed")
	}
}

func TestApplyCapacityToState(t *testing.T) {
	state := &ProviderState{}
	cap := &aiv1alpha1.DiscoveredCapacity{
		MaxParallel:  8,
		VRAMTotalMiB: 32768,
		LoadedModels: []aiv1alpha1.LoadedModel{{Name: "qwen3:32b", SizeVRAM: 2147483648}}, // 2 GiB
		AvailableModels: []aiv1alpha1.AvailableModel{
			{Name: "qwen3:32b", SizeBytes: 9999},            // already loaded -> excluded
			{Name: "llama3:8b", SizeBytes: 4 * 1024 * 1024}, // 4 MiB on disk
		},
	}
	applyCapacityToState(state, cap)

	if state.MaxParallel != 8 || state.TotalVramMiB != 32768 {
		t.Errorf("scalar fields not mapped: %+v", state)
	}
	if state.ActiveCount != 0 || state.QueueDepth != 0 {
		t.Errorf("v1 saturation fields should be zeroed, got active=%d queue=%d", state.ActiveCount, state.QueueDepth)
	}
	if got := state.LoadedModelFootprintsMiB["qwen3:32b"]; got != 2048 {
		t.Errorf("loaded footprint = %d MiB, want 2048", got)
	}
	// llama3:8b is available-not-loaded -> in the available map; qwen3 is loaded -> excluded.
	if _, loadedInAvail := state.AvailableModelFootprintsMiB["qwen3:32b"]; loadedInAvail {
		t.Error("a loaded model must not appear in the available footprints")
	}
	if got := state.AvailableModelFootprintsMiB["llama3:8b"]; got != 4 {
		t.Errorf("available footprint = %d MiB, want 4", got)
	}
}

func TestAvailableFootprints(t *testing.T) {
	// Empty input -> nil.
	if got := availableFootprints(nil, nil); got != nil {
		t.Errorf("no available models should yield nil, got %v", got)
	}
	// All available already loaded -> nil (match the nil-when-empty contract).
	loaded := map[string]int64{"a": 1}
	only := []aiv1alpha1.AvailableModel{{Name: "a", SizeBytes: 5}}
	if got := availableFootprints(loaded, only); got != nil {
		t.Errorf("all-loaded should yield nil, got %v", got)
	}
	// A not-yet-loaded model is included, converted to MiB.
	mix := []aiv1alpha1.AvailableModel{{Name: "a", SizeBytes: 5}, {Name: "b", SizeBytes: 3 * 1024 * 1024}}
	got := availableFootprints(loaded, mix)
	if len(got) != 1 || got["b"] != 3 {
		t.Errorf("availableFootprints = %v, want {b:3}", got)
	}
}
