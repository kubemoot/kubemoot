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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func modelTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := aiv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("add kubemoot scheme: %v", err)
	}
	return s
}

// A transient provider probe failure (/api/tags returns 500) must NOT trigger a
// model pull. pullOllamaModel does a long synchronous download that blocks the
// reconcile worker; spurious-pulling on a momentary blip is what left fresh crew
// Models stuck and their agents Unschedulable. The reconcile must requeue and
// leave an already-Ready model untouched.
func TestReconcileOllamaModel_ProbeErrorDoesNotPull(t *testing.T) {
	var pulls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.WriteHeader(http.StatusInternalServerError) // probe fails
		case "/api/pull":
			atomic.AddInt32(&pulls, 1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	model := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen-8b", Namespace: "crew-x"},
		Spec:       aiv1alpha1.ModelSpec{Model: "qwen3:8b", ProviderRef: "ollama-gpu"},
		Status:     aiv1alpha1.ModelStatus{State: "Available", Ready: true},
	}
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-gpu", Namespace: "kubemoot"},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
	}

	r := &ModelReconciler{HTTPClient: srv.Client()}
	res, err := r.reconcileOllamaModel(context.Background(), model, provider)
	if err != nil {
		t.Fatalf("reconcileOllamaModel returned error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue after a transient probe error")
	}
	if c := atomic.LoadInt32(&pulls); c != 0 {
		t.Errorf("probe error must not trigger a pull; got %d pull(s)", c)
	}
	if model.Status.State != "Available" || !model.Status.Ready {
		t.Errorf("transient probe error must not downgrade state; got state=%q ready=%v", model.Status.State, model.Status.Ready)
	}
}

// When the provider answers and the model is genuinely absent, the reconcile
// pulls it. This is the legitimate counterpart to the probe-error case above.
// A TLS server is used deliberately: the default transport would reject the
// server's self-signed cert, so a passing pull assertion proves pullOllamaModel
// honors the injected r.HTTPClient (srv.Client) rather than a fresh client.
func TestReconcileOllamaModel_AbsentTriggersPull(t *testing.T) {
	var pulls int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"models":[]}`)) // model genuinely absent
		case "/api/pull":
			atomic.AddInt32(&pulls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	model := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen-8b", Namespace: "crew-x"},
		Spec:       aiv1alpha1.ModelSpec{Model: "qwen3:8b", ProviderRef: "ollama-gpu"},
	}
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "ollama-gpu", Namespace: "kubemoot"},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: srv.URL},
	}
	cl := fake.NewClientBuilder().
		WithScheme(modelTestScheme(t)).
		WithObjects(model).
		WithStatusSubresource(model).
		Build()
	r := &ModelReconciler{Client: cl, HTTPClient: srv.Client()}
	if _, err := r.reconcileOllamaModel(context.Background(), model, provider); err != nil {
		t.Fatalf("reconcileOllamaModel returned error: %v", err)
	}
	if c := atomic.LoadInt32(&pulls); c != 1 {
		t.Errorf("absent model should trigger exactly one pull; got %d", c)
	}
}
