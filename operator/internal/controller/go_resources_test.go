/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func gatewayContainer(t *testing.T, crew *kubemootv1alpha1.Crew) corev1.Container {
	t.Helper()
	r := &CrewReconciler{ConfigCache: NewConfigCache()}
	cs := r.buildDeployment(crew).Spec.Template.Spec.Containers
	if len(cs) != 1 {
		t.Fatalf("want one gateway container, got %d", len(cs))
	}
	return cs[0]
}

func testCrew() *kubemootv1alpha1.Crew {
	return &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "c", Namespace: "ns"}}
}

func TestDiscussionGatewayDefaultsAreBurstableWithNoCPULimitAndGoMemLimit(t *testing.T) {
	c := gatewayContainer(t, testCrew())
	wantQty(t, "cpu request", c.Resources.Requests, corev1.ResourceCPU, "5m")
	wantQty(t, "memory request", c.Resources.Requests, corev1.ResourceMemory, "48Mi")
	wantQty(t, "memory limit", c.Resources.Limits, corev1.ResourceMemory, "128Mi")
	if _, ok := c.Resources.Limits[corev1.ResourceCPU]; ok {
		t.Errorf("a CPU limit is set by default: %v", c.Resources.Limits)
	}
	if got := goMemLimitOf(c.Env); got != "115MiB" {
		t.Errorf("GOMEMLIMIT = %q, want 115MiB", got)
	}
}

func TestDiscussionGatewayGoMemLimitFollowsTheCrewsOwnResources(t *testing.T) {
	crew := testCrew()
	crew.Spec.Discussion = &kubemootv1alpha1.DiscussionConfig{Resources: &corev1.ResourceRequirements{
		Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
	}}
	if got := goMemLimitOf(gatewayContainer(t, crew).Env); got != "460MiB" {
		t.Errorf("GOMEMLIMIT = %q, want 460MiB", got)
	}
	crew.Spec.Discussion.Resources = &corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
	}
	if got := goMemLimitOf(gatewayContainer(t, crew).Env); got != "" {
		t.Errorf("a crew with no memory limit got GOMEMLIMIT %q", got)
	}
}

func TestBridgeSidecarKeepsItsSizeAndGetsGoMemLimit(t *testing.T) {
	mcp := mrcMCPServer(nil)
	mcp.Spec.Transport = kubemootv1alpha1.TransportStdio
	mcp.Spec.Command = []string{"/bin/server"}
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	var bridge *corev1.Container
	for _, group := range [][]corev1.Container{
		r.buildDeployment(mcp).Spec.Template.Spec.InitContainers,
		r.buildDeployment(mcp).Spec.Template.Spec.Containers,
	} {
		for i := range group {
			if group[i].Name == "mcp-bridge" {
				bridge = &group[i]
			}
		}
	}
	if bridge == nil {
		t.Fatal("no mcp-bridge container in a stdio MCPServer pod")
	}
	wantQty(t, "memory request", bridge.Resources.Requests, corev1.ResourceMemory, "16Mi")
	wantQty(t, "memory limit", bridge.Resources.Limits, corev1.ResourceMemory, "64Mi")
	if got := goMemLimitOf(bridge.Env); got != "57MiB" {
		t.Errorf("GOMEMLIMIT = %q, want 57MiB", got)
	}
}

// goMemLimitOf is the GOMEMLIMIT in env, or "" when there is none.
func goMemLimitOf(env []corev1.EnvVar) string {
	v, _ := envValue(env, "GOMEMLIMIT")
	return v
}
