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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// pullScenario serves /api/tags with an empty list until a pull has happened, then
// answers with tagsCodeAfterPull and tagsAfterPull, and counts the pulls.
func pullScenario(t *testing.T, tagsCodeAfterPull int, tagsAfterPull string) (*httptest.Server, *int32) {
	t.Helper()
	var pulls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case testPathAPITags:
			if atomic.LoadInt32(&pulls) == 0 {
				_, _ = w.Write([]byte(`{"models":[]}`))
				return
			}
			w.WriteHeader(tagsCodeAfterPull)
			_, _ = w.Write([]byte(tagsAfterPull))
		case testPathAPIPull:
			atomic.AddInt32(&pulls, 1)
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &pulls
}

// absentOllamaModel is a Model with no status yet and its Ollama provider at endpoint.
func absentOllamaModel(endpoint string) (*aiv1alpha1.Model, *aiv1alpha1.ModelProvider) {
	model := &aiv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: "qwen-8b", Namespace: testCrewNamespace},
		Spec:       aiv1alpha1.ModelSpec{Model: testModelID, ProviderRef: testOllamaGPU},
	}
	provider := &aiv1alpha1.ModelProvider{
		ObjectMeta: metav1.ObjectMeta{Name: testOllamaGPU, Namespace: testKubemoot},
		Spec:       aiv1alpha1.ModelProviderSpec{Type: aiv1alpha1.ProviderTypeOllama, Endpoint: endpoint},
	}
	return model, provider
}

func reconcileAbsentModel(t *testing.T, srv *httptest.Server) (*aiv1alpha1.Model, time.Duration) {
	t.Helper()
	model, provider := absentOllamaModel(srv.URL)
	cl := fake.NewClientBuilder().WithScheme(modelTestScheme(t)).WithObjects(model).WithStatusSubresource(model).Build()
	r := &ModelReconciler{Client: cl, HTTPClient: srv.Client()}
	res, err := r.reconcileOllamaModel(context.Background(), model, provider)
	if err != nil {
		t.Fatalf("reconcileOllamaModel: %v", err)
	}
	return model, res.RequeueAfter
}

// A pull that lands the model reports it Ready, with its info, in the same reconcile.
func TestReconcileAfterPull_ListedModelIsReady(t *testing.T) {
	srv, pulls := pullScenario(t, http.StatusOK,
		`{"models":[{"name":"`+testModelID+`","size":2048,"digest":"sha256:feed"}]}`)
	model, _ := reconcileAbsentModel(t, srv)
	if *pulls != 1 {
		t.Errorf("pulls = %d, want 1", *pulls)
	}
	if !model.Status.Ready || model.Status.State != stateAvailable {
		t.Errorf("status = %q ready=%v, want %q ready", model.Status.State, model.Status.Ready, stateAvailable)
	}
	cond := meta.FindStatusCondition(model.Status.Conditions, conditionTypeReady)
	if cond == nil || cond.Status != metav1.ConditionTrue || cond.Reason != stateAvailable {
		t.Errorf("Ready condition = %+v, want True with reason %s", cond, stateAvailable)
	}
	info := model.Status.ModelInfo
	if info == nil || info.Size != "2.0 KB" || info.Digest != "sha256:feed" {
		t.Errorf("ModelInfo = %+v, want size 2.0 KB and the digest", info)
	}
}

// A pull that reports success while the model never appears stays Pulling and
// polls on the Pulling interval instead of requeueing at once.
func TestReconcileAfterPull_UnlistedModelPollsAsPulling(t *testing.T) {
	srv, pulls := pullScenario(t, http.StatusOK, `{"models":[]}`)
	model, requeueAfter := reconcileAbsentModel(t, srv)
	assertPullingPoll(t, model, requeueAfter, *pulls, "not listed yet")
}

// A listing that fails after the pull keeps the model Pulling and names the error.
func TestReconcileAfterPull_ListingFailureStaysPulling(t *testing.T) {
	srv, pulls := pullScenario(t, http.StatusInternalServerError, `{}`)
	model, requeueAfter := reconcileAbsentModel(t, srv)
	assertPullingPoll(t, model, requeueAfter, *pulls, "listing the model failed: Ollama returned status 500")
}

func assertPullingPoll(t *testing.T, model *aiv1alpha1.Model, requeueAfter time.Duration, pulls int32, wantInMessage string) {
	t.Helper()
	if pulls != 1 {
		t.Errorf("pulls = %d, want 1", pulls)
	}
	if model.Status.Ready || model.Status.State != statePulling {
		t.Errorf("status = %q ready=%v, want %s not ready", model.Status.State, model.Status.Ready, statePulling)
	}
	if !strings.Contains(model.Status.Message, wantInMessage) {
		t.Errorf("message = %q, want it to contain %q", model.Status.Message, wantInMessage)
	}
	if requeueAfter < 10*time.Second {
		t.Errorf("RequeueAfter = %v, want the Pulling poll interval", requeueAfter)
	}
}
