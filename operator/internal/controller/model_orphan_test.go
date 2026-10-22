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
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func orphanModel(name, serverRef string, auto bool) *aiv1alpha1.Model {
	m := deletionModel(name, "crew-grade", serverRef, "qwen3:8b", false)
	if auto {
		m.Labels = map[string]string{annoAutoProvisioned: "true"}
	}
	return m
}

func reconcileOrphan(t *testing.T, m *aiv1alpha1.Model, others ...client.Object) (client.Client, error) {
	t.Helper()
	objs := append([]client.Object{m}, others...)
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(objs...).WithStatusSubresource(m).Build()
	r := &ModelReconciler{Client: cl}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)})
	return cl, err
}

func modelExists(t *testing.T, cl client.Client, m *aiv1alpha1.Model) bool {
	t.Helper()
	err := cl.Get(context.Background(), client.ObjectKeyFromObject(m), &aiv1alpha1.Model{})
	if err != nil && !errors.IsNotFound(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestReconcile_AutoProvisionedModelWithoutServerRefIsRemoved(t *testing.T) {
	m := orphanModel("auto-qwen3-8b-ollama-rig1", "", true)
	cl, err := reconcileOrphan(t, m)
	if err != nil {
		t.Fatal(err)
	}
	if modelExists(t, cl, m) {
		t.Error("an auto-provisioned Model with no server reference must be removed")
	}
}

func TestReconcile_AutoProvisionedModelNamingDeletedServerIsRemoved(t *testing.T) {
	m := orphanModel("auto-qwen3-14b-ollama-gpu", "ollama-gpu", true)
	other := deletionServer("http://127.0.0.1:1")
	cl, err := reconcileOrphan(t, m, other)
	if err != nil {
		t.Fatal(err)
	}
	if modelExists(t, cl, m) {
		t.Error("an auto-provisioned Model naming a deleted server must be removed")
	}
}

func TestReconcile_PersonWrittenModelWithoutServerIsKeptAndReported(t *testing.T) {
	for _, ref := range []string{"", "ollama-gpu"} {
		m := orphanModel("mine", ref, false)
		cl, err := reconcileOrphan(t, m)
		if err != nil {
			t.Fatal(err)
		}
		got := &aiv1alpha1.Model{}
		if err := cl.Get(context.Background(), client.ObjectKeyFromObject(m), got); err != nil {
			t.Fatalf("serverRef %q: a Model a person wrote must be kept: %v", ref, err)
		}
		if got.Status.State != phaseError || got.Status.Message == "" {
			t.Errorf("serverRef %q: want an Error status with a message, got %q %q", ref, got.Status.State, got.Status.Message)
		}
	}
}

func TestReconcile_AutoProvisionedModelOnExistingServerIsKept(t *testing.T) {
	m := orphanModel("auto-qwen3-8b", delTestServer, true)
	cl, err := reconcileOrphan(t, m, notReady(deletionServer("http://127.0.0.1:1")))
	if err != nil {
		t.Fatal(err)
	}
	if !modelExists(t, cl, m) {
		t.Error("an auto-provisioned Model whose server exists must be kept")
	}
}
