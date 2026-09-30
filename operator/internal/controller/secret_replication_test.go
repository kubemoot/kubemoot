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
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func TestParseReplicateSecretsAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        []SecretRef
	}{
		{
			name:        "nil annotations",
			annotations: nil,
			want:        nil,
		},
		{
			name:        "annotation absent",
			annotations: map[string]string{"other": "value"},
			want:        nil,
		},
		{
			name:        "empty value",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": ""},
			want:        nil,
		},
		{
			name:        "single name uses operator namespace",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "harbor-pull-secret"},
			want:        []SecretRef{{Name: "harbor-pull-secret"}},
		},
		{
			name:        "single ns/name pair",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "homelab-pilot/proxmox-secret"},
			want:        []SecretRef{{Name: "proxmox-secret", Namespace: "homelab-pilot"}},
		},
		{
			name:        "mixed list",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "harbor-pull-secret,homelab-pilot/proxmox-secret,db-creds"},
			want: []SecretRef{
				{Name: "harbor-pull-secret"},
				{Name: "proxmox-secret", Namespace: "homelab-pilot"},
				{Name: "db-creds"},
			},
		},
		{
			name:        "whitespace trimmed",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "  foo  ,  ns1 / bar  ,baz"},
			want: []SecretRef{
				{Name: "foo"},
				{Name: "bar", Namespace: "ns1"},
				{Name: "baz"},
			},
		},
		{
			name:        "empty entries skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "foo,,bar,"},
			want: []SecretRef{
				{Name: "foo"},
				{Name: "bar"},
			},
		},
		{
			name:        "malformed entries skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "/no-ns,no-name/,ns/name/extra,ok"},
			want: []SecretRef{
				{Name: "ok"},
			},
		},
		{
			name:        "only slash skipped",
			annotations: map[string]string{"kubemoot.ai/replicate-secrets": "/"},
			want:        nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseReplicateSecretsAnnotation(tt.annotations)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseReplicateSecretsAnnotation() = %v, want %v", got, tt.want)
			}
		})
	}
}

const (
	testTenantNS   = "tenant-a"
	testOperatorNS = "kubemoot"
	testSharedNS   = "shared-db"
)

func TestSecretSourceAllowed(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	tests := []struct {
		name      string
		allowlist string
		source    string
		want      bool
	}{
		{"operator namespace", "", testOperatorNS, true},
		{"other namespace refused", "", testTenantNS, false},
		{"kube-system refused", "", testKubeSystemNS, false},
		{"allowlisted namespace", testSharedNS, testSharedNS, true},
		{"allowlist with spaces and blanks", " shared-db , ,other", "other", true},
		{"not in allowlist", testSharedNS, testTenantNS, false},
		{"empty source", testSharedNS, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(secretSourceNamespacesEnv, tc.allowlist)
			if got := secretSourceAllowed(tc.source); got != tc.want {
				t.Fatalf("secretSourceAllowed(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

func TestReplicateSecretFromRefusesDisallowedSource(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, testSharedNS)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := func(ns string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: ns},
			Data:       map[string][]byte{"password": []byte("x")},
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(secret(testTenantNS), secret(testSharedNS), secret(testOperatorNS)).Build()

	replicateSecretFrom(context.Background(), c, "db", testTenantNS, "crew-ns")
	if secretExists(context.Background(), c, "db", "crew-ns") {
		t.Fatal("secret was copied from a namespace outside the allowlist")
	}
	replicateSecretFrom(context.Background(), c, "db", testSharedNS, "crew-ns")
	if !secretExists(context.Background(), c, "db", "crew-ns") {
		t.Fatal("secret was not copied from an allowlisted namespace")
	}
	replicateSecretFrom(context.Background(), c, "db", testOperatorNS, "crew-ns-2")
	if !secretExists(context.Background(), c, "db", "crew-ns-2") {
		t.Fatal("secret was not copied from the operator namespace")
	}
}

func TestDiscoverRAGSourceDefaultsSearchesOnlyAllowedNamespaces(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, "")
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	rag := func(ns string) *kubemootv1alpha1.RAGSource {
		return &kubemootv1alpha1.RAGSource{
			ObjectMeta: metav1.ObjectMeta{Name: "docs", Namespace: ns},
			Spec: kubemootv1alpha1.RAGSourceSpec{
				VectorStore:       kubemootv1alpha1.VectorStoreConfig{Endpoint: "pg:5432", SecretRef: "db-" + ns},
				EmbeddingModelRef: "embed",
			},
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rag(testTenantNS), rag(testOperatorNS)).Build()
	r := &AgentReconciler{Client: c}

	vs, _, src := r.discoverRAGSourceDefaults(context.Background(), "crew-ns")
	if vs == nil || src != testOperatorNS {
		t.Fatalf("expected the operator namespace RAGSource, got source %q", src)
	}

	t.Setenv(secretSourceNamespacesEnv, testTenantNS)
	c = fake.NewClientBuilder().WithScheme(scheme).WithObjects(rag(testTenantNS)).Build()
	r = &AgentReconciler{Client: c}
	if _, _, src := r.discoverRAGSourceDefaults(context.Background(), "crew-ns"); src != testTenantNS {
		t.Fatalf("an allowlisted namespace should be searched, got source %q", src)
	}

	t.Setenv(secretSourceNamespacesEnv, "")
	if vs, _, src := r.discoverRAGSourceDefaults(context.Background(), "crew-ns"); vs != nil {
		t.Fatalf("a RAGSource in a non-allowed namespace was used (source %q)", src)
	}
}

func TestAllowedSecretSourceNamespacesDedupes(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", testOperatorNS)
	t.Setenv(secretSourceNamespacesEnv, "a, a,"+testOperatorNS+",b")
	want := []string{testOperatorNS, "a", "b"}
	if got := allowedSecretSourceNamespaces(); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
