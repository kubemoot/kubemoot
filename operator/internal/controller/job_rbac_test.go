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
	"reflect"
	"testing"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	rbacTestNS = "team-rbac"
	keptLabel  = "example.test/kept"
)

func rbacTestClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// getRBAC fetches obj by name in the test namespace, failing the test when absent.
func getRBAC(t *testing.T, c client.Client, name string, obj client.Object) {
	t.Helper()
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: rbacTestNS}, obj); err != nil {
		t.Fatalf("get %T %s: %v", obj, name, err)
	}
}

func mustEnsureJobRBAC(t *testing.T, c client.Client, name string, verbs []string) {
	t.Helper()
	if err := ensureJobRBAC(context.Background(), c, rbacTestNS, name, verbs); err != nil {
		t.Fatalf("ensureJobRBAC(%s): %v", name, err)
	}
}

func TestEnsureJobRBACCreatesServiceAccount(t *testing.T) {
	c := rbacTestClient(t)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	sa := &corev1.ServiceAccount{}
	getRBAC(t, c, componentRAGIndexer, sa)
	if sa.Labels[labelComponent] != componentRAGIndexer || sa.Labels[labelManagedBy] != managedByValue {
		t.Fatalf("labels: %v", sa.Labels)
	}
}

func TestEnsureJobRBACCreatesRole(t *testing.T) {
	c := rbacTestClient(t)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	role := &rbacv1.Role{}
	getRBAC(t, c, componentRAGIndexer, role)
	want := []rbacv1.PolicyRule{{APIGroups: []string{batchv1.GroupName}, Resources: []string{jobsResource}, Verbs: ragIndexerJobVerbs}}
	if !reflect.DeepEqual(role.Rules, want) {
		t.Fatalf("rules: %+v", role.Rules)
	}
}

func TestEnsureJobRBACBindsTheServiceAccount(t *testing.T) {
	c := rbacTestClient(t)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	rb := &rbacv1.RoleBinding{}
	getRBAC(t, c, componentRAGIndexer, rb)
	wantSubjects := []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: componentRAGIndexer, Namespace: rbacTestNS}}
	if !reflect.DeepEqual(rb.Subjects, wantSubjects) {
		t.Fatalf("subjects: %+v", rb.Subjects)
	}
	wantRef := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: componentRAGIndexer}
	if rb.RoleRef != wantRef {
		t.Fatalf("roleRef: %+v", rb.RoleRef)
	}
}

func TestEnsureJobRBACIsIdempotent(t *testing.T) {
	c := rbacTestClient(t)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
}

func TestEnsureJobRBACUpdatesAStaleRole(t *testing.T) {
	stale := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: componentRAGIndexer, Namespace: rbacTestNS, Labels: map[string]string{keptLabel: "someone-else"}},
		Rules:      []rbacv1.PolicyRule{{APIGroups: []string{batchv1.GroupName}, Resources: []string{jobsResource}, Verbs: []string{verbGet}}},
	}
	c := rbacTestClient(t, stale)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	role := &rbacv1.Role{}
	getRBAC(t, c, componentRAGIndexer, role)
	if !reflect.DeepEqual(role.Rules[0].Verbs, ragIndexerJobVerbs) {
		t.Fatalf("verbs not updated: %v", role.Rules[0].Verbs)
	}
	if role.Labels[keptLabel] != "someone-else" || role.Labels[labelComponent] != componentRAGIndexer {
		t.Fatalf("labels: %v", role.Labels)
	}
}

func TestEnsureJobRBACKeepsComponentsApart(t *testing.T) {
	c := rbacTestClient(t)
	mustEnsureJobRBAC(t, c, componentFitnessRunner, fitnessRunnerJobVerbs)
	mustEnsureJobRBAC(t, c, componentRAGIndexer, ragIndexerJobVerbs)
	fitness := &rbacv1.Role{}
	getRBAC(t, c, componentFitnessRunner, fitness)
	if !reflect.DeepEqual(fitness.Rules[0].Verbs, fitnessRunnerJobVerbs) {
		t.Fatalf("fitness verbs changed: %v", fitness.Rules[0].Verbs)
	}
}

func TestIndexingJobServiceAccount(t *testing.T) {
	r := &RAGSourceReconciler{Client: rbacTestClient(t), ConfigCache: NewConfigCache()}
	em := &kubemootv1alpha1.EmbeddingModel{
		ObjectMeta: metav1.ObjectMeta{Name: FallbackEmbeddingModel, Namespace: rbacTestNS},
		Spec:       kubemootv1alpha1.EmbeddingModelSpec{Model: "nomic-embed-text"},
	}
	rag := func(indexer *kubemootv1alpha1.IndexerConfig) *kubemootv1alpha1.RAGSource {
		return &kubemootv1alpha1.RAGSource{
			ObjectMeta: metav1.ObjectMeta{Name: "course", Namespace: rbacTestNS},
			Spec: kubemootv1alpha1.RAGSourceSpec{
				Source: kubemootv1alpha1.SourceConfig{
					Type: kubemootv1alpha1.RAGSourceTypeGit,
					Git:  &kubemootv1alpha1.GitSource{URL: "https://github.com/Akuli/python-tutorial", Paths: []string{"basics"}},
				},
				Indexer: indexer,
			},
		}
	}

	if got := r.buildIndexingJob(rag(nil), em, "course-indexer-1").Spec.Template.Spec.ServiceAccountName; got != componentRAGIndexer {
		t.Fatalf("default ServiceAccountName = %q, want %q", got, componentRAGIndexer)
	}
	custom := &kubemootv1alpha1.IndexerConfig{ServiceAccountName: "mine"}
	if got := r.buildIndexingJob(rag(custom), em, "course-indexer-1").Spec.Template.Spec.ServiceAccountName; got != "mine" {
		t.Fatalf("named ServiceAccountName = %q, want mine", got)
	}
}
