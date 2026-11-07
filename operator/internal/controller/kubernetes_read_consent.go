/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// labelReadConsent on a Namespace is the consent to let the Kubernetes MCP servers of
	// every Crew read it. Only the value "true" consents. It is set by someone with rights
	// on the Namespace object; the operator never writes it.
	labelReadConsent = "kubemoot.ai/crew-read"
	// readConsentGranted is the value of labelReadConsent that grants consent.
	readConsentGranted = "true"
	// capabilityKubernetes marks an MCPServer as a Kubernetes MCP server.
	capabilityKubernetes = "kubernetes"

	// Event reasons and action on a Kubernetes MCP server.
	reasonNoReadConsent = "NoReadConsent"
	reasonReadConsent   = "ReadConsent"
	actionReconcile     = "Reconcile"
	// kindClusterRole is the roleRef kind of a ClusterRole.
	kindClusterRole = "ClusterRole"

	// kubernetesReadRole is the chart's ClusterRole with the namespaced reads. A RoleBinding
	// in a consenting namespace grants it there and nowhere else.
	kubernetesReadRole = "kubemoot-kubernetes-mcp-read"
	// kubernetesNamespacesRole is the chart's ClusterRole that lets a server list namespaces,
	// which is cluster-scoped and cannot be granted by a RoleBinding.
	kubernetesNamespacesRole = "kubemoot-kubernetes-mcp-namespaces"

	// kubernetesReadBinding is the name of the RoleBinding the operator keeps in each
	// consenting namespace.
	kubernetesReadBinding = "kubemoot-kubernetes-mcp-read"
	// kubernetesNamespacesBinding is the name of the ClusterRoleBinding the operator keeps
	// for the namespaces ClusterRole.
	kubernetesNamespacesBinding = "kubemoot-kubernetes-mcp-namespaces"

	// readConsentComponent labels the bindings the operator keeps for read consent.
	readConsentComponent = "kubernetes-mcp-read-consent"
	// readConsentCluster is the reconcile request name for the cluster-wide work. It is not a
	// valid namespace name, so it never collides with a Namespace.
	readConsentCluster = "*"

	// legacyKubernetesBinding is the ClusterRoleBinding earlier releases made, bound to
	// legacyKubernetesRole. It is removed on upgrade.
	legacyKubernetesBinding = "kubemoot-kubernetes-mcp-binding"
	legacyKubernetesRole    = "kubemoot-mcpserver-creator"
	// legacyKubernetesAccount is the one subject of the legacy binding.
	legacyKubernetesAccount = "kubemoot-kubernetes-mcp"
)

