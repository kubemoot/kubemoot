/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// builtinAgentResources is the resource default of an agent container. The request covers the
// native agent runtime's steady use with headroom and the memory limit bounds a runaway to a
// quarter gigabyte.
//
// There is deliberately no CPU limit. A CPU limit makes the kernel pause the container once it
// uses its quota inside a 100ms window, and an agent spends its time orchestrating inference
// calls and waiting on them, so the pauses would show up as added latency on bursts that are
// cheap overall. The CPU request alone still gives the container its share when the node is
// busy. Memory is different: it cannot be taken back, so it keeps a limit. Because a request
// is set and the memory limit differs from it, the pod is Burstable (not BestEffort), so it is
// not among the first evicted under node pressure.
func builtinAgentResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("25m"),
			corev1.ResourceMemory: resource.MustParse("96Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

// builtinGooseResources is the built-in default of the goose harness. The container itself is
// small, but it runs `goose run` for every request and that process, with its tool
// subprocesses, needs far more than the agent runtime does. The request reserves a working set
// for one run and the memory limit leaves room for the extensions a run starts. No CPU limit,
// for the reason given at builtinAgentResources.
func builtinGooseResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		},
	}
}

// builtinResourcesFor picks the built-in default by harness name; an empty name is the agent
// runtime, and any harness other than goose uses the agent runtime's default.
func builtinResourcesFor(harness string) corev1.ResourceRequirements {
	if harness == HarnessNameGoose {
		return builtinGooseResources()
	}
	return builtinAgentResources()
}

// agentContainerResources resolves the resources of an agent container. The layers are the
// built-in default for the harness, then KubemootConfig spec.defaults.agentResources, then the
// Agent's own (override); a later layer wins. They merge per resource name: a layer replaces only
// the requests and limits it names, so an Agent that sets only a memory limit keeps the default
// requests. See mergeResources for how a layer's request and limit interact with the inherited
// ones.
func (r *AgentReconciler) agentContainerResources(harness string, override *corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := builtinResourcesFor(harness)
	if r.ConfigCache != nil {
		mergeResources(&out, r.ConfigCache.GetAgentResources())
	}
	mergeResources(&out, override)
	return out
}

// mergeResources lays one layer over dst, resource name by resource name, then reconciles the
// layer's requests with the limits. A zero limit means no limit and is ignored. A layer that sets
// a request above the inherited limit without setting that limit raises the limit to the
// request, so the user's request is never lowered. A layer that sets both a request and a limit
// with the request above the limit is the user's own conflict, and the request is lowered to the
// limit so the pod is valid. A layer that sets only a limit below an inherited request lowers
// that inherited request to the limit.
func mergeResources(dst *corev1.ResourceRequirements, src *corev1.ResourceRequirements) {
	if src == nil {
		return
	}
	dst.Requests = mergeList(dst.Requests, src.Requests, false)
	dst.Limits = mergeList(dst.Limits, src.Limits, true)
	for name, limit := range src.Limits {
		if req, ok := dst.Requests[name]; ok && !limit.IsZero() && req.Cmp(limit) > 0 && !hasKey(src.Requests, name) {
			dst.Requests[name] = limit.DeepCopy()
		}
	}
	for name, req := range src.Requests {
		reconcileOne(dst, name, req, src.Limits)
	}
	dst.Claims = mergeClaims(dst.Claims, src.Claims)
}

func reconcileOne(dst *corev1.ResourceRequirements, name corev1.ResourceName, req resource.Quantity, srcLimits corev1.ResourceList) {
	limit, hasLimit := dst.Limits[name]
	if !hasLimit || req.Cmp(limit) <= 0 {
		return
	}
	if own, ok := srcLimits[name]; ok && !own.IsZero() {
		dst.Requests[name] = limit.DeepCopy()
		return
	}
	dst.Limits[name] = req.DeepCopy()
}

func hasKey(l corev1.ResourceList, name corev1.ResourceName) bool {
	_, ok := l[name]
	return ok
}

// mergeList copies src over dst; with skipZero a zero quantity is left out (a zero limit).
func mergeList(dst, src corev1.ResourceList, skipZero bool) corev1.ResourceList {
	if dst == nil && len(src) > 0 {
		dst = corev1.ResourceList{}
	}
	for name, q := range src {
		if skipZero && q.IsZero() {
			continue
		}
		dst[name] = q.DeepCopy()
	}
	return dst
}

// mergeClaims merges resource claims by name; a later layer's claim replaces an earlier one of
// the same name, so the container never lists a name twice.
func mergeClaims(dst, src []corev1.ResourceClaim) []corev1.ResourceClaim {
	for _, c := range src {
		i := slices.IndexFunc(dst, func(e corev1.ResourceClaim) bool { return e.Name == c.Name })
		if i >= 0 {
			dst[i] = c
		} else {
			dst = append(dst, c)
		}
	}
	return dst
}
