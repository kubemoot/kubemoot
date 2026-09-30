package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

const (
	testKubeSystemNS = "kube-system"
	testDefaultNS    = "default"
)

func nsWith(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestNamespaceDeletableForCrew(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "kubemoot")
	optIn := func(crew string) map[string]string {
		return map[string]string{crewLabelKey: crew, managedNamespaceLabel: managedNamespaceOptIn}
	}
	terminating := nsWith("team-a", optIn("c1"))
	now := metav1.Now()
	terminating.DeletionTimestamp = &now

	tests := []struct {
		name string
		ns   *corev1.Namespace
		crew string
		want bool
	}{
		{"opted-in namespace is deletable", nsWith("team-a", optIn("c1")), "c1", true},
		{"no labels", nsWith("team-a", nil), "c1", false},
		{"operator crew label alone is not consent", nsWith("team-a", map[string]string{crewLabelKey: "c1", labelManagedBy: managedByValue}), "c1", false},
		{"opt-in label without crew label", nsWith("team-a", map[string]string{managedNamespaceLabel: managedNamespaceOptIn}), "c1", false},
		{"opted in for another crew", nsWith("team-a", optIn("c2")), "c1", false},
		{"opt-in label with wrong value", nsWith("team-a", map[string]string{crewLabelKey: "c1", managedNamespaceLabel: "no"}), "c1", false},
		{"already terminating", terminating, "c1", false},
		{testKubeSystemNS, nsWith(testKubeSystemNS, optIn("c1")), "c1", false},
		{"kube-public", nsWith("kube-public", optIn("c1")), "c1", false},
		{"kube-node-lease", nsWith("kube-node-lease", optIn("c1")), "c1", false},
		{"operator namespace", nsWith("kubemoot", optIn("c1")), "c1", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceDeletableForCrew(tc.ns, tc.crew); got != tc.want {
				t.Fatalf("namespaceDeletableForCrew = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSystemNamespacesNeverDeletable(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "kubemoot")
	for _, name := range []string{testKubeSystemNS, "kube-public", "kube-node-lease", testDefaultNS} {
		ns := nsWith(name, map[string]string{crewLabelKey: "c1", managedNamespaceLabel: managedNamespaceOptIn})
		if namespaceDeletableForCrew(ns, "c1") {
			t.Fatalf("%s must never be deletable", name)
		}
	}
}

func TestIsProtectedNamespaceFollowsOperatorNamespace(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "moot-system")
	if !isProtectedNamespace("moot-system") {
		t.Fatal("operator namespace must be protected")
	}
	if isProtectedNamespace("kubemoot") {
		t.Fatal("only the configured operator namespace is protected besides the system namespaces")
	}
}

func TestIsManagedNamespaceRequiresCrewAnnotation(t *testing.T) {
	t.Setenv("OPERATOR_NAMESPACE", "kubemoot")
	ns := nsWith("team-a", map[string]string{crewLabelKey: "c1", managedNamespaceLabel: managedNamespaceOptIn})
	r := &CrewReconciler{}
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "c1", Namespace: "team-a"}}
	if r.isManagedNamespace(ns, crew) {
		t.Fatal("a Crew without the manage-namespace annotation must not delete the namespace")
	}
	crew.Annotations = map[string]string{manageNamespaceAnno: manageNamespaceRequested}
	if !r.isManagedNamespace(ns, crew) {
		t.Fatal("annotated Crew in an opted-in namespace should be allowed")
	}
}
