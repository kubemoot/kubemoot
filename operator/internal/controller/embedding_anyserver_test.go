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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
)

const (
	anyEmbedModel = "embed-m"
	anyChatModel  = "chat-m"
)

// vllmEmbedServer is a vLLM server that serves anyEmbedModel for embeddings (3
// dimensions, not normalized) and refuses anyChatModel, as vLLM does for a chat
// model. It counts the embedding calls and answers 503 while down is set.
type vllmEmbedServer struct {
	srv   *httptest.Server
	calls atomic.Int32
	down  atomic.Bool
	// refuse makes every model answer 400, as vLLM does while a model is loading.
	refuse atomic.Bool
}

func newVLLMEmbedServer(t *testing.T) *vllmEmbedServer {
	t.Helper()
	v := &vllmEmbedServer{}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		v.calls.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case v.down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
		case v.refuse.Load():
			w.WriteHeader(http.StatusBadRequest)
		case body.Model == anyEmbedModel:
			_, _ = w.Write([]byte(`{"data":[{"embedding":[3,0,4]}]}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Model does not support Embeddings API"}}`))
		}
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func vllmFactsFor(t *testing.T, r *ModelServerReconciler, v *vllmEmbedServer, prior []aiv1alpha1.EmbeddingCatalogModel, models ...string) map[string]catalogFacts {
	t.Helper()
	server := &aiv1alpha1.ModelServer{Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM}}
	server.Status.EmbeddingModels = prior
	var snap engine.Snapshot
	for _, m := range models {
		server.Spec.Servers = append(server.Spec.Servers, aiv1alpha1.VLLMServer{Model: m, Endpoint: v.srv.URL})
		snap.Available = append(snap.Available, aiv1alpha1.AvailableModel{Name: m})
	}
	out := map[string]catalogFacts{}
	for _, f := range r.vllmFacts(context.Background(), v.srv.Client(), server, snap) {
		out[f.Name] = f
	}
	return out
}

func TestVLLMFactsListEmbeddingModelsWithTheirKindAndSize(t *testing.T) {
	v := newVLLMEmbedServer(t)
	facts := vllmFactsFor(t, &ModelServerReconciler{}, v, nil, anyEmbedModel, anyChatModel)

	if e := facts[anyEmbedModel]; !e.Embedding || e.EmbeddingDimensions != 3 {
		t.Errorf("embedding model facts = %+v, want an embedding model of 3 dimensions", e)
	}
	if c := facts[anyChatModel]; c.Embedding {
		t.Errorf("chat model facts = %+v, want a chat model", c)
	}
	if got := buildEmbeddingCatalog([]catalogFacts{facts[anyEmbedModel], facts[anyChatModel]}); len(got) != 1 || got[0].Name != anyEmbedModel {
		t.Errorf("embedding catalog = %+v, want only %s", got, anyEmbedModel)
	}
}

func TestVLLMFactsAskEachModelOnce(t *testing.T) {
	v := newVLLMEmbedServer(t)
	r := &ModelServerReconciler{}
	for range 4 {
		vllmFactsFor(t, r, v, nil, anyEmbedModel, anyChatModel)
	}
	if got := v.calls.Load(); got != 3 {
		t.Errorf("embedding calls = %d, want 3 (one for the embedding model, two to settle the chat model, then none)", got)
	}
}

func TestVLLMFactsDoNotPinAModelAsChatOnOneRefusal(t *testing.T) {
	v := newVLLMEmbedServer(t)
	r := &ModelServerReconciler{}
	v.refuse.Store(true) // the model is still loading
	if e := vllmFactsFor(t, r, v, nil, anyEmbedModel)[anyEmbedModel]; e.Embedding {
		t.Fatalf("facts = %+v, want not embedding while the refusal lasts", e)
	}
	v.refuse.Store(false)
	if e := vllmFactsFor(t, r, v, nil, anyEmbedModel)[anyEmbedModel]; !e.Embedding || e.EmbeddingDimensions == 0 {
		t.Errorf("facts = %+v, want the model found once it answers after a single refusal", e)
	}
}

func TestVLLMFactsKeepAnEmbeddingModelWhenTheServerCannotAnswerNow(t *testing.T) {
	v := newVLLMEmbedServer(t)
	v.down.Store(true)
	prior := []aiv1alpha1.EmbeddingCatalogModel{{Name: anyEmbedModel, Dimensions: 3}}
	r := &ModelServerReconciler{}
	facts := vllmFactsFor(t, r, v, prior, anyEmbedModel, anyChatModel)

	if e := facts[anyEmbedModel]; !e.Embedding || e.EmbeddingDimensions != 3 {
		t.Errorf("facts = %+v, want the earlier answer kept while the server is down", e)
	}
	if facts[anyChatModel].Embedding {
		t.Error("a model never known to embed is not one because the server is down")
	}
	v.down.Store(false)
	if e := vllmFactsFor(t, r, v, nil, anyChatModel)[anyChatModel]; e.Embedding {
		t.Errorf("a failed ask must not be remembered: %+v", e)
	}
}