// ReadConsentReconciler grants the Kubernetes MCP servers read access to the namespaces
// that consent to it, and to no others. A namespace consents with the label
// kubemoot.ai/crew-read=true. The reconciler keeps a RoleBinding to the read ClusterRole
// in each consenting namespace, bound to the ServiceAccount of every Kubernetes MCP server,
// removes it when the namespace stops consenting, and keeps a ClusterRoleBinding that
// lets those ServiceAccounts list namespaces. It reports the count of consenting
// namespaces on each Kubernetes MCP server.
type ReadConsentReconciler struct {
	client.Client
	// Recorder emits an event on a Kubernetes MCP server when the count changes. Nil emits none.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=kubemoot-kubernetes-mcp-read;kubemoot-kubernetes-mcp-namespaces
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers/status,verbs=get;update;patch

// Reconcile handles one Namespace, or the cluster-wide request after a Kubernetes MCP
// server appears, changes its ServiceAccount or goes away.
func (r *ReadConsentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	servers, err := r.kubernetesServers(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	subjects := serverSubjects(servers)
	if req.Name == readConsentCluster {
		err = r.syncCluster(ctx, subjects)
	} else {
		err = r.syncNamespace(ctx, req.Name, subjects)
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.report(ctx, servers)
}

// kubernetesServers lists the MCPServers that carry the kubernetes capability.
func (r *ReadConsentReconciler) kubernetesServers(ctx context.Context) ([]kubemootv1alpha1.MCPServer, error) {
	all := &kubemootv1alpha1.MCPServerList{}
	if err := r.List(ctx, all); err != nil {
		return nil, fmt.Errorf("list MCP servers: %w", err)
	}
	var out []kubemootv1alpha1.MCPServer
	for i := range all.Items {
		if isKubernetesServer(&all.Items[i]) && all.Items[i].DeletionTimestamp.IsZero() {
			out = append(out, all.Items[i])
		}
	}
	return out, nil
}

// isKubernetesServer reports whether the MCPServer declares the kubernetes capability.
func isKubernetesServer(s *kubemootv1alpha1.MCPServer) bool {
	return slices.Contains(s.Spec.Capabilities, capabilityKubernetes)
}

// serverSubjects returns, sorted and without repeats, the ServiceAccounts the servers run
// as. A server without its own ServiceAccount runs as the namespace default, which is
// never bound, whether the server names it or leaves the field empty.
func serverSubjects(servers []kubemootv1alpha1.MCPServer) []rbacv1.Subject {
	seen := map[string]rbacv1.Subject{}
	for i := range servers {
		name := servers[i].Spec.ServiceAccountName
		if name == "" || name == defaultServiceAccount {
			continue
		}
		subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: servers[i].Namespace}
		seen[subject.Namespace+"/"+subject.Name] = subject
	}
	out := make([]rbacv1.Subject, 0, len(seen))
	for _, subject := range seen {
		out = append(out, subject)
	}
	slices.SortFunc(out, func(a, b rbacv1.Subject) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	return out
}

// consents reports whether the namespace grants read consent: the label is "true" and the
// namespace is not being deleted.
func consents(obj client.Object) bool {
	return obj.GetLabels()[labelReadConsent] == readConsentGranted && obj.GetDeletionTimestamp().IsZero()
}

// syncNamespace brings one namespace's RoleBinding in line with its consent.
func (r *ReadConsentReconciler) syncNamespace(ctx context.Context, name string, subjects []rbacv1.Subject) error {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
		// A deleted namespace takes its RoleBinding with it.
		return client.IgnoreNotFound(err)
	}
	if consents(ns) {
		return r.ensureReadBinding(ctx, name, subjects)
	}
	return r.removeBinding(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: kubernetesReadBinding, Namespace: name}})
}

// syncCluster keeps the namespaces ClusterRoleBinding, every consenting namespace's
// RoleBinding, and removes RoleBindings left in namespaces that no longer consent.
func (r *ReadConsentReconciler) syncCluster(ctx context.Context, subjects []rbacv1.Subject) error {
	if err := r.ensureNamespacesBinding(ctx, subjects); err != nil {
		return err
	}
	consenting, err := r.consentingNamespaces(ctx)
	if err != nil {
		return err
	}
	for _, name := range consenting {
		if err := r.ensureReadBinding(ctx, name, subjects); err != nil {
			return err
		}
	}
	return r.removeStaleBindings(ctx, consenting)
}

// consentingNamespaces lists, sorted, the namespaces that grant read consent.
func (r *ReadConsentReconciler) consentingNamespaces(ctx context.Context) ([]string, error) {
	list := &corev1.NamespaceList{}
	if err := r.List(ctx, list, client.MatchingLabels{labelReadConsent: readConsentGranted}); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var out []string
	for i := range list.Items {
		if consents(&list.Items[i]) {
			out = append(out, list.Items[i].Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

func readConsentLabels() map[string]string {
	return map[string]string{labelManagedBy: managedByValue, labelComponent: readConsentComponent}
}

// ensureReadBinding makes the RoleBinding in a consenting namespace. With no Kubernetes MCP
// server there is nobody to bind, so it removes the binding instead.
func (r *ReadConsentReconciler) ensureReadBinding(ctx context.Context, namespace string, subjects []rbacv1.Subject) error {
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: kubernetesReadBinding, Namespace: namespace}}
	return r.ensureBinding(ctx, binding, subjects, func() {
		binding.Subjects = subjects
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: kubernetesReadRole}
	})
}

// ensureNamespacesBinding makes the ClusterRoleBinding that lets the servers list namespaces,
// or removes it when there is no server.
func (r *ReadConsentReconciler) ensureNamespacesBinding(ctx context.Context, subjects []rbacv1.Subject) error {
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: kubernetesNamespacesBinding}}
	return r.ensureBinding(ctx, binding, subjects, func() {
		binding.Subjects = subjects
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: kubernetesNamespacesRole}
	})
}

