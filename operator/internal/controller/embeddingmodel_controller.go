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
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/engine/ollama"
)

const embeddingModelFinalizer = "kubemoot.ai/embeddingmodel-finalizer"

// EmbeddingModelReconciler reconciles an EmbeddingModel object
type EmbeddingModelReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client

	// Pulls is the download tracker shared with the Model controller; a reconciler
	// without one uses a private tracker.
	Pulls *PullTracker
}

// tracker returns the shared pull tracker, or a private one created on first use.
func (r *EmbeddingModelReconciler) tracker() *PullTracker {
	return trackerOrNew(&r.Pulls)
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=embeddingmodels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=embeddingmodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=embeddingmodels/finalizers,verbs=update

// Reconcile handles EmbeddingModel reconciliation
func (r *EmbeddingModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the EmbeddingModel instance
	embeddingModel := &kubemootv1alpha1.EmbeddingModel{}
	if err := r.Get(ctx, req.NamespacedName, embeddingModel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling EmbeddingModel", "name", embeddingModel.Name, "model", embeddingModel.Spec.Model)

	// Handle deletion
	if !embeddingModel.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, embeddingModel)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(embeddingModel, embeddingModelFinalizer) {
		if err := addFinalizer(ctx, r.Client, embeddingModel, embeddingModelFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	}

	server, result, err := r.resolveServer(ctx, embeddingModel)
	if server == nil {
		return result, err
	}

	return r.reconcileOnServer(ctx, embeddingModel, server)
}

// reconcileOnServer reports the embedding model Available. A server that holds
// downloaded model files (Ollama) is asked directly, because only it can tell a model
// it has from one its catalog still lists, and it can download a missing one. Any
// other server serves the models it was started with, so the model is Available
// when the server's catalog lists it as an embedding model.
func (r *EmbeddingModelReconciler) reconcileOnServer(ctx context.Context, em *kubemootv1alpha1.EmbeddingModel, server *kubemootv1alpha1.ModelServer) (ctrl.Result, error) {
	if managesModelFiles(server) {
		return r.reconcileOllamaEmbedding(ctx, em, server)
	}
	listed, mismatch := serverEmbeds(server, em)
	switch {
	case listed:
		return r.reportServed(ctx, em, server)
	case mismatch != "":
		return r.updateStatus(ctx, em, "Error", mismatch)
	}
	return r.updateStatus(ctx, em, statePending,
		fmt.Sprintf("ModelServer %s does not serve %s as an embedding model; serve it there and the catalog lists it", server.Name, em.Spec.Model))
}

// reportServed records an embedding model the server's catalog lists as Available,
// with the vector size the catalog reports.
func (r *EmbeddingModelReconciler) reportServed(ctx context.Context, em *kubemootv1alpha1.EmbeddingModel, server *kubemootv1alpha1.ModelServer) (ctrl.Result, error) {
	key := pullKey(server.Name, em.Spec.Model)
	if err := releaseStalePulls(ctx, r.Client, r.tracker(), em, key); err != nil {
		return ctrl.Result{}, err
	}
	r.tracker().forget(key)
	info := &kubemootv1alpha1.EmbeddingModelInfo{Dimensions: em.Spec.Dimensions}
	for _, e := range server.Status.EmbeddingModels {
		if latestTag(e.Name) == latestTag(em.Spec.Model) && e.Dimensions > 0 {
			info.Dimensions = e.Dimensions
		}
	}
	return r.markAvailable(ctx, em, server.EndpointFor(em.Spec.Model), info)
}

// resolveServer finds the ModelServer that serves the embedding model: the one
// spec.serverRef names, or, with no serverRef, one the catalog supplies. It returns
// a nil server with the reconcile result when none can serve it yet.
func (r *EmbeddingModelReconciler) resolveServer(ctx context.Context, em *kubemootv1alpha1.EmbeddingModel) (*kubemootv1alpha1.ModelServer, ctrl.Result, error) {
	if em.Spec.ServerRef == "" {
		return r.resolveCatalogServer(ctx, em)
	}
	server, err := r.findModelServer(ctx, em.Spec.ServerRef)
	if err != nil {
		message := fmt.Sprintf("ModelServer '%s' not found", em.Spec.ServerRef)
		recordBound(em, "", reasonServerGone, message)
		result, err := r.updateStatus(ctx, em, "Error", message)
		return nil, result, err
	}
	recordBound(em, server.Name, reasonBoundRef, fmt.Sprintf("Using ModelServer %s named by spec.serverRef", server.Name))
	if !server.Status.Ready {
		result, err := r.updateStatus(ctx, em, statePending, fmt.Sprintf("Waiting for ModelServer '%s' to be ready", server.Name))
		return nil, result, err
	}
	return server, ctrl.Result{}, nil
}

// resolveCatalogServer binds an EmbeddingModel with no serverRef to a ready server
// whose catalog lists the model. With none, the Bound condition is False
// (NoServer) naming the model, and the endpoint is cleared so a consumer does not
// keep using a server the model has left.
func (r *EmbeddingModelReconciler) resolveCatalogServer(ctx context.Context, em *kubemootv1alpha1.EmbeddingModel) (*kubemootv1alpha1.ModelServer, ctrl.Result, error) {
	server, message, err := r.bindEmbeddingServer(ctx, em)
	if err != nil {
		return nil, ctrl.Result{}, err
	}
	if server == nil {
		recordBound(em, "", reasonNoServer, message)
		em.Status.Endpoint = ""
		if err := releaseStalePulls(ctx, r.Client, r.tracker(), em, ""); err != nil {
			return nil, ctrl.Result{}, err
		}
		result, err := r.updateStatus(ctx, em, statePending, message)
		return nil, result, err
	}
	if previous := em.Status.BoundServer; previous != server.Name {
		logf.FromContext(ctx).Info("Binding EmbeddingModel to ModelServer", "model", em.Spec.Model, "from", previous, "to", server.Name)
	}
	recordBound(em, server.Name, reasonBoundCatalog, fmt.Sprintf("Bound to ModelServer %s/%s, which has embedding model %s", server.Namespace, server.Name, em.Spec.Model))
	return server, ctrl.Result{}, nil
}

// reconcileOllamaEmbedding handles Ollama-based embedding models
func (r *EmbeddingModelReconciler) reconcileOllamaEmbedding(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, server *kubemootv1alpha1.ModelServer) (ctrl.Result, error) {
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	key := pullKey(server.Name, embeddingModel.Spec.Model)
	if err := releaseStalePulls(ctx, r.Client, r.tracker(), embeddingModel, key); err != nil {
		return ctrl.Result{}, err
	}

	// Check if model exists in Ollama
	exists, exact, err := r.ollamaModelExists(ctx, httpClient, server.Spec.Endpoint, embeddingModel.Spec.Model)
	if err != nil {
		return r.updateStatus(ctx, embeddingModel, "Error",
			fmt.Sprintf("Failed to check model: %v", err))
	}

	if !exact && embeddingModel.Spec.ServerRef == "" {
		// A model bound from the catalog is the exact tag or it is not on the server: a
		// different tag of the same name makes different vectors.
		exists = false
	}
	if !exists {
		return r.pullOrWait(ctx, httpClient, embeddingModel, server)
	}
	if exact {
		// Only the exact tag ends a pull; a base-name match may be a different tag.
		r.tracker().forget(key)
	}
	return r.reportAvailable(ctx, httpClient, embeddingModel, server.Spec.Endpoint)
}

// pullOrWait downloads the model onto the server when the cluster owner allows it
// there. A catalog-bound model the server no longer lists, and no Model asks for,
// waits for the catalog to catch up, which re-binds it.
func (r *EmbeddingModelReconciler) pullOrWait(ctx context.Context, httpClient *http.Client, em *kubemootv1alpha1.EmbeddingModel, server *kubemootv1alpha1.ModelServer) (ctrl.Result, error) {
	allowed, err := r.embeddingPullAllowed(ctx, em, server)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !allowed {
		return r.updateStatus(ctx, em, statePending,
			fmt.Sprintf("ModelServer %s no longer lists %s; waiting for the catalog to bind another server", server.Name, em.Spec.Model))
	}
	return r.reconcilePull(ctx, httpClient, em, server)
}

// reportAvailable records an embedding model the server lists as Available.
func (r *EmbeddingModelReconciler) reportAvailable(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, endpoint string) (ctrl.Result, error) {
	// Model exists - get info and mark as available
	modelInfo, err := r.getOllamaModelInfo(ctx, httpClient, endpoint, embeddingModel.Spec.Model)
	if err != nil {
		logf.FromContext(ctx).Error(err, "Failed to get model info, continuing anyway")
		// Use spec dimensions if we couldn't get info
		modelInfo = &kubemootv1alpha1.EmbeddingModelInfo{Dimensions: embeddingModel.Spec.Dimensions}
	}
	return r.markAvailable(ctx, embeddingModel, endpoint, modelInfo)
}

// markAvailable writes the Available status: the endpoint a consumer embeds through
// and the model's info.
func (r *EmbeddingModelReconciler) markAvailable(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, endpoint string, info *kubemootv1alpha1.EmbeddingModelInfo) (ctrl.Result, error) {
	embeddingModel.Status.State = stateAvailable
	embeddingModel.Status.Ready = true
	embeddingModel.Status.Message = fmt.Sprintf("Embedding model %s is available", embeddingModel.Spec.Model)
	embeddingModel.Status.Endpoint = endpoint // Set endpoint for RAGSource to use
	embeddingModel.Status.Pull = nil
	embeddingModel.Status.ModelInfo = info

	meta.SetStatusCondition(&embeddingModel.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             stateAvailable,
		Message:            embeddingModel.Status.Message,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, embeddingModel); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to update EmbeddingModel status")
		return ctrl.Result{}, err
	}

	// Requeue to check periodically
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// ollamaModelExists reports whether Ollama lists the model, by exact tag or by base
// name; exact is true only for the exact tag.
func (r *EmbeddingModelReconciler) ollamaModelExists(ctx context.Context, httpClient *http.Client, endpoint, modelName string) (exists, exact bool, err error) {
	models, err := listOllamaTags(ctx, httpClient, endpoint)
	if err != nil {
		return false, false, err
	}
	for _, m := range models {
		if latestTag(m.Name) == latestTag(modelName) {
			return true, true, nil
		}
		if strings.Split(m.Name, ":")[0] == strings.Split(modelName, ":")[0] {
			exists = true
		}
	}
	return exists, false, nil
}

// findModelServer looks up a ModelServer by name across all namespaces.
func (r *EmbeddingModelReconciler) findModelServer(ctx context.Context, name string) (*kubemootv1alpha1.ModelServer, error) {
	serverList := &kubemootv1alpha1.ModelServerList{}
	if err := r.List(ctx, serverList); err != nil {
		return nil, fmt.Errorf("failed to list ModelServers: %w", err)
	}
	for i := range serverList.Items {
		if serverList.Items[i].Name == name {
			return &serverList.Items[i], nil
		}
	}
	return nil, fmt.Errorf("ModelServer %q not found in any namespace", name)
}

// reconcilePull drives the download of an embedding model the server does not
// list, on the same background pull the Model controller uses: the reconcile
// returns at once and reports the progress of the pull.
func (r *EmbeddingModelReconciler) reconcilePull(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, server *kubemootv1alpha1.ModelServer) (ctrl.Result, error) {
	key := pullKey(server.Name, embeddingModel.Spec.Model)
	step := r.tracker().advance(key, embeddingModel.UID, newPullStream(server, embeddingModel.Spec.Model, httpClient))
	switch step.phase {
	case pullSucceeded:
		return r.reportAfterPull(ctx, httpClient, embeddingModel, server.Spec.Endpoint)
	case pullFailed:
		return r.updateStatus(ctx, embeddingModel, "Error", step.err.Error())
	}
	embeddingModel.Status.Pull = step.progress
	return r.updateStatus(ctx, embeddingModel, statePulling, pullMessage(step.progress))
}

// reportAfterPull acts on what the server lists once a pull has finished: a
// listed model is reported Available, otherwise the model stays Pulling.
func (r *EmbeddingModelReconciler) reportAfterPull(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, endpoint string) (ctrl.Result, error) {
	if exists, _, err := r.ollamaModelExists(ctx, httpClient, endpoint, embeddingModel.Spec.Model); err == nil && exists {
		return r.reportAvailable(ctx, httpClient, embeddingModel, endpoint)
	}
	return r.updateStatus(ctx, embeddingModel, statePulling,
		fmt.Sprintf("Pull of %s finished; waiting for the server to list it", embeddingModel.Spec.Model))
}

// getOllamaModelInfo retrieves model metadata from Ollama
func (r *EmbeddingModelReconciler) getOllamaModelInfo(ctx context.Context, httpClient *http.Client, endpoint, modelName string) (*kubemootv1alpha1.EmbeddingModelInfo, error) {
	shown, err := ollama.Show(ctx, httpClient, endpoint, modelName)
	if err != nil {
		return nil, err
	}
	return &kubemootv1alpha1.EmbeddingModelInfo{Family: shown.Family, Dimensions: shown.EmbeddingLength}, nil
}

// handleDeletion handles EmbeddingModel deletion
func (r *EmbeddingModelReconciler) handleDeletion(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(embeddingModel, embeddingModelFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling EmbeddingModel deletion", "name", embeddingModel.Name)

	// The model stays in Ollama as other consumers may share it. A download still
	// running is stopped unless another Model or EmbeddingModel wants the tag.
	if server := embeddingModel.BoundServerName(); server != "" {
		if err := abandonIfUnwanted(ctx, r.Client, r.tracker(), embeddingModel,
			pullKey(server, embeddingModel.Spec.Model)); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Remove finalizer
	if err := removeFinalizer(ctx, r.Client, embeddingModel, embeddingModelFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("EmbeddingModel deleted successfully", "name", embeddingModel.Name)
	return ctrl.Result{}, nil
}

// updateStatus records a not-ready state on the EmbeddingModel (the ready path
// sets its own status in Reconcile) and requeues: 10s while pulling, 30s otherwise.
func (r *EmbeddingModelReconciler) updateStatus(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, state string, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	embeddingModel.Status.State = state
	embeddingModel.Status.Ready = false
	embeddingModel.Status.Message = message
	if state != statePulling {
		embeddingModel.Status.Pull = nil
	}

	meta.SetStatusCondition(&embeddingModel.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             state,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, embeddingModel); err != nil {
		log.Error(err, "Failed to update EmbeddingModel status")
		return ctrl.Result{}, err
	}

	if state == statePulling {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EmbeddingModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.EmbeddingModel{}).
		Named("embeddingmodel").
		// A ModelServer that gains, loses or stops listing an embedding model
		// re-binds the EmbeddingModels that choose their server from the catalog.
		Watches(&kubemootv1alpha1.ModelServer{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueUnboundEmbeddingModels),
			builder.WithPredicates(modelServerEmbeddingPredicate())).
		Complete(r)
}