func TestVLLMFactsDoNotAskModelsThatAreNotAnswering(t *testing.T) {
	v := newVLLMEmbedServer(t)
	server := &aiv1alpha1.ModelServer{Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM,
		Servers: []aiv1alpha1.VLLMServer{{Model: anyEmbedModel, Endpoint: v.srv.URL}}}}
	facts := (&ModelServerReconciler{}).vllmFacts(context.Background(), v.srv.Client(), server, engine.Snapshot{})
	if v.calls.Load() != 0 || facts[0].Embedding {
		t.Errorf("calls = %d facts = %+v, want no question for a model that does not answer", v.calls.Load(), facts)
	}
}

func TestVLLMDeclaredEmbeddingCapabilityMakesAnEmbeddingModel(t *testing.T) {
	v := newVLLMEmbedServer(t)
	r := &ModelServerReconciler{}
	f := catalogFacts{Name: anyChatModel, Present: true}
	r.applyServedKind(context.Background(), v.srv.Client(), &aiv1alpha1.ModelServer{}, &f, v.srv.URL,
		declaredModel{Name: anyChatModel, Capabilities: []string{capabilityEmbedding}})
	if !f.Embedding {
		t.Errorf("facts = %+v, want the declared embedding capability honored", f)
	}
}

// vllmEmbedServerObject is a ready vLLM ModelServer whose catalog lists the embedding model.
func vllmEmbedServerObject(name string, listed bool) *aiv1alpha1.ModelServer {
	s := &aiv1alpha1.ModelServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testKubemoot},
		Spec: aiv1alpha1.ModelServerSpec{Type: aiv1alpha1.ServerTypeVLLM,
			Servers: []aiv1alpha1.VLLMServer{{Model: embedTestModel, Endpoint: "http://vllm-" + name + ":8000"}}},
		Status: aiv1alpha1.ModelServerStatus{Ready: true},
	}
	if listed {
		s.Status.EmbeddingModels = []aiv1alpha1.EmbeddingCatalogModel{{Name: embedTestModel, Dimensions: embedTestDims}}
	}
	return s
}

func TestEmbeddingModelBindsToAVLLMServerThatServesIt(t *testing.T) {
	h := newBindHarness(t, unboundEmbedding(), vllmEmbedServerObject("v-embed", true))

	got := h.reconcile()

	if !got.Status.Ready || got.Status.BoundServer != "v-embed" || got.Status.Endpoint != "http://vllm-v-embed:8000" {
		t.Fatalf("status = %+v, want Available on v-embed at its vLLM endpoint", got.Status)
	}
	if got.Status.ModelInfo == nil || got.Status.ModelInfo.Dimensions != embedTestDims {
		t.Errorf("model info = %+v, want %d dimensions from the catalog", got.Status.ModelInfo, embedTestDims)
	}
}

func TestEmbeddingModelDoesNotBindToAChatOnlyVLLMServer(t *testing.T) {
	h := newBindHarness(t, unboundEmbedding(), vllmEmbedServerObject("v-chat", false))

	got := h.reconcile()

	c := boundCondition(t, got)
	if got.Status.Ready || got.Status.BoundServer != "" || c.Reason != reasonNoServer {
		t.Errorf("status = %+v condition = %+v, want NoServer", got.Status, c)
	}
}

func TestEmbeddingModelNamingAChatOnlyVLLMServerWaitsForIt(t *testing.T) {
	em := unboundEmbedding()
	em.Spec.ServerRef = "v-chat"
	h := newBindHarness(t, em, vllmEmbedServerObject("v-chat", false))

	got := h.reconcile()

	if got.Status.Ready || got.Status.State != statePending || !strings.Contains(got.Status.Message, "does not serve") {
		t.Errorf("status = %+v, want Pending because the server does not serve the model", got.Status)
	}
}

func TestEmbeddingModelDimensionMismatchOnAVLLMServerIsAnError(t *testing.T) {
	em := unboundEmbedding()
	em.Spec.ServerRef = "v-embed"
	em.Spec.Dimensions = 1024
	h := newBindHarness(t, em, vllmEmbedServerObject("v-embed", true))

	got := h.reconcile()

	if got.Status.Ready || got.Status.State != phaseError || !strings.Contains(got.Status.Message, "1024") {
		t.Errorf("status = %+v, want an Error naming the 1024 dimensions", got.Status)
	}
}
