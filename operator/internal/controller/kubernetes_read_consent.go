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
	// labelSecretsConsent on a Namespace consents to Secret reads, and labelWriteConsent to
	// pod exec and workload patch and scale, by the Kubernetes MCP servers that ask for them
	// with the annotation annoKubernetesAccess.
	labelSecretsConsent = "kubemoot.ai/crew-read-secrets"
	labelWriteConsent   = "kubemoot.ai/crew-write"
	// annoKubernetesAccess on an MCPServer lists, comma separated, the extra access it asks for:
	// secrets (get, list, watch), secret-get (get) and write. A namespace grants each only with
	// its own label; the annotation alone grants nothing.
	annoKubernetesAccess = "kubemoot.ai/kubernetes-access"

	// The chart's opt-in ClusterRoles. Each RoleBinding carries the name of its role.
	roleSecrets   = "kubemoot-kubernetes-mcp-secrets"
	roleSecretGet = "kubemoot-kubernetes-mcp-secret-get"
	roleWrite     = "kubemoot-kubernetes-mcp-write"

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

// consentTier is one level of access a namespace can consent to: a ClusterRole, the
// namespace label that consents, the RoleBinding that carries it, and which servers it binds.
type consentTier struct {
	label   string
	role    string
	binding string
	// token is the word in annoKubernetesAccess that asks for the tier. Empty binds every
	// Kubernetes MCP server.
	token string
}

var consentTiers = []consentTier{
	{label: labelReadConsent, role: kubernetesReadRole, binding: kubernetesReadBinding},
	{label: labelSecretsConsent, role: roleSecrets, binding: roleSecrets, token: "secrets"},
	{label: labelSecretsConsent, role: roleSecretGet, binding: roleSecretGet, token: "secret-get"},
	{label: labelWriteConsent, role: roleWrite, binding: roleWrite, token: "write"},
}