// ensureBinding creates or updates a binding with the wanted subjects and role (set fills
// them in), or removes it when there are no subjects. A binding of that name the operator
// did not make is an error and is left as it is.
func (r *ReadConsentReconciler) ensureBinding(ctx context.Context, binding client.Object, subjects []rbacv1.Subject, set func()) error {
	if len(subjects) == 0 {
		return r.removeBinding(ctx, binding)
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, binding, func() error {
		if binding.GetResourceVersion() != "" && binding.GetLabels()[labelComponent] != readConsentComponent {
			return fmt.Errorf("a binding of that name exists that the operator did not make")
		}
		binding.SetLabels(mergeLabels(binding.GetLabels(), readConsentLabels()))
		set()
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensure binding %s/%s: %w", binding.GetNamespace(), binding.GetName(), err)
	}
	return nil
}

// removeBinding deletes a binding this reconciler made. A binding of that name without the
// operator's labels was made by someone else and stays.
func (r *ReadConsentReconciler) removeBinding(ctx context.Context, binding client.Object) error {
	if err := r.Get(ctx, client.ObjectKeyFromObject(binding), binding); err != nil {
		return client.IgnoreNotFound(err)
	}
	if binding.GetLabels()[labelComponent] != readConsentComponent {
		return nil
	}
	return client.IgnoreNotFound(r.Delete(ctx, binding))
}

// removeStaleBindings deletes read-consent RoleBindings in every namespace not in keep.
func (r *ReadConsentReconciler) removeStaleBindings(ctx context.Context, keep []string) error {
	bindings := &rbacv1.RoleBindingList{}
	if err := r.List(ctx, bindings, client.MatchingLabels{labelComponent: readConsentComponent}); err != nil {
		return fmt.Errorf("list role bindings: %w", err)
	}
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Name != kubernetesReadBinding || slices.Contains(keep, b.Namespace) {
			continue
		}
		if err := client.IgnoreNotFound(r.Delete(ctx, b)); err != nil {
			return fmt.Errorf("delete role binding %s/%s: %w", b.Namespace, b.Name, err)
		}
	}
	return nil
}

