/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package cluster reads ModelServers and records their calibration in their status.
package cluster

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Annotations a person sets to ask for a fresh calibration; see the ModelServer
// calibration docs.
const (
	AnnotationReset      = "kubemoot.ai/calibration-reset"
	AnnotationResetScope = "kubemoot.ai/calibration-reset-scope"
)

// ConditionCalibrated is the ModelServer condition calibration sets.
const ConditionCalibrated = "Calibrated"

// PhaseCalibrated is the calibration phase of a server whose every model was measured.
const PhaseCalibrated = "Calibrated"

// Cluster reads ModelServers and writes their calibration status.
type Cluster struct {
	Client client.Client
}

// Servers lists every ModelServer in the cluster.
func (c Cluster) Servers(ctx context.Context) ([]aiv1alpha1.ModelServer, error) {
	list := &aiv1alpha1.ModelServerList{}
	if err := c.Client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Server finds a ModelServer by name; an error when none or more than one has it.
func (c Cluster) Server(ctx context.Context, name string) (*aiv1alpha1.ModelServer, error) {
	servers, err := c.Servers(ctx)
	if err != nil {
		return nil, err
	}
	var found *aiv1alpha1.ModelServer
	for i := range servers {
		if servers[i].Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("more than one ModelServer is named %s", name)
		}
		found = &servers[i]
	}
	if found == nil {
		return nil, fmt.Errorf("no ModelServer is named %s", name)
	}
	return found, nil
}

// BoundModels lists the distinct model names Model CRs in any namespace bind to the server.
func (c Cluster) BoundModels(ctx context.Context, server string) ([]string, error) {
	list := &aiv1alpha1.ModelList{}
	if err := c.Client.List(ctx, list); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for i := range list.Items {
		m := &list.Items[i]
		if m.Spec.ServerRef == server && m.Spec.Model != "" && !seen[m.Spec.Model] {
			seen[m.Spec.Model] = true
			out = append(out, m.Spec.Model)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CalibrationEnabled is the server's spec.calibration.enabled, true when unset.
func CalibrationEnabled(p *aiv1alpha1.ModelServer) bool {
	return p.Spec.Calibration == nil || p.Spec.Calibration.Enabled == nil || *p.Spec.Calibration.Enabled
}

// ResetRequested is true when the reset annotation holds a value calibration has
// not acted on yet.
func ResetRequested(p *aiv1alpha1.ModelServer) bool {
	token := p.Annotations[AnnotationReset]
	if token == "" {
		return false
	}
	return p.Status.Calibration == nil || token != p.Status.Calibration.ResetToken
}

// RecordCalibration sets the server's status.calibration and its Calibrated
// condition, retrying when another writer updated the status first.
func (c Cluster) RecordCalibration(ctx context.Context, p *aiv1alpha1.ModelServer,
	update func(*aiv1alpha1.ServerCalibration, *aiv1alpha1.ModelServer)) error {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &aiv1alpha1.ModelServer{}
		if err := c.Client.Get(ctx, key, current); err != nil {
			return err
		}
		// A merge patch sends only what changed, so status fields this build does
		// not know about are left as they are; the resource version guards races.
		original := current.DeepCopy()
		if current.Status.Calibration == nil {
			current.Status.Calibration = &aiv1alpha1.ServerCalibration{}
		}
		update(current.Status.Calibration, current)
		meta.SetStatusCondition(&current.Status.Conditions, condition(current.Status.Calibration))
		return c.Client.Status().Patch(ctx, current,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
	})
}

func condition(cal *aiv1alpha1.ServerCalibration) metav1.Condition {
	status := metav1.ConditionFalse
	if cal.Phase == PhaseCalibrated {
		status = metav1.ConditionTrue
	}
	reason := cal.Phase
	if reason == "" {
		reason = "Unknown"
	}
	return metav1.Condition{Type: ConditionCalibrated, Status: status, Reason: reason, Message: cal.Message}
}

// UsableMemoryMiB is the budget a model must fit on the server, the reserve the
// agent runtime's fit gate keeps (12% of the total, at least 2 GiB); 0 when the
// total is unknown. The total is the discovered accelerator memory, else the
// declared spec.scheduling.memoryMiB.
func UsableMemoryMiB(p *aiv1alpha1.ModelServer) int64 {
	total := TotalMemoryMiB(p)
	if total <= 0 {
		return 0
	}
	return max(int64(0), total-max(int64(2048), total*12/100))
}

// TotalMemoryMiB is the server's memory: discovered, else declared, else 0.
func TotalMemoryMiB(p *aiv1alpha1.ModelServer) int64 {
	if p.Status.Capacity != nil && p.Status.Capacity.VRAMTotalMiB > 0 {
		return p.Status.Capacity.VRAMTotalMiB
	}
	if p.Spec.Scheduling != nil {
		return p.Spec.Scheduling.MemoryMiB
	}
	return 0
}

// HasAccelerator is true when the server's memory is accelerator memory it
// discovered, so a model partly outside it has spilled to the CPU.
func HasAccelerator(p *aiv1alpha1.ModelServer) bool {
	return p.Status.Capacity != nil && p.Status.Capacity.VRAMTotalMiB > 0
}

// IsVLLM is true for a server of vLLM servers, which batch requests and keep models asleep.
func IsVLLM(p *aiv1alpha1.ModelServer) bool {
	return p.Spec.Type == aiv1alpha1.ServerTypeVLLM
}

// VLLMProbeWidth is how many requests the parallel probe sends together to a vLLM
// server. The server batches without a fixed limit, so the width is a probe size, not a
// capacity.
const VLLMProbeWidth = 4

// MaxParallel is how many requests the server's engine serves together; at least 1.
// A vLLM server batches requests, so it is the probe width.
func MaxParallel(p *aiv1alpha1.ModelServer) int {
	if IsVLLM(p) {
		return VLLMProbeWidth
	}
	if p.Status.Capacity != nil && p.Status.Capacity.MaxParallel > 0 {
		return p.Status.Capacity.MaxParallel
	}
	return 1
}
