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
	"testing"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	accelMI300      = "Instinct-MI300X"
	accelNode       = "gpu-1"
	accelFromEngine = "from engine"
)

func TestAcceleratorModelFromNodeLabels(t *testing.T) {
	for name, tc := range map[string]struct {
		labels map[string]string
		want   string
	}{
		"nvidia":   {map[string]string{"nvidia.com/gpu.product": "NVIDIA-GeForce-RTX-5090"}, "NVIDIA-GeForce-RTX-5090"},
		"amd":      {map[string]string{"amd.com/gpu.product-name": accelMI300}, accelMI300},
		"no label": {map[string]string{"kubernetes.io/arch": "amd64"}, ""},
		"empty":    {nil, ""},
	} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}}
		if got := acceleratorModelFromNode(node); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestFillAcceleratorModelFromNode(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name: accelNode, Labels: map[string]string{"amd.com/gpu.product-name": accelMI300},
	}}
	r := &ModelServerReconciler{Client: fake.NewClientBuilder().WithObjects(node).Build()}
	newServer := func(c *aiv1alpha1.DiscoveredCapacity) *aiv1alpha1.ModelServer {
		return &aiv1alpha1.ModelServer{Status: aiv1alpha1.ModelServerStatus{Capacity: c}}
	}

	s := newServer(&aiv1alpha1.DiscoveredCapacity{NodeName: accelNode})
	r.fillAcceleratorModelFromNode(context.Background(), s)
	if c := s.Status.Capacity; c.AcceleratorModel != accelMI300 {
		t.Errorf("a node label names the accelerator %+v", c)
	}

	known := newServer(&aiv1alpha1.DiscoveredCapacity{NodeName: accelNode, AcceleratorModel: "from DCGM"})
	r.fillAcceleratorModelFromNode(context.Background(), known)
	if known.Status.Capacity.AcceleratorModel != "from DCGM" {
		t.Errorf("a name an earlier source gave is kept, got %q", known.Status.Capacity.AcceleratorModel)
	}

	for name, srv := range map[string]*aiv1alpha1.ModelServer{
		"no capacity":  newServer(nil),
		"no node name": newServer(&aiv1alpha1.DiscoveredCapacity{}),
		"unknown node": newServer(&aiv1alpha1.DiscoveredCapacity{NodeName: "absent-node"}),
	} {
		r.fillAcceleratorModelFromNode(context.Background(), srv)
		if srv.Status.Capacity != nil && srv.Status.Capacity.AcceleratorModel != "" {
			t.Errorf("%s: named %q from nothing", name, srv.Status.Capacity.AcceleratorModel)
		}
	}
}

func TestEngineReportedAcceleratorModelIsApplied(t *testing.T) {
	c := &aiv1alpha1.DiscoveredCapacity{AcceleratorModel: "from node", GPUModel: "from node"}
	applySnapshotToCapacity(c, engine.Snapshot{AcceleratorModel: accelFromEngine})
	if c.AcceleratorModel != accelFromEngine || c.GPUModel != accelFromEngine {
		t.Errorf("the engine's own report wins: %+v", c)
	}
	applySnapshotToCapacity(c, engine.Snapshot{})
	if c.AcceleratorModel != accelFromEngine {
		t.Errorf("a probe that reports no name keeps the name, got %q", c.AcceleratorModel)
	}
}

func TestSnapshotWritesAcceleratorMemoryUnderBothNames(t *testing.T) {
	c := &aiv1alpha1.DiscoveredCapacity{}
	applySnapshotToCapacity(c, engine.Snapshot{
		LoadedKnown: true, LoadedSizesKnown: true,
		Loaded: []aiv1alpha1.LoadedModel{{Name: "m", AcceleratorMemory: 2 << 30, SizeVRAM: 2 << 30}},
	})
	if c.AcceleratorMemoryUsedMiB != 2048 || c.VRAMUsedMiB != 2048 {
		t.Errorf("used = %d / %d, want 2048 under both names", c.AcceleratorMemoryUsedMiB, c.VRAMUsedMiB)
	}
}
