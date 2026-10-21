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

// embeddingmodel_binding.go binds an EmbeddingModel that names no server to a
// ModelServer from the cluster's catalog. The crew names the model, because stored
// vectors match only the model that made them; the cluster supplies the server.
package controller

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// The Bound condition of an EmbeddingModel says which server serves it, or that
// none does.
const (
	conditionTypeBound = "Bound"
	reasonBoundCatalog = "Catalog"
	reasonBoundRef     = "ServerRef"
	reasonNoServer     = "NoServer"
	reasonServerGone   = "ServerNotFound"
)

// embeddingCandidate is a server that can serve an embedding model.
type embeddingCandidate struct {
	server *aiv1alpha1.ModelServer
	// present is true when the server lists the model as an embedding model now;
	// otherwise a Model object on the server asks for the model to be downloaded there.
	present bool
}

// latestTag is the form of a model name Ollama stores: a name with no tag is the
// "latest" tag.
func latestTag(name string) string {
	if !strings.Contains(name, ":") {
		return name + ":latest"
	}
	return name
}

// serverEmbeds reports whether the server lists the embedding model, and a reason
// when it lists it with a vector size other than the one the EmbeddingModel names.
func serverEmbeds(server *aiv1alpha1.ModelServer, em *aiv1alpha1.EmbeddingModel) (listed bool, mismatch string) {
	for _, e := range server.Status.EmbeddingModels {
		if latestTag(e.Name) != latestTag(em.Spec.Model) {
			continue
		}
		if e.Dimensions > 0 && em.Spec.Dimensions > 0 && e.Dimensions != em.Spec.Dimensions {
			return false, fmt.Sprintf("%s/%s reports %d dimensions, the EmbeddingModel names %d", server.Namespace, server.Name, e.Dimensions, em.Spec.Dimensions)
		}
		return true, ""
	}
	return false, ""
}

// managesModelFiles reports whether the server holds downloaded model files the
// operator can list and add to (Ollama). A vLLM server serves the model it was
// started with and has nothing to download.
func managesModelFiles(server *aiv1alpha1.ModelServer) bool {
	return server.Spec.Type == aiv1alpha1.ServerTypeOllama
}

// serverDeclares reports whether a Model object on the server asks for the model
// to be on it: the cluster owner's consent to download it there. It uses the
// catalog's own rule for which Model speaks for a model, so a disabled Model
// withdraws the request.
func serverDeclares(server *aiv1alpha1.ModelServer, tag string, models []aiv1alpha1.Model) bool {
	for name, m := range catalogModels(server, models) {
		if latestTag(name) == latestTag(tag) {
			return !m.Spec.Disabled
		}
	}
	return false
}

