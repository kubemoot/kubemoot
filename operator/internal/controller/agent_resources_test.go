/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func rl(cpu, mem string) corev1.ResourceList {
	out := corev1.ResourceList{}
	if cpu != "" {
		out[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if mem != "" {
		out[corev1.ResourceMemory] = resource.MustParse(mem)
	}
	return out
}

// wantQty checks one resource quantity in a list; an empty want means the resource is absent.
func wantQty(t *testing.T, what string, list corev1.ResourceList, name corev1.ResourceName, want string) {
	t.Helper()
	got, ok := list[name]
	if want == "" {
		if ok {
			t.Errorf("%s %s = %s, want none", what, name, got.String())
		}
		return
	}
	if !ok || got.Cmp(resource.MustParse(want)) != 0 {
		t.Errorf("%s %s = %s, want %s", what, name, got.String(), want)
	}
}

func withConfigDefault(r *AgentReconciler, rr *corev1.ResourceRequirements) {
	r.ConfigCache.Update(&kubemootv1alpha1.KubemootConfig{
		Spec: kubemootv1alpha1.KubemootConfigSpec{Defaults: kubemootv1alpha1.DefaultConfig{AgentResources: rr}},
	})
}

func TestBuiltinAgentResourcesApplyWithNoConfig(t *testing.T) {
	r, _ := harnessReconciler(t)
	got := r.agentContainerResources("", nil)
	wantQty(t, "request", got.Requests, corev1.ResourceCPU, "25m")
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "96Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "256Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceCPU, "")
}

func TestNilConfigCacheStillGetsTheBuiltinDefault(t *testing.T) {
	r := &AgentReconciler{}
	got := r.agentContainerResources("", nil)
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "96Mi")
}

func TestKubemootConfigDefaultOverridesBuiltin(t *testing.T) {
	r, _ := harnessReconciler(t)
	withConfigDefault(r, &corev1.ResourceRequirements{Requests: rl("50m", "128Mi"), Limits: rl("", "512Mi")})
	got := r.agentContainerResources("", nil)
	wantQty(t, "request", got.Requests, corev1.ResourceCPU, "50m")
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "128Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "512Mi")
}

func TestAgentSpecOverridesConfigAndBuiltin(t *testing.T) {
	r, _ := harnessReconciler(t)
	withConfigDefault(r, &corev1.ResourceRequirements{Requests: rl("50m", "128Mi"), Limits: rl("", "512Mi")})
	got := r.agentContainerResources("", &corev1.ResourceRequirements{Requests: rl("200m", "300Mi"), Limits: rl("1", "1Gi")})
	wantQty(t, "request", got.Requests, corev1.ResourceCPU, "200m")
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "300Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceCPU, "1")
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "1Gi")
}

func TestPartialOverrideMergesPerResource(t *testing.T) {
	r, _ := harnessReconciler(t)
	withConfigDefault(r, &corev1.ResourceRequirements{Requests: rl("", "128Mi")})
	got := r.agentContainerResources("", &corev1.ResourceRequirements{Limits: rl("", "200Mi")})
	wantQty(t, "request", got.Requests, corev1.ResourceCPU, "25m")      // built-in kept
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "128Mi") // config layer
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "200Mi")     // agent layer
}

func TestRequestAboveLimitIsLoweredToTheLimit(t *testing.T) {
	r, _ := harnessReconciler(t)
	got := r.agentContainerResources("", &corev1.ResourceRequirements{Limits: rl("", "64Mi")})
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "64Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "64Mi")
}

func TestGooseHarnessHasItsOwnBuiltinDefault(t *testing.T) {
	r, _ := harnessReconciler(t)
	got := r.agentContainerResources(HarnessNameGoose, nil)
	wantQty(t, "request", got.Requests, corev1.ResourceMemory, "256Mi")
	wantQty(t, "limit", got.Limits, corev1.ResourceMemory, "1Gi")
	other := r.agentContainerResources("custom", nil)
	wantQty(t, "request", other.Requests, corev1.ResourceMemory, "96Mi")
}

