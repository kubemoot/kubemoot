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

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// nodeAcceleratorLabels are the node labels that name the accelerator on the node, in the order
// they are read: the NVIDIA GPU feature discovery label, then the AMD GPU operator label.
var nodeAcceleratorLabels = []string{"nvidia.com/gpu.product", "amd.com/gpu.product-name"}

// acceleratorModelFromNode returns the accelerator name a node's labels carry, or "" when none do.
func acceleratorModelFromNode(node *corev1.Node) string {
	for _, label := range nodeAcceleratorLabels {
		if name := node.Labels[label]; name != "" {
			return name
		}
	}
	return ""
}

// fillAcceleratorModelFromNode names the accelerator from the labels of the node the server runs
// on when the engine and the accelerator metrics did not name it. Best-effort: an unreadable
// node leaves the name as it was.
func (r *ModelServerReconciler) fillAcceleratorModelFromNode(ctx context.Context, server *aiv1alpha1.ModelServer) {
	c := server.Status.Capacity
	if c == nil || c.AcceleratorModel != "" || c.NodeName == "" {
		return
	}
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: c.NodeName}, &node); err != nil {
		logf.FromContext(ctx).V(1).Info("Could not read the node to name the accelerator", "node", c.NodeName, "error", err)
		return
	}
	if name := acceleratorModelFromNode(&node); name != "" {
		c.AcceleratorModel = name
	}
}
