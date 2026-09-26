package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// NamespaceReconciler watches namespaces labeled kubemoot.ai/crew and replicates
// required secrets into them. This ensures image pull secrets and DB credentials
// are available before any Crew CR or Agent pods are created.
type NamespaceReconciler struct {
	client.Client
	ConfigCache *ConfigCache
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;create

func (r *NamespaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("namespace", req.Name)

	ns := &corev1.Namespace{}
	if err := r.Get(ctx, req.NamespacedName, ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Skip namespaces being deleted
	if !ns.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling crew namespace for secret replication")

	// Replicate image pull secrets from KubemootConfig
	for _, ref := range r.ConfigCache.GetImagePullSecrets() {
		replicateSecret(ctx, r.Client, ref.Name, ns.Name)
	}

	// Replicate additional secrets listed in the annotation.
	// Each entry is either "<name>" (sourced from operator namespace)
	// or "<source-ns>/<name>" (sourced from a specific namespace).
	for _, ref := range parseReplicateSecretsAnnotation(ns.Annotations) {
		if ref.Namespace == "" {
			replicateSecret(ctx, r.Client, ref.Name, ns.Name)
		} else {
			replicateSecretFrom(ctx, r.Client, ref.Name, ref.Namespace, ns.Name)
		}
	}

	// Requeue periodically to handle secret rotation
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only watch namespaces with the kubemoot.ai/crew label
	crewLabelPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasCrewLabel(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasCrewLabel(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // No action on namespace deletion
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasCrewLabel(e.Object)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}).
		WithEventFilter(crewLabelPredicate).
		Complete(r)
}

func hasCrewLabel(obj client.Object) bool {
	labels := obj.GetLabels()
	_, ok := labels["kubemoot.ai/crew"]
	return ok
}
