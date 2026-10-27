package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

// NamespaceReconciler watches namespaces labeled kubemoot.ai/crew and replicates
// required secrets into them. This ensures image pull secrets and DB credentials
// are available before any Crew CR or Agent pods are created.
type NamespaceReconciler struct {
	client.Client
	ConfigCache *ConfigCache
	// NetPol applies the namespace baseline network policies. Nil generates none.
	NetPol *NetPolicySync
	// APIReader reads without the cache. Nil reads through the cache only.
	APIReader client.Reader
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update

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

	// A namespace that lost the crew label keeps no generated policies.
	if !hasCrewLabel(ns) {
		return ctrl.Result{}, r.reconcileBaseline(ctx, ns)
	}

	log.Info("Reconciling crew namespace for secret replication")
	r.replicateSecrets(ctx, ns)

	if err := r.reconcileBaseline(ctx, ns); err != nil {
		return ctrl.Result{}, err
	}

	// Source Secret changes arrive through the Secret watch; the resync is a safety net.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// replicateSecrets copies the image pull secrets and the secrets the namespace lists into it.
func (r *NamespaceReconciler) replicateSecrets(ctx context.Context, ns *corev1.Namespace) {
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
}

// reconcileBaseline applies the default-deny ingress policy and the crew-internal allow to
// a crew namespace: one that carries the crew label and holds at least one Crew. A
// namespace without a Crew keeps no generated policies, and the crew label the operator
// set on it is removed. The operator's own namespace is left to the chart.
func (r *NamespaceReconciler) reconcileBaseline(ctx context.Context, ns *corev1.Namespace) error {
	holdsCrew, err := namespaceHoldsCrew(ctx, r, ns.Name)
	if err != nil {
		return err
	}
	if r.NetPol == nil {
		return nil
	}
	if !holdsCrew && r.NetPol.Config.Enabled {
		if err := r.clearStaleCrewLabel(ctx, ns); err != nil {
			return err
		}
	}
	var objects []client.Object
	if holdsCrew && ns.Name != operatorNamespace() && hasCrewLabel(ns) {
		objects = netpol.Baseline(r.NetPol.Resolved(), ns.Name)
	}
	return r.NetPol.Apply(ctx, ns, ns.Name, objects)
}

// clearStaleCrewLabel removes the crew label from a namespace that holds no Crew, when the
// operator set it (the managed-by label says so). A label a person set is left alone.
func (r *NamespaceReconciler) clearStaleCrewLabel(ctx context.Context, ns *corev1.Namespace) error {
	if ns.Labels[labelManagedBy] != managedByValue || !hasCrewLabel(ns) || isProtectedNamespace(ns.Name) {
		return nil
	}
	// The cached Crew list may lag a Crew created a moment ago; confirm against the API server.
	if r.APIReader != nil {
		holdsCrew, err := namespaceHoldsCrew(ctx, r.APIReader, ns.Name)
		if err != nil || holdsCrew {
			return err
		}
	}
	// The lock makes a concurrent label write by the Crew reconciler a conflict, which requeues.
	patch := client.MergeFromWithOptions(ns.DeepCopy(), client.MergeFromWithOptimisticLock{})
	delete(ns.Labels, labelCrew)
	delete(ns.Labels, labelManagedBy)
	return client.IgnoreNotFound(r.Patch(ctx, ns, patch))
}

func (r *NamespaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Only watch namespaces with the kubemoot.ai/crew label
	crewLabelPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasCrewLabel(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasCrewLabel(e.ObjectNew) || hasCrewLabel(e.ObjectOld)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // No action on namespace deletion
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasCrewLabel(e.Object)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Namespace{}, builder.WithPredicates(crewLabelPredicate)).
		Owns(&networkingv1.NetworkPolicy{}).
		// The first Crew of a namespace creates its baseline; the last Crew's deletion removes it.
		Watches(&kubemootv1alpha1.Crew{}, enqueueCrewNamespace(), builder.WithPredicates(crewLifecyclePredicate())).
		// A change to a source Secret re-reconciles the crew namespaces that replicate it.
		Watches(&corev1.Secret{}, enqueueNamespacesForSecret(mgr.GetClient(), r.ConfigCache),
			builder.WithPredicates(sourceSecretPredicate())).
		Complete(r)
}

func hasCrewLabel(obj client.Object) bool {
	labels := obj.GetLabels()
	_, ok := labels["kubemoot.ai/crew"]
	return ok
}
