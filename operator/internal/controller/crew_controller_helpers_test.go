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

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// newTestCrewReconciler creates a minimal CrewReconciler for testing pure helper methods.
// No real Kubernetes client is used — only the Scheme is needed for label building.
func newTestCrewReconciler() *CrewReconciler {
	return &CrewReconciler{
		Scheme: runtime.NewScheme(),
	}
}

func TestCrewGatewayName(t *testing.T) {
	r := newTestCrewReconciler()
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: "my-crew"},
	}
	if got := r.gatewayName(crew); got != "my-crew-discussion" {
		t.Errorf("expected my-crew-discussion, got %s", got)
	}
}

func TestCrewRbacName(t *testing.T) {
	r := newTestCrewReconciler()
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA},
	}
	if got := r.rbacName(crew); got != "crew-team-a-pilot-discussion" {
		t.Errorf("expected crew-team-a-pilot-discussion, got %s", got)
	}
}

func TestCrewRbacName_SameCrewInTwoNamespacesDiffers(t *testing.T) {
	r := newTestCrewReconciler()
	a := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceA}}
	b := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: testCrewPilot, Namespace: testNamespaceB}}
	if r.rbacName(a) == r.rbacName(b) {
		t.Errorf("cluster RBAC names collide: %s", r.rbacName(a))
	}
}

func TestCrewBuildLabels(t *testing.T) {
	r := newTestCrewReconciler()
	crew := &kubemootv1alpha1.Crew{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
	}
	labels := r.buildLabels(crew)

	if labels[labelName] != "alpha-discussion" {
		t.Errorf("expected alpha-discussion, got %s", labels[labelName])
	}
	if labels[labelInstance] != "alpha" {
		t.Errorf("expected alpha, got %s", labels[labelInstance])
	}
	if labels[labelManagedBy] != managedByValue {
		t.Errorf("expected %s, got %s", managedByValue, labels[labelManagedBy])
	}
	if labels[labelComponent] != "discussion-gateway" {
		t.Errorf("expected discussion-gateway, got %s", labels[labelComponent])
	}
	if labels[crewLabelKey] != "alpha" {
		t.Errorf("expected alpha, got %s", labels[crewLabelKey])
	}
}

func TestCrewBuildLabels_DifferentNames(t *testing.T) {
	r := newTestCrewReconciler()
	tests := []struct {
		name         string
		expectedGW   string
		expectedCrew string
	}{
		{"homelab-pilot", "homelab-pilot-discussion", "homelab-pilot"},
		{"x", "x-discussion", "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crew := &kubemootv1alpha1.Crew{
				ObjectMeta: metav1.ObjectMeta{Name: tt.name},
			}
			labels := r.buildLabels(crew)
			if labels[labelName] != tt.expectedGW {
				t.Errorf("expected %s, got %s", tt.expectedGW, labels[labelName])
			}
			if labels[crewLabelKey] != tt.expectedCrew {
				t.Errorf("expected %s, got %s", tt.expectedCrew, labels[crewLabelKey])
			}
		})
	}
}
