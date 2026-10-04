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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	aiv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const modelFinalizer = "kubemoot.ai/model-finalizer"

// ModelReconciler reconciles a Model object
type ModelReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	HTTPClient *http.Client
}

// ollamaDisplayName is how errors name the Ollama provider.
const ollamaDisplayName = "Ollama"

// OllamaTagsResponse represents the response from Ollama /api/tags
type OllamaTagsResponse struct {
	Models []OllamaModelInfo `json:"models"`
}

// OllamaModelInfo represents model info from Ollama
type OllamaModelInfo struct {
	Name       string             `json:"name"`
	ModifiedAt string             `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    OllamaModelDetails `json:"details"`
}

// OllamaModelDetails contains model details
type OllamaModelDetails struct {
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// OllamaPullRequest represents a pull request to Ollama
type OllamaPullRequest struct {
	Name   string `json:"name"`
	Stream bool   `json:"stream"`
}

// OllamaPullResponse represents the response from Ollama /api/pull
type OllamaPullResponse struct {
	Status    string `json:"status"`
	Digest    string `json:"digest,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Completed int64  `json:"completed,omitempty"`
}

// OllamaDeleteRequest represents a delete request to Ollama
type OllamaDeleteRequest struct {
	Name string `json:"name"`
}

// OllamaRunningModelsResponse represents running models from /api/ps
type OllamaRunningModelsResponse struct {
	Models []OllamaRunningModel `json:"models"`
}

// OllamaRunningModel represents a running model from /api/ps
type OllamaRunningModel struct {
	Name          string `json:"name"`
	SizeVRAM      int64  `json:"size_vram"`
	Size          int64  `json:"size"`
	ContextLength int    `json:"context_length"`
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=models,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=models/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=models/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop
func (r *ModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the Model instance
	model := &aiv1alpha1.Model{}
	if err := r.Get(ctx, req.NamespacedName, model); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("Reconciling Model", "name", model.Name, "model", model.Spec.Model, "providerRef", model.Spec.ProviderRef)

	// Get the referenced ModelProvider (search cluster-wide — providers are
	// infrastructure and may be in a different namespace than the Model)
	provider, err := r.findModelProvider(ctx, model.Spec.ProviderRef)
	if err != nil {
		return r.updateModelStatus(ctx, model, "Error", false, fmt.Sprintf("ModelProvider %s not found", model.Spec.ProviderRef), nil)
	}

	// Check if provider is ready
	if !provider.Status.Ready {
		return r.updateModelStatus(ctx, model, "Pending", false, fmt.Sprintf("Waiting for ModelProvider %s to be ready", model.Spec.ProviderRef), nil)
	}

	// Handle deletion
	if !model.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, model, provider)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(model, modelFinalizer) {
		if err := addFinalizer(ctx, r.Client, model, modelFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		return requeueNow(), nil
	}

	if provider.Spec.Type != aiv1alpha1.ProviderTypeOllama {
		return r.updateModelStatus(ctx, model, "Error", false, aiv1alpha1.UnsupportedProviderTypeMessage, nil)
	}
	return r.reconcileOllamaModel(ctx, model, provider)
}

// reconcileOllamaModel handles Ollama model reconciliation
func (r *ModelReconciler) reconcileOllamaModel(ctx context.Context, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	// Check if model exists locally in Ollama
	modelInfo, err := r.getOllamaModelInfo(ctx, httpClient, provider.Spec.Endpoint, model.Spec.Model)
	if err != nil {
		// The probe failed (provider busy or momentarily unreachable). Do NOT treat
		// this as "model absent" and start a pull: pullOllamaModel blocks the
		// reconcile worker on a long synchronous download, head-of-line-blocking
		// every other Model. Preserve the current state and retry shortly — a model
		// that was already Available stays Available across a transient blip.
		log.Error(err, "Failed to probe model in Ollama; retrying without changing state", "model", model.Spec.Model)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if modelInfo == nil {
		// Model genuinely absent from the provider — pull it.
		log.Info("Model not found in Ollama, initiating pull", "model", model.Spec.Model)
		return r.pullOllamaModel(ctx, httpClient, model, provider)
	}

	return r.reportOllamaModel(ctx, httpClient, model, provider, modelInfo)
}

// reportOllamaModel records a model the provider lists as present.
func (r *ModelReconciler) reportOllamaModel(ctx context.Context, httpClient *http.Client, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider, modelInfo *OllamaModelInfo) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// The /api/tags probe already confirmed the model is present, so it is
	// Available and Ready for scheduling regardless of VRAM residency. isModelLoaded
	// only refines Available -> Loaded. A transient /api/ps error therefore must NOT
	// requeue-without-status the way the /api/tags error does: that would leave a
	// confirmed-present model stuck un-Ready and its agents Unschedulable — the very
	// bug this change fixes. We report Available and let the periodic requeue upgrade
	// the label later.
	loaded, err := r.isModelLoaded(ctx, httpClient, provider.Spec.Endpoint, model.Spec.Model)
	if err != nil {
		log.Error(err, "Failed to check if model is loaded; reporting Available pending re-check", "model", model.Spec.Model)
	}

	// Update status with model info
	info := &aiv1alpha1.ModelInfo{
		Size:         formatBytes(modelInfo.Size),
		Parameters:   modelInfo.Details.ParameterSize,
		Family:       modelInfo.Details.Family,
		Quantization: modelInfo.Details.QuantizationLevel,
		Format:       modelInfo.Details.Format,
		Digest:       modelInfo.Digest,
		ModifiedAt:   modelInfo.ModifiedAt,
	}

	state := stateAvailable
	if loaded {
		state = "Loaded"
	}

	return r.updateModelStatus(ctx, model, state, true, "Model available", info)
}

// getOllamaModelInfo retrieves model info from Ollama
func (r *ModelReconciler) getOllamaModelInfo(ctx context.Context, httpClient *http.Client, endpoint, modelName string) (*OllamaModelInfo, error) {
	resp, err := httpGet(ctx, httpClient, fmt.Sprintf("%s/api/tags", endpoint))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned status %d", ollamaDisplayName, resp.StatusCode)
	}

	var tagsResp OllamaTagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tagsResp); err != nil {
		return nil, err
	}

	for _, m := range tagsResp.Models {
		if m.Name == modelName {
			return &m, nil
		}
	}

	return nil, nil
}

