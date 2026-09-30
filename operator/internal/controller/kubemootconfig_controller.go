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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// KubemootConfigReconciler reconciles a KubemootConfig object
type KubemootConfigReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	ConfigCache *ConfigCache
}

// +kubebuilder:rbac:groups=kubemoot.ai,resources=kubemootconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubemoot.ai,resources=kubemootconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=kubemootconfigs/finalizers,verbs=update

// Reconcile handles KubemootConfig reconciliation
// This controller is responsible for:
// 1. Watching for changes to the KubemootConfig singleton
// 2. Updating the in-memory ConfigCache when changes occur
// 3. Other controllers use the ConfigCache to get current default images
func (r *KubemootConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// We only care about the "default" singleton
	if req.Name != DefaultKubemootConfigName {
		log.V(1).Info("Ignoring KubemootConfig that is not named 'default'", "name", req.Name)
		return ctrl.Result{}, nil
	}

	// Fetch the KubemootConfig instance
	config := &kubemootv1alpha1.KubemootConfig{}
	if err := r.Get(ctx, req.NamespacedName, config); err != nil {
		if errors.IsNotFound(err) {
			// Config was deleted - clear the cache
			log.Info("KubemootConfig 'default' was deleted, clearing config cache")
			r.ConfigCache.Clear()
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	log.Info("Reconciling KubemootConfig",
		"indexer", config.Spec.Images.Indexer,
		"queryService", config.Spec.Images.QueryService,
		"agentRuntime", config.Spec.Images.AgentRuntime,
		"mcpGateway", config.Spec.Images.McpGateway,
	)

	// Update the config cache - this is the key operation
	// All other controllers will pick up changes on their next reconciliation
	r.ConfigCache.Update(config)

	// Update status
	now := metav1.Now()
	config.Status.Ready = true
	config.Status.LastUpdated = &now
	config.Status.Message = "Configuration loaded successfully"

	condition := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ConfigLoaded",
		Message:            "Configuration has been loaded into the operator",
		LastTransitionTime: now,
	}
	meta.SetStatusCondition(&config.Status.Conditions, condition)

	if err := r.Status().Update(ctx, config); err != nil {
		log.Error(err, "Failed to update KubemootConfig status")
		return ctrl.Result{}, err
	}

	log.Info("KubemootConfig cache updated successfully")
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KubemootConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubemootv1alpha1.KubemootConfig{}).
		Named("kubemootconfig").
		Complete(r)
}
