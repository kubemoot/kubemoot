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
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
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

	// Fetch the referenced ModelProvider (cluster-wide — providers are infrastructure)
	provider, err := r.findModelProvider(ctx, embeddingModel.Spec.ProviderRef)
	if err != nil {
		return r.updateStatus(ctx, embeddingModel, "Error",
			fmt.Sprintf("ModelProvider '%s' not found", embeddingModel.Spec.ProviderRef))
	}

	// Check if provider is ready
	if !provider.Status.Ready {
		return r.updateStatus(ctx, embeddingModel, "Pending",
			fmt.Sprintf("Waiting for ModelProvider '%s' to be ready", provider.Name))
	}

	if provider.Spec.Type != kubemootv1alpha1.ProviderTypeOllama {
		return r.updateStatus(ctx, embeddingModel, "Error", kubemootv1alpha1.UnsupportedProviderTypeMessage)
	}
	return r.reconcileOllamaEmbedding(ctx, embeddingModel, provider)
}

// reconcileOllamaEmbedding handles Ollama-based embedding models
func (r *EmbeddingModelReconciler) reconcileOllamaEmbedding(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, provider *kubemootv1alpha1.ModelProvider) (ctrl.Result, error) {
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	key := pullKey(embeddingModel.Spec.ProviderRef, embeddingModel.Spec.Model)
	if err := releaseStalePulls(ctx, r.Client, r.tracker(), embeddingModel, key); err != nil {
		return ctrl.Result{}, err
	}

	// Check if model exists in Ollama
	exists, exact, err := r.ollamaModelExists(ctx, httpClient, provider.Spec.Endpoint, embeddingModel.Spec.Model)
	if err != nil {
		return r.updateStatus(ctx, embeddingModel, "Error",
			fmt.Sprintf("Failed to check model: %v", err))
	}

	if !exists {
		return r.reconcilePull(ctx, httpClient, embeddingModel, provider)
	}
	if exact {
		// Only the exact tag ends a pull; a base-name match may be a different tag.
		r.tracker().forget(key)
	}
	return r.reportAvailable(ctx, httpClient, embeddingModel, provider.Spec.Endpoint)
}

// reportAvailable records an embedding model the provider lists as Available.
func (r *EmbeddingModelReconciler) reportAvailable(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, endpoint string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Model exists - get info and mark as available
	modelInfo, err := r.getOllamaModelInfo(httpClient, endpoint, embeddingModel.Spec.Model)
	if err != nil {
		log.Error(err, "Failed to get model info, continuing anyway")
	}

	// Update status with model info
	embeddingModel.Status.State = stateAvailable
	embeddingModel.Status.Ready = true
	embeddingModel.Status.Message = fmt.Sprintf("Embedding model %s is available", embeddingModel.Spec.Model)
	embeddingModel.Status.Endpoint = endpoint // Set endpoint for RAGSource to use
	embeddingModel.Status.Pull = nil

	if modelInfo != nil {
		embeddingModel.Status.ModelInfo = modelInfo
	} else {
		// Use spec dimensions if we couldn't get info
		embeddingModel.Status.ModelInfo = &kubemootv1alpha1.EmbeddingModelInfo{
			Dimensions: embeddingModel.Spec.Dimensions,
		}
	}

	// Set condition
	condition := metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionTrue,
		Reason:             stateAvailable,
		Message:            embeddingModel.Status.Message,
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&embeddingModel.Status.Conditions, condition)

	if err := r.Status().Update(ctx, embeddingModel); err != nil {
		log.Error(err, "Failed to update EmbeddingModel status")
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
		if m.Name == modelName {
			return true, true, nil
		}
		if strings.Split(m.Name, ":")[0] == strings.Split(modelName, ":")[0] {
			exists = true
		}
	}
	return exists, false, nil
}

// findModelProvider looks up a ModelProvider by name across all namespaces.
func (r *EmbeddingModelReconciler) findModelProvider(ctx context.Context, name string) (*kubemootv1alpha1.ModelProvider, error) {
	providerList := &kubemootv1alpha1.ModelProviderList{}
	if err := r.List(ctx, providerList); err != nil {
		return nil, fmt.Errorf("failed to list ModelProviders: %w", err)
	}
	for i := range providerList.Items {
		if providerList.Items[i].Name == name {
			return &providerList.Items[i], nil
		}
	}
	return nil, fmt.Errorf("ModelProvider %q not found in any namespace", name)
}

// reconcilePull drives the download of an embedding model the provider does not
// list, on the same background pull the Model controller uses: the reconcile
// returns at once and reports the progress of the pull.
func (r *EmbeddingModelReconciler) reconcilePull(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, provider *kubemootv1alpha1.ModelProvider) (ctrl.Result, error) {
	key := pullKey(embeddingModel.Spec.ProviderRef, embeddingModel.Spec.Model)
	step := r.tracker().advance(key, embeddingModel.UID, newPullStream(provider, embeddingModel.Spec.Model, httpClient))
	switch step.phase {
	case pullSucceeded:
		return r.reportAfterPull(ctx, httpClient, embeddingModel, provider.Spec.Endpoint)
	case pullFailed:
		return r.updateStatus(ctx, embeddingModel, "Error", step.err.Error())
	}
	embeddingModel.Status.Pull = step.progress
	return r.updateStatus(ctx, embeddingModel, statePulling, pullMessage(step.progress))
}

// reportAfterPull acts on what the provider lists once a pull has finished: a
// listed model is reported Available, otherwise the model stays Pulling.
func (r *EmbeddingModelReconciler) reportAfterPull(ctx context.Context, httpClient *http.Client, embeddingModel *kubemootv1alpha1.EmbeddingModel, endpoint string) (ctrl.Result, error) {
	if exists, _, err := r.ollamaModelExists(ctx, httpClient, endpoint, embeddingModel.Spec.Model); err == nil && exists {
		return r.reportAvailable(ctx, httpClient, embeddingModel, endpoint)
	}
	return r.updateStatus(ctx, embeddingModel, statePulling,
		fmt.Sprintf("Pull of %s finished; waiting for the provider to list it", embeddingModel.Spec.Model))
}

// getOllamaModelInfo retrieves model metadata from Ollama
func (r *EmbeddingModelReconciler) getOllamaModelInfo(httpClient *http.Client, endpoint, modelName string) (*kubemootv1alpha1.EmbeddingModelInfo, error) {
	reqBody := fmt.Sprintf(`{"name": "%s"}`, modelName)
	resp, err := httpClient.Post(
		fmt.Sprintf("%s/api/show", endpoint),
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result struct {
		Details struct {
			Family            string   `json:"family"`
			ParameterSize     string   `json:"parameter_size"`
			QuantizationLevel string   `json:"quantization_level"`
			Families          []string `json:"families"`
		} `json:"details"`
		ModelInfo map[string]interface{} `json:"model_info"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	info := &kubemootv1alpha1.EmbeddingModelInfo{
		Family: result.Details.Family,
	}

	// Try to extract embedding dimension from model_info
	if dim, ok := result.ModelInfo["embedding_length"]; ok {
		if dimFloat, ok := dim.(float64); ok {
			info.Dimensions = int32(dimFloat)
		}
	}

	return info, nil
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
	if err := abandonIfUnwanted(ctx, r.Client, r.tracker(), embeddingModel,
		pullKey(embeddingModel.Spec.ProviderRef, embeddingModel.Spec.Model)); err != nil {
		return ctrl.Result{}, err
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
		Complete(r)
}
