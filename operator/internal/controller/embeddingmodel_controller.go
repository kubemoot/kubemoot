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
	"io"
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
		return ctrl.Result{Requeue: true}, nil
	}

	// Fetch the referenced ModelProvider (cluster-wide — providers are infrastructure)
	provider, err := r.findModelProvider(ctx, embeddingModel.Spec.ProviderRef)
	if err != nil {
		return r.updateStatus(ctx, embeddingModel, "Error", false,
			fmt.Sprintf("ModelProvider '%s' not found", embeddingModel.Spec.ProviderRef))
	}

	// Check if provider is ready
	if !provider.Status.Ready {
		return r.updateStatus(ctx, embeddingModel, "Pending", false,
			fmt.Sprintf("Waiting for ModelProvider '%s' to be ready", provider.Name))
	}

	if provider.Spec.Type != kubemootv1alpha1.ProviderTypeOllama {
		return r.updateStatus(ctx, embeddingModel, "Error", false, kubemootv1alpha1.UnsupportedProviderTypeMessage)
	}
	return r.reconcileOllamaEmbedding(ctx, embeddingModel, provider)
}

// reconcileOllamaEmbedding handles Ollama-based embedding models
func (r *EmbeddingModelReconciler) reconcileOllamaEmbedding(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, provider *kubemootv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	endpoint := provider.Spec.Endpoint

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	// Check if model exists in Ollama
	exists, err := r.ollamaModelExists(httpClient, endpoint, embeddingModel.Spec.Model)
	if err != nil {
		return r.updateStatus(ctx, embeddingModel, "Error", false,
			fmt.Sprintf("Failed to check model: %v", err))
	}

	if !exists {
		// Start pulling the model
		log.Info("Pulling embedding model", "model", embeddingModel.Spec.Model)
		if err := r.ollamaPullModel(httpClient, endpoint, embeddingModel.Spec.Model); err != nil {
			return r.updateStatus(ctx, embeddingModel, "Error", false,
				fmt.Sprintf("Failed to pull model: %v", err))
		}
		return r.updateStatus(ctx, embeddingModel, "Pulling", false,
			fmt.Sprintf("Pulling model %s", embeddingModel.Spec.Model))
	}

	// Model exists - get info and mark as available
	modelInfo, err := r.getOllamaModelInfo(httpClient, endpoint, embeddingModel.Spec.Model)
	if err != nil {
		log.Error(err, "Failed to get model info, continuing anyway")
	}

	// Update status with model info
	embeddingModel.Status.State = "Available"
	embeddingModel.Status.Ready = true
	embeddingModel.Status.Message = fmt.Sprintf("Embedding model %s is available", embeddingModel.Spec.Model)
	embeddingModel.Status.Endpoint = endpoint // Set endpoint for RAGSource to use

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
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Available",
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

// ollamaModelExists checks if a model exists in Ollama
func (r *EmbeddingModelReconciler) ollamaModelExists(client *http.Client, endpoint, modelName string) (bool, error) {
	resp, err := client.Get(fmt.Sprintf("%s/api/tags", endpoint))
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, err
	}

	// Check for exact match or base name match
	for _, m := range result.Models {
		if m.Name == modelName || strings.Split(m.Name, ":")[0] == strings.Split(modelName, ":")[0] {
			return true, nil
		}
	}

	return false, nil
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

// ollamaPullModel initiates a model pull in Ollama
func (r *EmbeddingModelReconciler) ollamaPullModel(client *http.Client, endpoint, modelName string) error {
	reqBody := fmt.Sprintf(`{"name": "%s", "stream": false}`, modelName)
	resp, err := client.Post(
		fmt.Sprintf("%s/api/pull", endpoint),
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read response to completion (pull is synchronous with stream: false)
	_, err = io.ReadAll(resp.Body)
	return err
}

// getOllamaModelInfo retrieves model metadata from Ollama
func (r *EmbeddingModelReconciler) getOllamaModelInfo(client *http.Client, endpoint, modelName string) (*kubemootv1alpha1.EmbeddingModelInfo, error) {
	reqBody := fmt.Sprintf(`{"name": "%s"}`, modelName)
	resp, err := client.Post(
		fmt.Sprintf("%s/api/show", endpoint),
		"application/json",
		strings.NewReader(reqBody),
	)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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

	// Optionally delete model from Ollama (similar to Model controller)
	// For now, we leave embedding models in Ollama as they may be shared

	// Remove finalizer
	if err := removeFinalizer(ctx, r.Client, embeddingModel, embeddingModelFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("EmbeddingModel deleted successfully", "name", embeddingModel.Name)
	return ctrl.Result{}, nil
}

// updateStatus updates the EmbeddingModel status
func (r *EmbeddingModelReconciler) updateStatus(ctx context.Context, embeddingModel *kubemootv1alpha1.EmbeddingModel, state string, ready bool, message string) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	embeddingModel.Status.State = state
	embeddingModel.Status.Ready = ready
	embeddingModel.Status.Message = message

	// Set condition
	conditionStatus := metav1.ConditionFalse
	if ready {
		conditionStatus = metav1.ConditionTrue
	}
	condition := metav1.Condition{
		Type:               "Ready",
		Status:             conditionStatus,
		Reason:             state,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	meta.SetStatusCondition(&embeddingModel.Status.Conditions, condition)

	if err := r.Status().Update(ctx, embeddingModel); err != nil {
		log.Error(err, "Failed to update EmbeddingModel status")
		return ctrl.Result{}, err
	}

	// Requeue based on state
	if state == "Pulling" {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if !ready {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *EmbeddingModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.EmbeddingModel{}).
		Named("embeddingmodel").
		Complete(r)
}
