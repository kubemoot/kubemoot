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
	stderrors "errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/events"
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

type failingReader struct{ client.Reader }

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return stderrors.New("api unavailable")
}

func orphanReconciler(t *testing.T, m *aiv1alpha1.Model, reader client.Reader, rec *events.FakeRecorder) (*ModelReconciler, client.Client) {
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(m).WithStatusSubresource(m).Build()
	return &ModelReconciler{Client: cl, APIReader: reader, Recorder: rec}, cl
}

func TestReconcile_OrphanKeptWhenAPIReaderFindsServerTheCacheMissed(t *testing.T) {
	m := orphanModel("auto-x", "ollama-gpu", true)
	s := deletionServer("http://127.0.0.1:1")
	s.Name = "ollama-gpu"
	live := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(s).Build()
	rec := events.NewFakeRecorder(5)
	r, cl := orphanReconciler(t, m, live, rec)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)}); err != nil {
		t.Fatal(err)
	}
	if !modelExists(t, cl, m) {
		t.Error("a server present via the API reader must keep the Model")
	}
	if len(rec.Events) != 0 {
		t.Error("no event is expected when the Model is kept")
	}
}

func TestReconcile_OrphanKeptAndErrorReturnedWhenReadFails(t *testing.T) {
	m := orphanModel("auto-x", "ollama-gpu", true)
	r, cl := orphanReconciler(t, m, failingReader{}, events.NewFakeRecorder(5))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)}); err == nil {
		t.Error("a failed read must be returned for a retry")
	}
	if !modelExists(t, cl, m) {
		t.Error("a failed read must keep the Model")
	}
}

func TestReconcile_OrphanLabelOtherThanTrueIsKept(t *testing.T) {
	m := orphanModel("auto-x", "", false)
	m.Labels = map[string]string{annoAutoProvisioned: "false"}
	r, cl := orphanReconciler(t, m, nil, events.NewFakeRecorder(5))
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)}); err != nil {
		t.Fatal(err)
	}
	if !modelExists(t, cl, m) {
		t.Error("a label value other than true must keep the Model")
	}
}

func TestReconcile_OrphanRemovalEmitsEventNamingReason(t *testing.T) {
	for ref, want := range map[string]string{"": "spec.serverRef is empty", "absent-srv": `"absent-srv" does not exist`} {
		m := orphanModel("auto-x", ref, true)
		rec := events.NewFakeRecorder(5)
		r, cl := orphanReconciler(t, m, nil, rec)
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(m)}); err != nil {
			t.Fatal(err)
		}
		if modelExists(t, cl, m) {
			t.Errorf("serverRef %q: Model must be removed", ref)
		}
		select {
		case ev := <-rec.Events:
			if !strings.Contains(ev, want) {
				t.Errorf("serverRef %q: event %q lacks %q", ref, ev, want)
			}
		default:
			t.Errorf("serverRef %q: no event emitted", ref)
		}
	}
}
