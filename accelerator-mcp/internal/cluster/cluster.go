/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package cluster reads ModelProviders and records their calibration in their status.
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

// Annotations a person sets to ask for a fresh calibration; see the ModelProvider
// calibration docs.
const (
	AnnotationReset      = "kubemoot.ai/calibration-reset"
	AnnotationResetScope = "kubemoot.ai/calibration-reset-scope"
)

// ConditionCalibrated is the ModelProvider condition calibration sets.
const ConditionCalibrated = "Calibrated"

// PhaseCalibrated is the calibration phase of a provider whose every model was measured.
const PhaseCalibrated = "Calibrated"

// Cluster reads ModelProviders and writes their calibration status.
type Cluster struct {
	Client client.Client
}

// Providers lists every ModelProvider in the cluster.
func (c Cluster) Providers(ctx context.Context) ([]aiv1alpha1.ModelProvider, error) {
	list := &aiv1alpha1.ModelProviderList{}
	if err := c.Client.List(ctx, list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Provider finds a ModelProvider by name; an error when none or more than one has it.
func (c Cluster) Provider(ctx context.Context, name string) (*aiv1alpha1.ModelProvider, error) {
	providers, err := c.Providers(ctx)
	if err != nil {
		return nil, err
	}
	var found *aiv1alpha1.ModelProvider
	for i := range providers {
		if providers[i].Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("more than one ModelProvider is named %s", name)
		}
		found = &providers[i]
	}
	if found == nil {
		return nil, fmt.Errorf("no ModelProvider is named %s", name)
	}
	return found, nil
}

// BoundModels lists the distinct model names Model CRs in any namespace bind to the provider.
func (c Cluster) BoundModels(ctx context.Context, provider string) ([]string, error) {
	list := &aiv1alpha1.ModelList{}
	if err := c.Client.List(ctx, list); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for i := range list.Items {
		m := &list.Items[i]
		if m.Spec.ProviderRef == provider && m.Spec.Model != "" && !seen[m.Spec.Model] {
			seen[m.Spec.Model] = true
			out = append(out, m.Spec.Model)
		}
	}
	sort.Strings(out)
	return out, nil
}

// CalibrationEnabled is the provider's spec.calibration.enabled, true when unset.
func CalibrationEnabled(p *aiv1alpha1.ModelProvider) bool {
	return p.Spec.Calibration == nil || p.Spec.Calibration.Enabled == nil || *p.Spec.Calibration.Enabled
}

// ResetRequested is true when the reset annotation holds a value calibration has
// not acted on yet.
func ResetRequested(p *aiv1alpha1.ModelProvider) bool {
	token := p.Annotations[AnnotationReset]
	if token == "" {
		return false
	}
	return p.Status.Calibration == nil || token != p.Status.Calibration.ResetToken
}

// RecordCalibration sets the provider's status.calibration and its Calibrated
// condition, retrying when another writer updated the status first.
func (c Cluster) RecordCalibration(ctx context.Context, p *aiv1alpha1.ModelProvider,
	update func(*aiv1alpha1.ProviderCalibration, *aiv1alpha1.ModelProvider)) error {
	key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &aiv1alpha1.ModelProvider{}
		if err := c.Client.Get(ctx, key, current); err != nil {
			return err
		}
		// A merge patch sends only what changed, so status fields this build does
		// not know about are left as they are; the resource version guards races.
		original := current.DeepCopy()
		if current.Status.Calibration == nil {
			current.Status.Calibration = &aiv1alpha1.ProviderCalibration{}
		}
		update(current.Status.Calibration, current)
		meta.SetStatusCondition(&current.Status.Conditions, condition(current.Status.Calibration))
		return c.Client.Status().Patch(ctx, current,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
	})
}

func condition(cal *aiv1alpha1.ProviderCalibration) metav1.Condition {
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

// UsableMemoryMiB is the budget a model must fit on the provider, the reserve the
// agent runtime's fit gate keeps (12% of the total, at least 2 GiB); 0 when the
// total is unknown. The total is the discovered accelerator memory, else the
// declared spec.scheduling.memoryMiB.
func UsableMemoryMiB(p *aiv1alpha1.ModelProvider) int64 {
	total := TotalMemoryMiB(p)
	if total <= 0 {
		return 0
	}
	return max(int64(0), total-max(int64(2048), total*12/100))
}

// TotalMemoryMiB is the provider's memory: discovered, else declared, else 0.
func TotalMemoryMiB(p *aiv1alpha1.ModelProvider) int64 {
	if p.Status.Capacity != nil && p.Status.Capacity.VRAMTotalMiB > 0 {
		return p.Status.Capacity.VRAMTotalMiB
	}
	if p.Spec.Scheduling != nil {
		return p.Spec.Scheduling.MemoryMiB
	}
	return 0
}

// HasAccelerator is true when the provider's memory is accelerator memory it
// discovered, so a model partly outside it has spilled to the CPU.
func HasAccelerator(p *aiv1alpha1.ModelProvider) bool {
	return p.Status.Capacity != nil && p.Status.Capacity.VRAMTotalMiB > 0
}

// IsVLLM is true for a provider of vLLM servers, which batch requests and keep models asleep.
func IsVLLM(p *aiv1alpha1.ModelProvider) bool {
	return p.Spec.Type == aiv1alpha1.ProviderTypeVLLM
}

// VLLMProbeWidth is how many requests the parallel probe sends together to a vLLM
// server. The server batches without a fixed limit, so the width is a probe size, not a
// capacity.
const VLLMProbeWidth = 4

// MaxParallel is how many requests the provider's engine serves together; at least 1.
// A vLLM provider batches requests, so it is the probe width.
func MaxParallel(p *aiv1alpha1.ModelProvider) int {
	if IsVLLM(p) {
		return VLLMProbeWidth
	}
	if p.Status.Capacity != nil && p.Status.Capacity.MaxParallel > 0 {
		return p.Status.Capacity.MaxParallel
	}
	return 1
}
