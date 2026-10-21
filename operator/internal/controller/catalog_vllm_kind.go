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

// catalog_vllm_kind.go tells a vLLM served model that embeds from one that chats.
// vLLM's /v1/models does not say which a model is, so the operator asks the model
// itself: a model served for embeddings answers /v1/embeddings with a vector, a chat
// model refuses it. The answer also gives the vector size. A model's answer is kept
// until the model leaves the server, so a model is asked once, not on every probe.
package controller

import (
	"context"
	"errors"
	"net/http"
	"slices"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/embedding"
)

// capabilityEmbedding is the ability the models annotation declares for a model
// served for embeddings.
const capabilityEmbedding = "embedding"

// servedKind is what a served model is: an embedding model with a vector size, or not.
type servedKind struct {
	embedding  bool
	dimensions int32
}

// applyServedKind sets the embedding facts of a served model: asked once while it is
// present, forgotten when it is not.
func (r *ModelServerReconciler) applyServedKind(ctx context.Context, httpClient *http.Client, server *aiv1alpha1.ModelServer, f *catalogFacts, endpoint string, declared declaredModel) {
	key := endpoint + "|" + f.Name
	if !f.Present {
		r.servedKinds.Delete(key)
		return
	}
	kind, ok := r.servedKinds.Load(key)
	if !ok {
		probed, definite := probeServedKind(ctx, httpClient, endpoint, f.Name)
		if !definite {
			probed = previousKind(server, f.Name)
		} else {
			r.servedKinds.Store(key, probed)
		}
		kind = probed
	}
	k := kind.(servedKind)
	if slices.Contains(declared.Capabilities, capabilityEmbedding) && !k.embedding {
		k.embedding = true
	}
	f.Embedding, f.EmbeddingDimensions = k.embedding, k.dimensions
}

// probeServedKind asks the model for an embedding of a short text. definite is false
// when the server could not answer, in which case nothing is learned.
func probeServedKind(ctx context.Context, httpClient *http.Client, endpoint, model string) (servedKind, bool) {
	vec, err := embedding.Embed(ctx, httpClient, endpoint, model, "kind")
	switch {
	case err == nil:
		return servedKind{embedding: true, dimensions: int32(len(vec))}, true
	case errors.Is(err, embedding.ErrNotEmbedding):
		return servedKind{}, true
	}
	logf.FromContext(ctx).V(1).Info("could not ask the vLLM model whether it embeds", "model", model, "error", err)
	return servedKind{}, false
}

// previousKind is what the server's last published catalog said about the model, for
// a probe that got no answer: a model that embedded a moment ago still does.
func previousKind(server *aiv1alpha1.ModelServer, name string) servedKind {
	for _, e := range server.Status.EmbeddingModels {
		if e.Name == name {
			return servedKind{embedding: true, dimensions: e.Dimensions}
		}
	}
	return servedKind{}
}