func TestMergeDoesNotMutateTheCachedConfig(t *testing.T) {
	r, _ := harnessReconciler(t)
	cfg := &corev1.ResourceRequirements{Requests: rl("50m", "")}
	withConfigDefault(r, cfg)
	r.agentContainerResources("", &corev1.ResourceRequirements{Requests: rl("500m", "")})
	wantQty(t, "cached request", r.ConfigCache.GetAgentResources().Requests, corev1.ResourceCPU, "50m")
	wantQty(t, "source request", cfg.Requests, corev1.ResourceCPU, "50m")
}

// qosClass mirrors the Kubernetes rule over the pod's containers and init containers.
func qosClass(spec corev1.PodSpec) corev1.PodQOSClass {
	all := append(append([]corev1.Container{}, spec.InitContainers...), spec.Containers...)
	anySet, guaranteed := false, true
	for _, c := range all {
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			anySet = true
		}
		for _, n := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			lim, ok := c.Resources.Limits[n]
			if req, hasReq := c.Resources.Requests[n]; !ok || (hasReq && req.Cmp(lim) != 0) {
				guaranteed = false
			}
		}
	}
	switch {
	case !anySet:
		return corev1.PodQOSBestEffort
	case guaranteed:
		return corev1.PodQOSGuaranteed
	}
	return corev1.PodQOSBurstable
}

func TestLegacyAgentPodIsBurstable(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness = nil
	spec := buildSidecarPod(t, agent).Spec.Template.Spec
	c := spec.Containers[0]
	if c.Name != harnessContainerName || qosClass(spec) != corev1.PodQOSBurstable {
		t.Errorf("container %q QoS %s, want agent Burstable", c.Name, qosClass(spec))
	}
	wantQty(t, "request", c.Resources.Requests, corev1.ResourceCPU, "25m")
	wantQty(t, "request", c.Resources.Requests, corev1.ResourceMemory, "96Mi")
	wantQty(t, "limit", c.Resources.Limits, corev1.ResourceMemory, "256Mi")
	wantQty(t, "limit", c.Resources.Limits, corev1.ResourceCPU, "")
}

func TestSidecarPodKeepsSidecarResourcesAndIsBurstable(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness.Name = "custom"
	spec := buildSidecarPod(t, agent).Spec.Template.Spec
	sc := spec.InitContainers[0]
	wantQty(t, "sidecar request", sc.Resources.Requests, corev1.ResourceCPU, "25m")
	wantQty(t, "sidecar request", sc.Resources.Requests, corev1.ResourceMemory, "32Mi")
	wantQty(t, "sidecar limit", sc.Resources.Limits, corev1.ResourceCPU, "250m")
	wantQty(t, "sidecar limit", sc.Resources.Limits, corev1.ResourceMemory, "64Mi")
	h := spec.Containers[0]
	wantQty(t, "harness request", h.Resources.Requests, corev1.ResourceMemory, "96Mi")
	if got := qosClass(spec); got != corev1.PodQOSBurstable {
		t.Errorf("QoS = %s, want Burstable", got)
	}
}

func TestGoosePodUsesTheGooseDefaultAndAgentOverrideWins(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness.Name = HarnessNameGoose
	h := buildSidecarPod(t, agent).Spec.Template.Spec.Containers[0]
	wantQty(t, "goose request", h.Resources.Requests, corev1.ResourceMemory, "256Mi")
	agent.Spec.Harness.Resources = &corev1.ResourceRequirements{Limits: rl("", "2Gi")}
	h = buildSidecarPod(t, agent).Spec.Template.Spec.Containers[0]
	wantQty(t, "goose limit", h.Resources.Limits, corev1.ResourceMemory, "2Gi")
	wantQty(t, "goose request", h.Resources.Requests, corev1.ResourceMemory, "256Mi")
}

func TestDeploymentResourcesOverrideLegacyAgentContainer(t *testing.T) {
	agent := sidecarAgent()
	agent.Spec.Harness = nil
	agent.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Resources: &corev1.ResourceRequirements{Requests: rl("", "200Mi"), Limits: rl("", "400Mi")}}
	c := buildSidecarPod(t, agent).Spec.Template.Spec.Containers[0]
	wantQty(t, "request", c.Resources.Requests, corev1.ResourceMemory, "200Mi")
	wantQty(t, "request", c.Resources.Requests, corev1.ResourceCPU, "25m")
	wantQty(t, "limit", c.Resources.Limits, corev1.ResourceMemory, "400Mi")
}
