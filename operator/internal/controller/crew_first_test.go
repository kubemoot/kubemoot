package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/netpol"
)

func TestGeneratesPolicies(t *testing.T) {
	var none *NetPolicySync
	if none.GeneratesPolicies() {
		t.Error("a nil sync generates nothing")
	}
	if (&NetPolicySync{Config: netpol.Config{Enabled: false}}).GeneratesPolicies() {
		t.Error("the flag off generates nothing")
	}
	if !(&NetPolicySync{Config: netpol.Config{Enabled: true}}).GeneratesPolicies() {
		t.Error("the flag on generates policies")
	}
}

func TestFirstCrewIn(t *testing.T) {
	crew := func(ns, name string) *kubemootv1alpha1.Crew {
		return &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	}
	cli := fake.NewClientBuilder().WithScheme(scopeTestScheme(t)).
		WithObjects(crew(testNamespaceA, "zeta"), crew(testNamespaceA, "alpha"), crew(testNamespaceB, testMismatchName)).Build()

	cases := []struct{ namespace, want string }{
		{testNamespaceA, "alpha"},
		{testNamespaceB, testMismatchName},
		{"empty-ns", ""},
	}
	for _, tc := range cases {
		got, err := firstCrewIn(context.Background(), cli, tc.namespace)
		if err != nil || got != tc.want {
			t.Errorf("firstCrewIn(%q) = %q, %v; want %q", tc.namespace, got, err, tc.want)
		}
	}
	holds, err := namespaceHoldsCrew(context.Background(), cli, "empty-ns")
	if err != nil || holds {
		t.Errorf("an empty namespace holds no Crew: %v %v", holds, err)
	}
}