// ReadConsentReconciler grants the Kubernetes MCP servers access to the namespaces that
// consent to it, and to no others. A namespace consents with the label
// kubemoot.ai/crew-read=true for reads, kubemoot.ai/crew-read-secrets=true for Secrets and
// kubemoot.ai/crew-write=true for exec and workload changes. For each tier the reconciler keeps
// a RoleBinding to the tier's ClusterRole in each consenting namespace, bound to the
// ServiceAccount of every Kubernetes MCP server (for Secrets and write, of every server that
// also asks for it with the annotation), removes it when the namespace stops consenting, and
// keeps a ClusterRoleBinding that lets those ServiceAccounts list namespaces. Consent is
// cluster-wide: a consenting namespace is open to every server that qualifies, from any
// namespace. It reports the count of consenting
// namespaces on each Kubernetes MCP server.
type ReadConsentReconciler struct {
	client.Client
	// Recorder emits an event on a Kubernetes MCP server when the count changes. Nil emits none.
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles,verbs=bind,resourceNames=kubemoot-kubernetes-mcp-read;kubemoot-kubernetes-mcp-namespaces;kubemoot-kubernetes-mcp-secrets;kubemoot-kubernetes-mcp-secret-get;kubemoot-kubernetes-mcp-write
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubemoot.ai,resources=mcpservers/status,verbs=get;update;patch

// Reconcile handles one Namespace, or the cluster-wide request after a Kubernetes MCP
// server appears, changes its ServiceAccount or goes away.
func (r *ReadConsentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	servers, err := r.kubernetesServers(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	if req.Name == readConsentCluster {
		err = r.syncCluster(ctx, servers)
	} else {
		err = r.syncNamespace(ctx, req.Name, servers)
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
func serverSubjects(servers []kubemootv1alpha1.MCPServer, token string) []rbacv1.Subject {
	seen := map[string]rbacv1.Subject{}
	for i := range servers {
		name := servers[i].Spec.ServiceAccountName
		if name == "" || name == defaultServiceAccount || !asksFor(&servers[i], token) {
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

// asksFor reports whether the server asks for the access token. The empty token is asked
// for by every server.
func asksFor(s *kubemootv1alpha1.MCPServer, token string) bool {
	if token == "" {
		return true
	}
	for _, word := range strings.Split(s.Annotations[annoKubernetesAccess], ",") {
		if strings.TrimSpace(word) == token {
			return true
		}
	}
	return false
}

// consentsTo reports whether the namespace carries the label with the value "true" and is
// not being deleted.
func consentsTo(obj client.Object, label string) bool {
	return obj.GetLabels()[label] == readConsentGranted && obj.GetDeletionTimestamp().IsZero()
}

// anyConsent reports whether the namespace consents to at least one tier.
func anyConsent(obj client.Object) bool {
	return strings.Contains(consentState(obj), "1")
}

// consentState summarizes which tiers the namespace consents to, to compare two versions.
func consentState(obj client.Object) string {
	var b strings.Builder
	for _, t := range consentTiers {
		if consentsTo(obj, t.label) {
			b.WriteString("1")
		} else {
			b.WriteString("0")
		}
	}
	return b.String()
}

// syncNamespace brings one namespace's RoleBindings in line with its consent, tier by tier.
func (r *ReadConsentReconciler) syncNamespace(ctx context.Context, name string, servers []kubemootv1alpha1.MCPServer) error {
	ns := &corev1.Namespace{}
	if err := r.Get(ctx, types.NamespacedName{Name: name}, ns); err != nil {
		// A deleted namespace takes its RoleBindings with it.
		return client.IgnoreNotFound(err)
	}
	for _, tier := range consentTiers {
		var err error
		if consentsTo(ns, tier.label) {
			err = r.ensureTierBinding(ctx, tier, name, serverSubjects(servers, tier.token))
		} else {
			err = r.removeBinding(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: tier.binding, Namespace: name}})
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// syncCluster keeps the namespaces ClusterRoleBinding, every consenting namespace's
// RoleBindings, and removes RoleBindings left in namespaces that no longer consent.
func (r *ReadConsentReconciler) syncCluster(ctx context.Context, servers []kubemootv1alpha1.MCPServer) error {
	if err := r.ensureNamespacesBinding(ctx, serverSubjects(servers, "")); err != nil {
		return err
	}
	for _, tier := range consentTiers {
		consenting, err := r.consentingNamespaces(ctx, tier.label)
		if err != nil {
			return err
		}
		subjects := serverSubjects(servers, tier.token)
		for _, name := range consenting {
			if err := r.ensureTierBinding(ctx, tier, name, subjects); err != nil {
				return err
			}
		}
		if err := r.removeStaleBindings(ctx, tier.binding, consenting); err != nil {
			return err
		}
	}
	return nil
}

// consentingNamespaces lists, sorted, the namespaces that carry the label with the value true.
func (r *ReadConsentReconciler) consentingNamespaces(ctx context.Context, label string) ([]string, error) {
	list := &corev1.NamespaceList{}
	if err := r.List(ctx, list, client.MatchingLabels{label: readConsentGranted}); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	var out []string
	for i := range list.Items {
		if consentsTo(&list.Items[i], label) {
			out = append(out, list.Items[i].Name)
		}
	}
	slices.Sort(out)
	return out, nil
}

func readConsentLabels() map[string]string {
	return map[string]string{labelManagedBy: managedByValue, labelComponent: readConsentComponent}
}

// ensureTierBinding makes the tier's RoleBinding in a consenting namespace. With no server
// that asks for the tier there is nobody to bind, so it removes the binding instead.
func (r *ReadConsentReconciler) ensureTierBinding(ctx context.Context, tier consentTier, namespace string, subjects []rbacv1.Subject) error {
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: tier.binding, Namespace: namespace}}
	return r.ensureBinding(ctx, binding, subjects, func() {
		binding.Subjects = subjects
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: kindClusterRole, Name: tier.role}
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

// removeStaleBindings deletes the RoleBindings of that name in every namespace not in keep.
func (r *ReadConsentReconciler) removeStaleBindings(ctx context.Context, name string, keep []string) error {
	bindings := &rbacv1.RoleBindingList{}
	if err := r.List(ctx, bindings, client.MatchingLabels{labelComponent: readConsentComponent}); err != nil {
		return fmt.Errorf("list role bindings: %w", err)
	}
	for i := range bindings.Items {
		b := &bindings.Items[i]
		if b.Name != name || slices.Contains(keep, b.Namespace) {
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
	consenting, err := r.consentingNamespaces(ctx, labelReadConsent)
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
		CreateFunc:  func(e event.CreateEvent) bool { return anyConsent(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return consentState(e.ObjectOld) != consentState(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return anyConsent(e.Object) },
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
				oldS.Annotations[annoKubernetesAccess] != newS.Annotations[annoKubernetesAccess] ||
				oldS.DeletionTimestamp.IsZero() != newS.DeletionTimestamp.IsZero()
		},
	}
}