// embeddingCandidates are the ready servers, of any engine, that have the embedding model
// or, where the engine downloads models, are declared to hold it, in the order the operator prefers them: a server that
// lists the model now comes before one that is only declared; among equals, the
// server the model is already bound to stays first (so a second server does not
// move it), then namespace and name. The second return value explains each server
// skipped for its vector size.
func embeddingCandidates(em *aiv1alpha1.EmbeddingModel, servers []aiv1alpha1.ModelServer, models []aiv1alpha1.Model) ([]embeddingCandidate, []string) {
	var out []embeddingCandidate
	var skipped []string
	for i := range servers {
		s := &servers[i]
		if !s.Status.Ready || !s.DeletionTimestamp.IsZero() {
			continue
		}
		listed, mismatch := serverEmbeds(s, em)
		switch {
		case listed:
			out = append(out, embeddingCandidate{server: s, present: true})
		case mismatch != "":
			skipped = append(skipped, mismatch)
		case managesModelFiles(s) && serverDeclares(s, em.Spec.Model, models):
			out = append(out, embeddingCandidate{server: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return embeddingCandidateBefore(em, out[i], out[j]) })
	return out, skipped
}

// embeddingCandidateBefore orders candidates: present, then already bound, then by namespace
// and name.
func embeddingCandidateBefore(em *aiv1alpha1.EmbeddingModel, a, b embeddingCandidate) bool {
	if a.present != b.present {
		return a.present
	}
	aBound, bBound := a.server.Name == em.Status.BoundServer, b.server.Name == em.Status.BoundServer
	if aBound != bBound {
		return aBound
	}
	if a.server.Namespace != b.server.Namespace {
		return a.server.Namespace < b.server.Namespace
	}
	return a.server.Name < b.server.Name
}

// noServerMessage tells the cluster owner what to add when no server has the model.
func noServerMessage(em *aiv1alpha1.EmbeddingModel, skipped []string) string {
	msg := fmt.Sprintf("No ready ModelServer has embedding model %s. Serve it from a ModelServer (the catalog lists it once the server answers), or add a Model for %s to a server that downloads models (kmctl model add) and the operator downloads it there",
		em.Spec.Model, em.Spec.Model)
	if len(skipped) > 0 {
		msg += "; skipped for vector size: " + strings.Join(skipped, "; ")
	}
	return msg
}

// bindEmbeddingServer picks the server for an EmbeddingModel with no serverRef.
// It returns the server chosen, or nil with the message for the NoServer condition.
func (r *EmbeddingModelReconciler) bindEmbeddingServer(ctx context.Context, em *aiv1alpha1.EmbeddingModel) (*aiv1alpha1.ModelServer, string, error) {
	var servers aiv1alpha1.ModelServerList
	if err := r.List(ctx, &servers); err != nil {
		return nil, "", fmt.Errorf("failed to list ModelServers: %w", err)
	}
	var models aiv1alpha1.ModelList
	if err := r.List(ctx, &models); err != nil {
		return nil, "", fmt.Errorf("failed to list Models: %w", err)
	}
	cands, skipped := embeddingCandidates(em, servers.Items, models.Items)
	if len(cands) == 0 {
		return nil, noServerMessage(em, skipped), nil
	}
	return cands[0].server, "", nil
}

// recordBound sets the bound server and the Bound condition on the EmbeddingModel
// in memory; the status write that follows persists them. An empty server records
// that none is bound and clears the endpoint, so a consumer never keeps using a
// server the model has left.
func recordBound(em *aiv1alpha1.EmbeddingModel, server, reason, message string) {
	em.Status.BoundServer = server
	status := metav1.ConditionTrue
	if server == "" {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&em.Status.Conditions, metav1.Condition{
		Type: conditionTypeBound, Status: status, Reason: reason, Message: message,
	})
}

// embeddingPullAllowed reports whether the operator may download the model onto the
// server: always for an explicit serverRef, as before; for a catalog binding only
// where a Model object on the server asks for it.
func (r *EmbeddingModelReconciler) embeddingPullAllowed(ctx context.Context, em *aiv1alpha1.EmbeddingModel, server *aiv1alpha1.ModelServer) (bool, error) {
	if em.Spec.ServerRef != "" {
		return true, nil
	}
	var models aiv1alpha1.ModelList
	if err := r.List(ctx, &models); err != nil {
		return false, err
	}
	return serverDeclares(server, em.Spec.Model, models.Items), nil
}

// embeddingRelevantChange is true when a ModelServer update changes what an
// EmbeddingModel binds to: readiness, endpoint, type or the embedding models listed.
func embeddingRelevantChange(oldS, newS *aiv1alpha1.ModelServer) bool {
	return oldS.Status.Ready != newS.Status.Ready ||
		oldS.Spec.Endpoint != newS.Spec.Endpoint ||
		oldS.Spec.Type != newS.Spec.Type ||
		!reflect.DeepEqual(oldS.Spec.Servers, newS.Spec.Servers) ||
		!reflect.DeepEqual(oldS.Status.VLLMServers, newS.Status.VLLMServers) ||
		!reflect.DeepEqual(oldS.Status.EmbeddingModels, newS.Status.EmbeddingModels)
}

// modelServerEmbeddingPredicate lets a ModelServer through when it is created or
// deleted, or when an update changes what an EmbeddingModel binds to.
func modelServerEmbeddingPredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, ok1 := e.ObjectOld.(*aiv1alpha1.ModelServer)
			n, ok2 := e.ObjectNew.(*aiv1alpha1.ModelServer)
			return !ok1 || !ok2 || embeddingRelevantChange(o, n)
		},
	}
}

// enqueueUnboundEmbeddingModels maps a ModelServer change to the EmbeddingModels
// that choose their server from the catalog, so they re-bind at once.
func (r *EmbeddingModelReconciler) enqueueUnboundEmbeddingModels(ctx context.Context, _ client.Object) []reconcile.Request {
	var list aiv1alpha1.EmbeddingModelList
	if err := r.List(ctx, &list); err != nil {
		logf.FromContext(ctx).Error(err, "could not list EmbeddingModels to re-bind")
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if e := &list.Items[i]; e.Spec.ServerRef == "" {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: e.Namespace, Name: e.Name}})
		}
	}
	return reqs
}
