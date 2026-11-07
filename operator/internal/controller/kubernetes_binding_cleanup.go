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

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// The Kubernetes MCP servers' access to the cluster is granted by the crew chart that deploys
// them, with its own ServiceAccount, roles and bindings. The operator grants none of it. Earlier
// releases did, and the functions here remove what those releases left behind.
const (
	// kindClusterRole is the roleRef kind of a ClusterRole.
	kindClusterRole = "ClusterRole"

	// legacyKubernetesBinding is the ClusterRoleBinding the first releases made, bound to
	// legacyKubernetesRole.
	legacyKubernetesBinding = "kubemoot-kubernetes-mcp-binding"
	legacyKubernetesRole    = "kubemoot-mcpserver-creator"
	// legacyKubernetesAccount is the one subject of that binding.
	legacyKubernetesAccount = "kubemoot-kubernetes-mcp"

	// readConsentComponent is the app.kubernetes.io/component value of the RoleBindings and the
	// ClusterRoleBinding the namespace read-consent controller made from namespace labels.
	readConsentComponent = "kubernetes-mcp-read-consent"
	// readConsentNamespacesBinding is the name of the ClusterRoleBinding that controller kept
	// so the servers could list namespaces.
	readConsentNamespacesBinding = "kubemoot-kubernetes-mcp-namespaces"
)

// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterrolebindings,verbs=get;delete

// RemoveLegacyKubernetesBinding deletes the ClusterRoleBinding earlier releases made, which
// granted the Kubernetes MCP server's ServiceAccount the creator role in every namespace. It
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

// RemoveReadConsentBindings deletes the RoleBindings and the ClusterRoleBinding the namespace
// read-consent controller made from the namespace labels kubemoot.ai/crew-read,
// crew-read-secrets and crew-write. It finds them by the labels
// app.kubernetes.io/managed-by=kubemoot-operator and
// app.kubernetes.io/component=kubernetes-mcp-read-consent, so a binding anyone else made stays.
//
// It runs once at start and is safe to run again. The crew chart now grants the access with its
// own bindings, so upgrade the crew chart before the operator: the crew reads nothing between
// this deletion and the chart's bindings. Reads go through reader, which sees the API server;
// writes go through c.
func RemoveReadConsentBindings(ctx context.Context, reader client.Reader, c client.Client) error {
	log := logf.FromContext(ctx)
	owned := client.MatchingLabels{labelManagedBy: managedByValue, labelComponent: readConsentComponent}

	bindings := &rbacv1.RoleBindingList{}
	if err := reader.List(ctx, bindings, owned); err != nil {
		return fmt.Errorf("list read-consent role bindings: %w", err)
	}
	for i := range bindings.Items {
		b := &bindings.Items[i]
		log.Info("Removing a namespace read-consent RoleBinding", "namespace", b.Namespace, "binding", b.Name)
		if err := client.IgnoreNotFound(c.Delete(ctx, b)); err != nil {
			return fmt.Errorf("delete role binding %s/%s: %w", b.Namespace, b.Name, err)
		}
	}

	cluster := &rbacv1.ClusterRoleBinding{}
	if err := reader.Get(ctx, types.NamespacedName{Name: readConsentNamespacesBinding}, cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	if cluster.Labels[labelComponent] != readConsentComponent || cluster.Labels[labelManagedBy] != managedByValue {
		return nil
	}
	log.Info("Removing the namespace read-consent ClusterRoleBinding", "binding", readConsentNamespacesBinding)
	return client.IgnoreNotFound(c.Delete(ctx, cluster))
}
