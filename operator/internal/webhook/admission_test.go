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

package webhook

import (
	"context"
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// These tests drive each typed validator through controller-runtime's
// admission handler, so the decode-then-dispatch path the manager serves is
// covered, not only the validate* helpers.

const (
	admissionUID     = "admission-test"
	validCrewName    = "hello-world"
	fitnessConfigMap = "hello-world-fitness-tests"
	caseDelete       = "delete"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := aiv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return s
}

func rawObject(t *testing.T, obj runtime.Object) runtime.RawExtension {
	t.Helper()
	if obj == nil {
		return runtime.RawExtension{}
	}
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return runtime.RawExtension{Raw: b}
}

func admissionRequest(t *testing.T, op admissionv1.Operation, obj, old runtime.Object) admission.Request {
	t.Helper()
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID:       admissionUID,
		Operation: op,
		Object:    rawObject(t, obj),
		OldObject: rawObject(t, old),
	}}
}

type admissionCase struct {
	name        string
	op          admissionv1.Operation
	obj, old    runtime.Object
	wantAllowed bool
}

func runAdmissionCases(t *testing.T, wh *admission.Webhook, cases []admissionCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := wh.Handle(context.Background(), admissionRequest(t, tc.op, tc.obj, tc.old))
			if resp.Allowed != tc.wantAllowed {
				t.Errorf("allowed = %v, want %v (result: %+v)", resp.Allowed, tc.wantAllowed, resp.Result)
			}
		})
	}
}

func typeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: aiv1alpha1.GroupVersion.String(), Kind: kind}
}

func fitness(phase aiv1alpha1.CrewFitnessPhase, testRef string) *aiv1alpha1.CrewFitness {
	return &aiv1alpha1.CrewFitness{
		TypeMeta:   typeMeta("CrewFitness"),
		ObjectMeta: metav1.ObjectMeta{Name: "fit", Namespace: "ns"},
		Spec: aiv1alpha1.CrewFitnessSpec{
			CrewRef:      validCrewName,
			TestRef:      testRef,
			ConfigMapRef: fitnessConfigMap,
		},
		Status: aiv1alpha1.CrewFitnessStatus{Phase: phase},
	}
}

func TestCrewFitnessAdmission(t *testing.T) {
	wh := admission.WithValidator(testScheme(t), &CrewFitnessValidator{})
	runAdmissionCases(t, wh, []admissionCase{
		{"create valid", admissionv1.Create, fitness("", "a"), nil, true},
		{"create missing testRef", admissionv1.Create, fitness("", ""), nil, false},
		{"update spec while running", admissionv1.Update, fitness(aiv1alpha1.CrewFitnessPhaseRunning, "b"),
			fitness(aiv1alpha1.CrewFitnessPhaseRunning, "a"), true},
		{"update spec after passed", admissionv1.Update, fitness(aiv1alpha1.CrewFitnessPhasePassed, "b"),
			fitness(aiv1alpha1.CrewFitnessPhasePassed, "a"), false},
		{"update spec after error", admissionv1.Update, fitness(aiv1alpha1.CrewFitnessPhaseError, "b"),
			fitness(aiv1alpha1.CrewFitnessPhaseError, "a"), false},
		{"update unchanged spec after failed", admissionv1.Update, fitness(aiv1alpha1.CrewFitnessPhaseFailed, "a"),
			fitness(aiv1alpha1.CrewFitnessPhaseFailed, "a"), true},
		{caseDelete, admissionv1.Delete, nil, fitness(aiv1alpha1.CrewFitnessPhasePassed, "a"), true},
	})
}

func crewNamed(name string) *aiv1alpha1.Crew {
	return &aiv1alpha1.Crew{
		TypeMeta:   typeMeta("Crew"),
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: aiv1alpha1.CrewSpec{
			Discussion: &aiv1alpha1.DiscussionConfig{Enabled: ptr.To(true)},
		},
	}
}

func TestCrewAdmission(t *testing.T) {
	wh := admission.WithValidator(testScheme(t), &CrewValidator{})
	runAdmissionCases(t, wh, []admissionCase{
		{"create valid", admissionv1.Create, crewNamed(validCrewName), nil, true},
		{"create invalid name", admissionv1.Create, crewNamed("Hello_World"), nil, false},
		{"update validates new object", admissionv1.Update, crewNamed("Bad_Name"), crewNamed(validCrewName), false},
		{caseDelete, admissionv1.Delete, nil, crewNamed("Bad_Name"), true},
	})

	resp := wh.Handle(context.Background(), admissionRequest(t, admissionv1.Create, crewNamed(validCrewName), nil))
	if len(resp.Warnings) == 0 {
		t.Error("expected a discussion warning to reach the admission response")
	}
}

func TestMCPServerAdmission(t *testing.T) {
	server := func(image, endpoint string) *aiv1alpha1.MCPServer {
		return &aiv1alpha1.MCPServer{
			TypeMeta:   typeMeta("MCPServer"),
			ObjectMeta: metav1.ObjectMeta{Name: "srv", Namespace: "ns"},
			Spec:       aiv1alpha1.MCPServerSpec{Image: image, ExternalEndpoint: endpoint},
		}
	}
	wh := admission.WithValidator(testScheme(t), &MCPServerValidator{})
	runAdmissionCases(t, wh, []admissionCase{
		{"create with image", admissionv1.Create, server("img", ""), nil, true},
		{"create with neither", admissionv1.Create, server("", ""), nil, false},
		{"update to both", admissionv1.Update, server("img", "http://x"), server("img", ""), false},
		{caseDelete, admissionv1.Delete, nil, server("", ""), true},
	})
}

func TestAgentAdmission(t *testing.T) {
	agent := &aiv1alpha1.Agent{
		TypeMeta:   typeMeta("Agent"),
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns"},
	}
	runAdmissionCases(t, admission.WithValidator(testScheme(t), &AgentValidator{}), []admissionCase{
		{"agent create", admissionv1.Create, agent, nil, true},
		{"agent update", admissionv1.Update, agent, agent, true},
		{"agent delete", admissionv1.Delete, nil, agent, true},
	})
}

func TestRAGSourceAdmission(t *testing.T) {
	rag := &aiv1alpha1.RAGSource{
		TypeMeta:   typeMeta("RAGSource"),
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: aiv1alpha1.RAGSourceSpec{
			Source: aiv1alpha1.SourceConfig{Type: aiv1alpha1.RAGSourceTypeGit},
		},
	}
	runAdmissionCases(t, admission.WithValidator(testScheme(t), &RAGSourceValidator{}), []admissionCase{
		{"ragsource create git type without git block", admissionv1.Create, rag, nil, false},
		{"ragsource delete", admissionv1.Delete, nil, rag, true},
	})
}

func TestAdmissionRejectsUndecodableObject(t *testing.T) {
	wh := admission.WithValidator(testScheme(t), &CrewValidator{})
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		UID:       admissionUID,
		Operation: admissionv1.Create,
		Object:    runtime.RawExtension{Raw: []byte(`{not json`)},
	}}
	if resp := wh.Handle(context.Background(), req); resp.Allowed {
		t.Error("expected malformed object to be rejected")
	}
}
