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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func baseDeployment() *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-agent",
			Annotations: map[string]string{"some-annotation": "value"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": testFixtureAgent},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "agent",
							Image: "harbor/agent:v1.0.0",
							Env: []corev1.EnvVar{
								{Name: "KUBEMOOT_AGENT_NAME", Value: testFixtureAgent},
							},
						},
					},
				},
			},
		},
	}
}

func TestComputeDeploymentHash_Deterministic(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %s and %s", h1, h2)
	}
}

func TestComputeDeploymentHash_ChangesOnImageChange(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	d2.Spec.Template.Spec.Containers[0].Image = "harbor/agent:v2.0.0"

	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 == h2 {
		t.Error("expected different hash for different image")
	}
}

func TestComputeDeploymentHash_ChangesOnEnvVarChange(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	d2.Spec.Template.Spec.Containers[0].Env = append(
		d2.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{Name: "NEW_VAR", Value: "new-value"},
	)

	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 == h2 {
		t.Error("expected different hash for different env vars")
	}
}

func TestComputeDeploymentHash_ChangesOnReplicasChange(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	replicas := int32(3)
	d2.Spec.Replicas = &replicas

	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 == h2 {
		t.Error("expected different hash for different replicas")
	}
}

func TestComputeDeploymentHash_StableOnAnnotationChange(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	d2.Annotations["new-annotation"] = "new-value"

	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 != h2 {
		t.Error("expected SAME hash for annotation-only change (anti-reconciliation-storm guard)")
	}
}

func TestComputeDeploymentHash_ChangesOnInitContainerChange(t *testing.T) {
	d1 := baseDeployment()
	d2 := baseDeployment()
	d2.Spec.Template.Spec.InitContainers = []corev1.Container{
		{
			Name:  "mcp-bridge",
			Image: "harbor/mcp-bridge:v1.0.0",
		},
	}

	h1 := computeDeploymentHash(d1)
	h2 := computeDeploymentHash(d2)
	if h1 == h2 {
		t.Error("expected different hash when init container added")
	}
}

// TestComputeDeploymentHash_ChangesOnInitContainerFieldChange is the regression
// guard for the sidecar-staleness bug: changing a FIELD on an EXISTING init
// container (securityContext, env, ...) must bump the hash. The old subset hash
// captured only init-container Name/Image/Command, so a sidecar securityContext
// change silently no-oped and the Deployment went stale until deleted by hand.
func TestComputeDeploymentHash_ChangesOnInitContainerFieldChange(t *testing.T) {
	withSidecar := func() *appsv1.Deployment {
		d := baseDeployment()
		runAsNonRoot := true
		d.Spec.Template.Spec.InitContainers = []corev1.Container{
			{
				Name:            "code-sandbox-mcp",
				Image:           "harbor/sandbox:v1.0.0",
				SecurityContext: &corev1.SecurityContext{RunAsNonRoot: &runAsNonRoot},
			},
		}
		return d
	}

	// Flip the existing sidecar's securityContext (the exact field that was dropped).
	d1 := withSidecar()
	d2 := withSidecar()
	runAsRoot := false
	d2.Spec.Template.Spec.InitContainers[0].SecurityContext.RunAsNonRoot = &runAsRoot
	if computeDeploymentHash(d1) == computeDeploymentHash(d2) {
		t.Error("expected different hash when an existing sidecar securityContext changes")
	}

	// An env change on the existing sidecar must also bump the hash.
	d3 := withSidecar()
	d3.Spec.Template.Spec.InitContainers[0].Env = []corev1.EnvVar{{Name: "X", Value: "y"}}
	if computeDeploymentHash(d1) == computeDeploymentHash(d3) {
		t.Error("expected different hash when an existing sidecar env changes")
	}
}

func TestComputeDeploymentHash_EmptyDeployment_NoPanic(t *testing.T) {
	d := &appsv1.Deployment{}
	hash := computeDeploymentHash(d)
	if hash == "" {
		t.Error("expected non-empty hash even for empty deployment")
	}
}