// isModelLoaded checks if a model is currently loaded in Ollama
func (r *ModelReconciler) isModelLoaded(ctx context.Context, httpClient *http.Client, endpoint, modelName string) (bool, error) {
	resp, err := httpGet(ctx, httpClient, fmt.Sprintf("%s/api/ps", endpoint))
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, nil // /api/ps might not be available in older versions
	}

	var psResp OllamaRunningModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&psResp); err != nil {
		return false, nil
	}

	for _, m := range psResp.Models {
		if m.Name == modelName {
			return true, nil
		}
	}

	return false, nil
}

// pullOllamaModel initiates a model pull in Ollama
func (r *ModelReconciler) pullOllamaModel(ctx context.Context, httpClient *http.Client, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Update status to pulling
	if _, err := r.updateModelStatus(ctx, model, "Pulling", false, "Pulling model from registry", nil); err != nil {
		return ctrl.Result{}, err
	}

	// Make pull request (non-streaming for simplicity)
	pullReq := OllamaPullRequest{
		Name:   model.Spec.Model,
		Stream: false,
	}

	reqBody, err := json.Marshal(pullReq)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Use a longer timeout for pull operations, but keep the injected transport so
	// r.HTTPClient (used by tests and any custom transport) is honored on the pull
	// path too — this is the call that must be redirectable in tests.
	pullClient := *httpClient
	pullClient.Timeout = 30 * time.Minute
	resp, err := pullClient.Post(
		fmt.Sprintf("%s/api/pull", provider.Spec.Endpoint),
		"application/json",
		bytes.NewReader(reqBody),
	)
	if err != nil {
		log.Error(err, "Failed to pull model")
		return r.updateModelStatus(ctx, model, "Error", false, fmt.Sprintf("Failed to pull model: %v", err), nil)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return r.updateModelStatus(ctx, model, "Error", false, fmt.Sprintf("Pull failed with status %d", resp.StatusCode), nil)
	}

	var pullResp OllamaPullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pullResp); err != nil {
		log.Error(err, "Failed to parse pull response")
	}

	log.Info("Model pull completed", "model", model.Spec.Model, "status", pullResp.Status)
	return r.reconcileAfterPull(ctx, httpClient, model, provider, pullResp.Status)
}

