package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// sourceSecretPredicate passes events for Secrets in an allowed source namespace whose
// content is new or changed. Deletions are ignored: copies outlive their source.
func sourceSecretPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return secretSourceAllowed(e.Object.GetNamespace()) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return secretSourceAllowed(e.ObjectNew.GetNamespace()) && secretContentChanged(e.ObjectOld, e.ObjectNew)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

func secretContentChanged(oldObj, newObj client.Object) bool {
	o, ok1 := oldObj.(*corev1.Secret)
	n, ok2 := newObj.(*corev1.Secret)
	if !ok1 || !ok2 {
		return true
	}
	return o.Type != n.Type || !secretDataEqual(o.Data, n.Data)
}

// secretIsImagePullSource reports whether the Secret is a configured image pull secret
// in the operator namespace, the only source the Crew and Namespace reconcilers pull from.
func secretIsImagePullSource(cache *ConfigCache, s client.Object) bool {
	if s.GetNamespace() != operatorNamespace() {
		return false
	}
	for _, ref := range cache.GetImagePullSecrets() {
		if ref.Name == s.GetName() {
			return true
		}
	}
	return false
}

// namespaceReplicates reports whether the namespace's replicate-secrets annotation names the Secret.
func namespaceReplicates(ns *corev1.Namespace, secret client.Object) bool {
	for _, ref := range parseReplicateSecretsAnnotation(ns.Annotations) {
		source := ref.Namespace
		if source == "" {
			source = operatorNamespace()
		}
		if ref.Name == secret.GetName() && source == secret.GetNamespace() {
			return true
		}
	}
	return false
}

// enqueueNamespacesForSecret maps a changed source Secret to the crew namespaces that replicate it.
func enqueueNamespacesForSecret(cli client.Reader, cache *ConfigCache) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return secretToNamespaceRequests(ctx, cli, cache, obj)
	})
}

func secretToNamespaceRequests(ctx context.Context, cli client.Reader, cache *ConfigCache, obj client.Object) []reconcile.Request {
	var list corev1.NamespaceList
	if err := cli.List(ctx, &list, client.HasLabels{"kubemoot.ai/crew"}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list crew namespaces for secret change")
		return nil
	}
	pull := secretIsImagePullSource(cache, obj)
	var reqs []reconcile.Request
	for i := range list.Items {
		if pull || namespaceReplicates(&list.Items[i], obj) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// enqueueCrewsForSecret maps a changed image pull Secret to every Crew, since each Crew
// reconcile replicates the pull secrets into its namespace.
func enqueueCrewsForSecret(cli client.Reader, cache *ConfigCache) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return secretToCrewRequests(ctx, cli, cache, obj)
	})
}

func secretToCrewRequests(ctx context.Context, cli client.Reader, cache *ConfigCache, obj client.Object) []reconcile.Request {
	if !secretIsImagePullSource(cache, obj) {
		return nil
	}
	var crews kubemootv1alpha1.CrewList
	if err := cli.List(ctx, &crews); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list crews for secret change")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(crews.Items))
	for i := range crews.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&crews.Items[i])})
	}
	return reqs
}
