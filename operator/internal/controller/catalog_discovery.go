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

// catalog_discovery.go reads what each engine says about its models for the
// catalog: Ollama through /api/tags and /api/show, vLLM through /v1/models and the
// models annotation the kubemoot-vllm chart puts on its Service.
package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine"
	"github.com/kubemoot/kubemoot/operator/internal/engine/ollama"
	"github.com/kubemoot/kubemoot/operator/internal/engine/vllm"
)

// AnnotationVLLMModels is the Service annotation the kubemoot-vllm chart sets: a
// JSON list of {name, parameters, capabilities}, one per model, so vLLM models
// are declared once, in the chart that starts the servers.
const AnnotationVLLMModels = "kubemoot.ai/models"

// declaredModel is one entry of the models annotation.
type declaredModel struct {
	Name         string   `json:"name"`
	Parameters   string   `json:"parameters,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// parseDeclaredModels reads the models annotation value; a value that is empty or
// not the expected JSON declares nothing.
func parseDeclaredModels(value string) map[string]declaredModel {
	out := map[string]declaredModel{}
	if value == "" {
		return out
	}
	var list []declaredModel
	if err := json.Unmarshal([]byte(value), &list); err != nil {
		return out
	}
	for _, d := range list {
		if d.Name != "" {
			out[d.Name] = d
		}
	}
	return out
}

// ollamaFacts lists the models an Ollama server has and what it says about each.
// /api/show runs once per model digest; its answer is kept in r.shows. A model
// whose /api/show fails keeps what /api/tags says.
func (r *ModelServerReconciler) ollamaFacts(ctx context.Context, httpClient *http.Client, endpoint string) ([]catalogFacts, error) {
	tags, err := ollama.ListTags(ctx, httpClient, endpoint)
	if err != nil {
		return nil, err
	}
	facts := make([]catalogFacts, 0, len(tags))
	live := make(map[string]bool, len(tags))
	for _, t := range tags {
		live[showKey(endpoint, t)] = true
		info, ok := r.showModel(ctx, httpClient, endpoint, t)
		facts = append(facts, factsFromOllama(t, info, ok))
	}
	r.pruneShows(endpoint, live)
	return facts, nil
}

// showKey identifies one model at one digest on one server.
func showKey(endpoint string, t ollama.ModelInfo) string {
	return endpoint + "|" + t.Name + "|" + t.Digest
}

// pruneShows forgets the shown models of the endpoint that the server no longer
// lists (a removed or re-pulled model), so the cache holds only what is live.
func (r *ModelServerReconciler) pruneShows(endpoint string, live map[string]bool) {
	r.shows.Range(func(k, _ any) bool {
		key := k.(string)
		if strings.HasPrefix(key, endpoint+"|") && !live[key] {
			r.shows.Delete(key)
		}
		return true
	})
}

// showModel is /api/show for one downloaded model, from the cache when the digest
// has been shown before.
func (r *ModelServerReconciler) showModel(ctx context.Context, httpClient *http.Client, endpoint string, t ollama.ModelInfo) (ollama.ShowInfo, bool) {
	key := showKey(endpoint, t)
	if cached, ok := r.shows.Load(key); ok {
		return cached.(ollama.ShowInfo), true
	}
	info, err := ollama.Show(ctx, httpClient, endpoint, t.Name)
	if err != nil {
		logf.FromContext(ctx).V(1).Info("Ollama /api/show failed; using /api/tags only", "model", t.Name, "error", err)
		return ollama.ShowInfo{}, false
	}
	if t.Digest != "" { // without a digest a re-pulled model could not be told from the old one
		r.shows.Store(key, info)
	}
	return info, true
}

// factsFromOllama combines a model's /api/tags entry with its /api/show answer
// (when haveShow) into catalog facts. The parameter count comes from, in order,
// /api/show's general.parameter_count, its details.parameter_size, and the
// details.parameter_size of /api/tags.
func factsFromOllama(t ollama.ModelInfo, info ollama.ShowInfo, haveShow bool) catalogFacts {
	f := catalogFacts{Name: t.Name, Present: true, Family: t.Details.Family, Source: aiv1alpha1.CatalogSourceNone}
	if haveShow {
		if info.Family != "" {
			f.Family = info.Family
		}
		f.ContextLength = info.ContextLength
		f.Capabilities = info.Capabilities
		f.Embedding = info.Embedding
		f.EmbeddingDimensions = info.EmbeddingLength
		f.ParameterCount = info.ParameterCount
		if f.ParameterCount == 0 {
			f.ParameterCount, _ = aiv1alpha1.ParseParameterCount(info.ParameterSize)
		}
		if f.ParameterCount > 0 {
			f.Source = aiv1alpha1.CatalogSourceOllamaShow
		}
	}
	if f.ParameterCount == 0 {
		if n, ok := aiv1alpha1.ParseParameterCount(t.Details.ParameterSize); ok {
			f.ParameterCount, f.Source = n, aiv1alpha1.CatalogSourceOllamaTags
		}
	}
	return f
}

// vllmFacts is what the server's vLLM servers say about their models: each
// served name, present when the probe found it answering, with the context window
// the server reports and the parameters and abilities the Service declares.
func (r *ModelServerReconciler) vllmFacts(ctx context.Context, httpClient *http.Client, server *aiv1alpha1.ModelServer, snap engine.Snapshot) []catalogFacts {
	declared := r.declaredVLLMModels(ctx, server)
	// A server that is restarting does not answer, but its model has not gone: a model
	// stays present while it was present before, so a restart does not drop it from the
	// catalog and roll the pods bound to it. A server that never answered is not present.
	answering := map[string]bool{}
	for _, e := range server.Status.Models {
		answering[e.Name] = e.Present
	}
	for _, a := range snap.Available {
		answering[a.Name] = true
	}
	servers := server.VLLMServers()
	contexts := servedContexts(ctx, httpClient, servers)
	facts := make([]catalogFacts, 0, len(servers))
	for _, srv := range servers {
		f := catalogFacts{Name: srv.Model, Present: answering[srv.Model], ContextLength: contexts[srv.Model], Source: aiv1alpha1.CatalogSourceNone}
		d, ok := declared[srv.Model]
		if ok {
			f.Capabilities = d.Capabilities
			if n, ok := aiv1alpha1.ParseParameterCount(d.Parameters); ok {
				f.ParameterCount, f.Source = n, aiv1alpha1.CatalogSourceService
			}
		}
		r.applyServedKind(ctx, httpClient, server, &f, srv.Endpoint, d)
		facts = append(facts, f)
	}
	return facts
}

// declaredVLLMModels reads the models annotation of the Service a vllm server
// names with spec.serviceRef; a server that lists its servers, or a Service that
// cannot be read, declares nothing.
func (r *ModelServerReconciler) declaredVLLMModels(ctx context.Context, server *aiv1alpha1.ModelServer) map[string]declaredModel {
	if server.Spec.ServiceRef == nil {
		return map[string]declaredModel{}
	}
	svc := &corev1.Service{}
	if err := r.Get(ctx, serviceKey(server), svc); err != nil {
		return map[string]declaredModel{}
	}
	return parseDeclaredModels(svc.Annotations[AnnotationVLLMModels])
}

// servedContexts is the context window, in tokens, each server reports for its
// served names, by served name. A server that does not answer contributes none.
func servedContexts(ctx context.Context, httpClient *http.Client, servers []aiv1alpha1.VLLMServer) map[string]int {
	out := map[string]int{}
	adapter := vllm.New(httpClient)
	seen := map[string]bool{}
	for _, srv := range servers {
		if seen[srv.Endpoint] {
			continue
		}
		seen[srv.Endpoint] = true
		served, err := adapter.ServedModels(ctx, srv.Endpoint)
		if err != nil {
			continue
		}
		for id, window := range vllm.ServedContexts(served, nil) {
			if window > 0 {
				out[id] = window
			}
		}
	}
	return out
}

// publishCatalog sets server.Status.Models from the engine's facts and the
// server's Model objects, and records whether every entry has a size. Callers
// skip it when the engine did not answer, which leaves the earlier catalog as it was.
func (r *ModelServerReconciler) publishCatalog(ctx context.Context, server *aiv1alpha1.ModelServer, facts []catalogFacts) {
	var all aiv1alpha1.ModelList
	if err := r.List(ctx, &all); err != nil {
		logf.FromContext(ctx).Info("could not list Models for the catalog", "server", server.Name, "err", err)
		return
	}
	server.Status.Models = buildCatalog(server, facts, catalogModels(server, all.Items))
	server.Status.EmbeddingModels = buildEmbeddingCatalog(facts)
	setCatalogSizeCondition(server)
}

// discoverOllamaCatalog publishes the catalog of an Ollama server. An Ollama that
// does not list its models right now leaves the earlier catalog in place.
func (r *ModelServerReconciler) discoverOllamaCatalog(ctx context.Context, server *aiv1alpha1.ModelServer, httpClient *http.Client) {
	facts, err := r.ollamaFacts(ctx, httpClient, server.Spec.Endpoint)
	if err != nil {
		logf.FromContext(ctx).V(1).Info("could not list the Ollama models for the catalog", "server", server.Name, "error", err)
		return
	}
	r.publishCatalog(ctx, server, facts)
}

// modelToServer maps a Model to the ModelServer it names in its own namespace,
// the server whose catalog the Model belongs to.
func modelToServer(_ context.Context, obj client.Object) []reconcile.Request {
	m, ok := obj.(*aiv1alpha1.Model)
	if !ok || m.Spec.ServerRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.ServerRef}}}
}