// reconcileAfterPull acts on what the provider lists once a pull has returned 200.
// A model that is now listed is reported at once. One that is not listed yet stays
// Pulling, and updateModelStatus polls a Pulling model every 10s, so a pull that
// reports success without the model ever appearing cannot loop tightly.
func (r *ModelReconciler) reconcileAfterPull(ctx context.Context, httpClient *http.Client, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider, pullStatus string) (ctrl.Result, error) {
	modelInfo, err := r.getOllamaModelInfo(ctx, httpClient, provider.Spec.Endpoint, model.Spec.Model)
	if err != nil {
		return r.updateModelStatus(ctx, model, "Pulling", false,
			fmt.Sprintf("Pull reported %q; listing the model failed: %v", pullStatus, err), nil)
	}
	if modelInfo == nil {
		return r.updateModelStatus(ctx, model, "Pulling", false,
			fmt.Sprintf("Pull reported %q; the model is not listed yet", pullStatus), nil)
	}
	return r.reportOllamaModel(ctx, httpClient, model, provider, modelInfo)
}

// handleDeletion handles the deletion of a Model
func (r *ModelReconciler) handleDeletion(ctx context.Context, model *aiv1alpha1.Model, provider *aiv1alpha1.ModelProvider) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(model, modelFinalizer) {
		return ctrl.Result{}, nil
	}

	log.Info("Handling model deletion", "model", model.Spec.Model)

	// Delete model from Ollama
	if provider.Spec.Type == aiv1alpha1.ProviderTypeOllama {
		if err := r.deleteOllamaModel(ctx, provider.Spec.Endpoint, model.Spec.Model); err != nil {
			log.Error(err, "Failed to delete model from Ollama, continuing with finalizer removal")
			// Don't block deletion if Ollama delete fails
		}
	}

	// Remove finalizer
	if err := removeFinalizer(ctx, r.Client, model, modelFinalizer); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Model deleted successfully", "model", model.Spec.Model)
	return ctrl.Result{}, nil
}

// deleteOllamaModel deletes a model from Ollama
func (r *ModelReconciler) deleteOllamaModel(ctx context.Context, endpoint, modelName string) error {
	log := logf.FromContext(ctx)

	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	deleteReq := OllamaDeleteRequest{Name: modelName}
	reqBody, err := json.Marshal(deleteReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("%s/api/delete", endpoint), bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		log.Info("Model not found in Ollama, already deleted", "model", modelName)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete failed with status %d", resp.StatusCode)
	}

	log.Info("Model deleted from Ollama", "model", modelName)
	return nil
}

// findModelProvider looks up a ModelProvider by name across all namespaces.
func (r *ModelReconciler) findModelProvider(ctx context.Context, name string) (*aiv1alpha1.ModelProvider, error) {
	providerList := &aiv1alpha1.ModelProviderList{}
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

// updateModelStatus updates the Model status
func (r *ModelReconciler) updateModelStatus(ctx context.Context, model *aiv1alpha1.Model, state string, ready bool, message string, info *aiv1alpha1.ModelInfo) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	model.Status.State = state
	model.Status.Ready = ready
	model.Status.Message = message
	if info != nil {
		model.Status.ModelInfo = info
	}

	// Set condition
	condition := metav1.Condition{
		Type:               conditionTypeReady,
		Status:             metav1.ConditionFalse,
		Reason:             state,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
	if ready {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&model.Status.Conditions, condition)

	if err := r.Status().Update(ctx, model); err != nil {
		log.Error(err, "Failed to update Model status")
		return ctrl.Result{}, err
	}

	// Requeue periodically to check model state
	requeueAfter := 5 * time.Minute
	if state == "Pulling" {
		requeueAfter = 10 * time.Second // Check pulling progress more frequently
	} else if !ready {
		requeueAfter = 30 * time.Second
	}

	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// formatBytes converts bytes to human-readable format
func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Model{}).
		Named("model").
		// Reconcile Models concurrently. A genuine pull is a long synchronous call;
		// with a single worker one pull would block every other Model's reconcile.
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}
