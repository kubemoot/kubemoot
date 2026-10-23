/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// builtinAgentResources is the resource default of an agent container. The measured peak of the
// native agent runtime is about 65Mi of memory and 0.02 cores, so the request covers steady use
// with headroom and the memory limit bounds a runaway to a quarter gigabyte.
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

// builtinGooseResources is the built-in default of the goose harness. The container itself idles
// near 5Mi, but it runs `goose run` for every request and that process, with its tool
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
// Agent's own (override), then KubemootConfig spec.defaults.agentResources, then the built-in
// default for the harness. They merge per resource name: a layer replaces only the requests
// and limits it names, so an Agent that sets only a memory limit keeps the default requests.
// A request above the resolved limit of the same resource is lowered to the limit, so a small
// limit in a higher layer never produces a pod the API server rejects.
func (r *AgentReconciler) agentContainerResources(harness string, override *corev1.ResourceRequirements) corev1.ResourceRequirements {
	out := builtinResourcesFor(harness)
	if r.ConfigCache != nil {
		mergeResources(&out, r.ConfigCache.GetAgentResources())
	}
	mergeResources(&out, override)
	clampRequestsToLimits(&out)
	return out
}

// mergeResources lays src over dst, resource name by resource name.
func mergeResources(dst *corev1.ResourceRequirements, src *corev1.ResourceRequirements) {
	if src == nil {
		return
	}
	dst.Requests = mergeList(dst.Requests, src.Requests)
	dst.Limits = mergeList(dst.Limits, src.Limits)
	dst.Claims = append(dst.Claims, src.Claims...)
}

func mergeList(dst, src corev1.ResourceList) corev1.ResourceList {
	if dst == nil && len(src) > 0 {
		dst = corev1.ResourceList{}
	}
	for name, q := range src {
		dst[name] = q.DeepCopy()
	}
	return dst
}

func clampRequestsToLimits(rr *corev1.ResourceRequirements) {
	for name, limit := range rr.Limits {
		if req, ok := rr.Requests[name]; ok && req.Cmp(limit) > 0 {
			rr.Requests[name] = limit.DeepCopy()
		}
	}
}