// report records the count of consenting namespaces on each Kubernetes MCP server, so an
// install where no namespace consents shows it. It writes only when the count changed.
func (r *ReadConsentReconciler) report(ctx context.Context, servers []kubemootv1alpha1.MCPServer) error {
	if len(servers) == 0 {
		return nil
	}
	consenting, err := r.consentingNamespaces(ctx)
	if err != nil {
		return err
	}
	count := int32(len(consenting)) //nolint:gosec // bounded by the namespaces in a cluster
	for i := range servers {
		if err := r.reportOne(ctx, &servers[i], count); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReadConsentReconciler) reportOne(ctx context.Context, server *kubemootv1alpha1.MCPServer, count int32) error {
	if cur := server.Status.ReadConsentNamespaces; cur != nil && *cur == count {
		return nil
	}
	patch := client.MergeFrom(server.DeepCopy())
	server.Status.ReadConsentNamespaces = &count
	if err := r.Status().Patch(ctx, server, patch); err != nil {
		return client.IgnoreNotFound(err)
	}
	r.recordCount(server, count)
	return nil
}

func (r *ReadConsentReconciler) recordCount(server *kubemootv1alpha1.MCPServer, count int32) {
	if r.Recorder == nil {
		return
	}
	if count == 0 {
		r.Recorder.Eventf(server, nil, corev1.EventTypeWarning, reasonNoReadConsent, actionReconcile,
			"No namespace carries %s=%s, so this server reads no namespace", labelReadConsent, readConsentGranted)
		return
	}
	r.Recorder.Eventf(server, nil, corev1.EventTypeNormal, reasonReadConsent, actionReconcile,
		"%d namespaces carry %s=%s", count, labelReadConsent, readConsentGranted)
}

// RemoveLegacyKubernetesBinding deletes the ClusterRoleBinding earlier releases made, which
// granted the Kubernetes MCP server's ServiceAccount the read role in every namespace. It
// leaves a binding of that name that points elsewhere. Reads go through reader, which sees
// the API server; writes go through c.
func RemoveLegacyKubernetesBinding(ctx context.Context, reader client.Reader, c client.Client) error {
	binding := &rbacv1.ClusterRoleBinding{}
	if err := reader.Get(ctx, types.NamespacedName{Name: legacyKubernetesBinding}, binding); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !isLegacyKubernetesBinding(binding) {
		return nil
	}
	logf.FromContext(ctx).Info("Removing the cluster-wide Kubernetes MCP binding", "binding", legacyKubernetesBinding)
	return client.IgnoreNotFound(c.Delete(ctx, binding))
}

func isLegacyKubernetesBinding(b *rbacv1.ClusterRoleBinding) bool {
	if b.RoleRef.Name != legacyKubernetesRole || len(b.Subjects) != 1 {
		return false
	}
	return b.Subjects[0].Kind == rbacv1.ServiceAccountKind && b.Subjects[0].Name == legacyKubernetesAccount
}

// SetupWithManager watches namespaces (consent label), the bindings it keeps, and the
// Kubernetes MCP servers (whose ServiceAccounts are the subjects).
func (r *ReadConsentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("kubernetes-read-consent").
		For(&corev1.Namespace{}, builder.WithPredicates(consentLabelPredicate())).
		Watches(&rbacv1.RoleBinding{}, handler.EnqueueRequestsFromMapFunc(roleBindingNamespace),
			builder.WithPredicates(ownBindingPredicate())).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(clusterRequest),
			builder.WithPredicates(ownBindingPredicate())).
		Watches(&kubemootv1alpha1.MCPServer{}, handler.EnqueueRequestsFromMapFunc(clusterRequest),
			builder.WithPredicates(kubernetesServerPredicate())).
		Complete(r)
}

func clusterRequest(context.Context, client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: readConsentCluster}}}
}

func roleBindingNamespace(_ context.Context, obj client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: obj.GetNamespace()}}}
}

// consentLabelPredicate passes a namespace created with consent, and an update that changes
// whether it consents. Deletion needs no work: the namespace takes its binding with it.
func consentLabelPredicate() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return consents(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return consents(e.ObjectOld) != consents(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return consents(e.Object) },
	}
}

// ownBindingPredicate passes the read-consent bindings and no others.
func ownBindingPredicate() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetLabels()[labelComponent] == readConsentComponent
	})
}

// kubernetesServerPredicate passes the lifecycle of a Kubernetes MCP server and a change to
// its ServiceAccount or capabilities. Status writes do not pass.
func kubernetesServerPredicate() predicate.Funcs {
	asServer := func(obj client.Object) *kubemootv1alpha1.MCPServer {
		s, _ := obj.(*kubemootv1alpha1.MCPServer)
		return s
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isKubernetesServer(asServer(e.Object)) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isKubernetesServer(asServer(e.Object)) },
		GenericFunc: func(e event.GenericEvent) bool { return isKubernetesServer(asServer(e.Object)) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldS, newS := asServer(e.ObjectOld), asServer(e.ObjectNew)
			if !isKubernetesServer(oldS) && !isKubernetesServer(newS) {
				return false
			}
			return isKubernetesServer(oldS) != isKubernetesServer(newS) ||
				oldS.Spec.ServiceAccountName != newS.Spec.ServiceAccountName ||
				oldS.DeletionTimestamp.IsZero() != newS.DeletionTimestamp.IsZero()
		},
	}
}
