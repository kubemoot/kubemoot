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
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

func TestQueryPrometheusScalar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"24576"]}]}}`))
	}))
	defer srv.Close()
	r := &ModelServerReconciler{}
	got := r.queryPrometheusScalar(context.Background(), srv.Client(), srv.URL, "up")
	if got != 24576 {
		t.Errorf("queryPrometheusScalar = %v, want 24576", got)
	}

	// A non-200 response yields 0, not an error.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if got := r.queryPrometheusScalar(context.Background(), bad.Client(), bad.URL, "up"); got != 0 {
		t.Errorf("a 500 should yield 0, got %v", got)
	}
}

func TestQueryPrometheusLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"modelName":"NVIDIA RTX 5090"},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()
	r := &ModelServerReconciler{}
	got := r.queryPrometheusLabel(context.Background(), srv.Client(), srv.URL, "DCGM_FI_DEV_GPU_TEMP", "modelName")
	if got != "NVIDIA RTX 5090" {
		t.Errorf("queryPrometheusLabel = %q, want the GPU model", got)
	}
}

func TestQueryDCGMMetrics(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if strings.Contains(q, "FB_FREE") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{},"value":[1,"32768"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"metric":{"modelName":"RTX 5090"},"value":[1,"1"]}]}}`))
	}))
	defer srv.Close()
	r := &ModelServerReconciler{}
	server := &aiv1alpha1.ModelServer{Status: aiv1alpha1.ModelServerStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}}}
	// queryDCGMMetrics logs on the success path; seed a logger so V(1).Info
	// does not panic outside the envtest suite (which calls SetLogger).
	ctx := logf.IntoContext(context.Background(), logr.Discard())
	r.queryDCGMMetrics(ctx, srv.Client(), srv.URL, "node-1", server)
	if server.Status.Capacity.VRAMTotalMiB != 32768 {
		t.Errorf("VRAMTotalMiB = %d, want 32768", server.Status.Capacity.VRAMTotalMiB)
	}
	if server.Status.Capacity.GPUModel != "RTX 5090" {
		t.Errorf("GPUModel = %q, want RTX 5090", server.Status.Capacity.GPUModel)
	}
}

func TestDiscoverLoadedModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// size_vram is in bytes; 2 GiB -> 2048 MiB.
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:32b","size_vram":2147483648,"size":3000}]}`))
	}))
	defer srv.Close()
	r := &ModelServerReconciler{}
	server := &aiv1alpha1.ModelServer{Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama, Endpoint: srv.URL}, Status: aiv1alpha1.ModelServerStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}}}
	r.probeEngine(context.Background(), server, srv.Client())
	if len(server.Status.Capacity.LoadedModels) != 1 || server.Status.Capacity.LoadedModels[0].Name != testModel32B {
		t.Fatalf("LoadedModels not populated: %+v", server.Status.Capacity.LoadedModels)
	}
	if server.Status.Capacity.VRAMUsedMiB != 2048 {
		t.Errorf("VRAMUsedMiB = %d, want 2048", server.Status.Capacity.VRAMUsedMiB)
	}
}

func TestDiscoverAvailableModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testPathAPITags {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3:8b","size":4096},{"name":"qwen3:32b","size":20480}]}`))
	}))
	defer srv.Close()
	r := &ModelServerReconciler{}
	server := &aiv1alpha1.ModelServer{Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeOllama, Endpoint: srv.URL}, Status: aiv1alpha1.ModelServerStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}}}
	r.probeEngine(context.Background(), server, srv.Client())
	if len(server.Status.Capacity.AvailableModels) != 2 {
		t.Fatalf("AvailableModels = %+v, want 2", server.Status.Capacity.AvailableModels)
	}
}

func TestCountAssignedAgents(t *testing.T) {
	scheme := gatewayScheme(t)
	dep := func(name string, labels map[string]string) *appsv1.Deployment {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: labels}}
	}
	cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		dep("a", map[string]string{testMullingServerKey: testServerName}),
		dep("b", map[string]string{"kubemoot.ai/triage-server": testServerName}),
		// An agent using this server for BOTH phases contributes 2.
		dep("c", map[string]string{testMullingServerKey: testServerName, "kubemoot.ai/triage-server": testServerName}),
		dep("d", map[string]string{testMullingServerKey: testOtherServer}),
	).Build()
	r := &ModelServerReconciler{Client: cli}
	server := &aiv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: testServerName, Namespace: "ns"}, Status: aiv1alpha1.ModelServerStatus{Capacity: &aiv1alpha1.DiscoveredCapacity{}}}
	r.countAssignedAgents(context.Background(), server)
	if server.Status.Capacity.MullingAgentCount != 2 || server.Status.Capacity.TriageAgentCount != 2 {
		t.Errorf("mulling=%d triage=%d, want 2/2", server.Status.Capacity.MullingAgentCount, server.Status.Capacity.TriageAgentCount)
	}
	if server.Status.Capacity.AgentCount != 4 {
		t.Errorf("AgentCount = %d, want 4 (each phase counted)", server.Status.Capacity.AgentCount)
	}
}
