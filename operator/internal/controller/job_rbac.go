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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// jobsResource is the batch resource a Job's own ServiceAccount is granted verbs on.
const jobsResource = "jobs"

// Verbs a Job's own ServiceAccount is granted on Jobs in its namespace.
const (
	verbGet    = "get"
	verbPatch  = "patch"
	verbUpdate = "update"
)

var (
	// fitnessRunnerJobVerbs let the fitness runner annotate its Job with results.
	fitnessRunnerJobVerbs = []string{verbGet, verbPatch}
	// ragIndexerJobVerbs let the RAG indexer annotate its Job with results (fabric8's edit reads, then patches).
	ragIndexerJobVerbs = []string{verbGet, verbPatch, verbUpdate}
)

// ensureJobRBAC makes a ServiceAccount, Role and RoleBinding, all called name, in ns,
// so a Job's pod running as that ServiceAccount may use verbs on Jobs in its own
// namespace. The fitness runner and the RAG indexer use it to annotate their own Job
// with results. Existing objects are brought to the wanted rules and binding.
func ensureJobRBAC(ctx context.Context, c client.Client, ns, name string, verbs []string) error {
	meta := func() metav1.ObjectMeta { return metav1.ObjectMeta{Name: name, Namespace: ns} }
	labels := map[string]string{labelManagedBy: managedByValue, labelComponent: name}

	sa := &corev1.ServiceAccount{ObjectMeta: meta()}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, sa, func() error {
		sa.Labels = mergeLabels(sa.Labels, labels)
		return nil
	}); err != nil {
		return fmt.Errorf("ensure service account %s/%s: %w", ns, name, err)
	}

	role := &rbacv1.Role{ObjectMeta: meta()}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, role, func() error {
		role.Labels = mergeLabels(role.Labels, labels)
		role.Rules = []rbacv1.PolicyRule{{APIGroups: []string{batchv1.GroupName}, Resources: []string{jobsResource}, Verbs: verbs}}
		return nil
	}); err != nil {
		return fmt.Errorf("ensure role %s/%s: %w", ns, name, err)
	}

	// A RoleBinding's roleRef is immutable. Rewriting it on every call is safe only
	// because the Role, the ServiceAccount and the binding all share one name.
	binding := &rbacv1.RoleBinding{ObjectMeta: meta()}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, binding, func() error {
		binding.Labels = mergeLabels(binding.Labels, labels)
		binding.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: ns}}
		binding.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}
		return nil
	}); err != nil {
		return fmt.Errorf("ensure role binding %s/%s: %w", ns, name, err)
	}
	return nil
}

// mergeLabels returns existing with wanted laid over it, keeping labels others added.
func mergeLabels(existing, wanted map[string]string) map[string]string {
	out := make(map[string]string, len(existing)+len(wanted))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range wanted {
		out[k] = v
	}
	return out
}
