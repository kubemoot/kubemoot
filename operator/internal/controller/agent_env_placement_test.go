package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	testPlacementShadow  = "shadow"
	testPlacementLearned = "learned"
	testPlacementCrew    = "demo"
	testPlacementNS      = "crew-ns"
)

func placementPolicyCR(name, crew, placement string) *kubemootv1alpha1.CrewSchedulingPolicy {
	return &kubemootv1alpha1.CrewSchedulingPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testPlacementNS},
		Spec: kubemootv1alpha1.CrewSchedulingPolicySpec{
			CrewRef:         crew,
			PlacementPolicy: placement,
			Rules:           []kubemootv1alpha1.SchedulingRule{{Phase: phaseMulling}},
		},
	}
}

// placementEnv returns the placement env value for an agent of crew "demo",
// with the realm default and the given CrewSchedulingPolicies in the cluster.
func placementEnv(t *testing.T, realm string, objs ...client.Object) string {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Defaults: kubemootv1alpha1.DefaultConfig{Scheduler: &kubemootv1alpha1.SchedulerConfig{PlacementPolicy: realm}},
	}})
	r := &AgentReconciler{
		Client:      fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Scheme:      scheme,
		ConfigCache: cache,
	}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{
		Name: "a", Namespace: testPlacementNS, Labels: map[string]string{labelCrew: testPlacementCrew},
	}}
	e := r.placementPolicyEnvVar(context.Background(), agent)
	if e.Name != envSchedulerPlacementPolicy {
		t.Fatalf("env name = %q", e.Name)
	}
	return e.Value
}

func TestPlacementPolicyEnv_DefaultsToAlgorithm(t *testing.T) {
	if got := placementEnv(t, ""); got != defaultPlacementPolicy {
		t.Fatalf("no realm or crew setting: got %q, want %q", got, defaultPlacementPolicy)
	}
}

func TestPlacementPolicyEnv_RealmDefaultApplies(t *testing.T) {
	if got := placementEnv(t, testPlacementShadow); got != testPlacementShadow {
		t.Fatalf("realm shadow: got %q", got)
	}
}

func TestPlacementPolicyEnv_CrewOverridesRealm(t *testing.T) {
	got := placementEnv(t, testPlacementShadow, placementPolicyCR("p", testPlacementCrew, testPlacementLearned))
	if got != testPlacementLearned {
		t.Fatalf("crew override: got %q, want %q", got, testPlacementLearned)
	}
}

func TestPlacementPolicyEnv_CrewWithoutPlacementInheritsRealm(t *testing.T) {
	got := placementEnv(t, testPlacementShadow, placementPolicyCR("p", testPlacementCrew, ""))
	if got != testPlacementShadow {
		t.Fatalf("crew policy without placementPolicy: got %q, want realm %q", got, testPlacementShadow)
	}
}

func TestPlacementPolicyEnv_OtherCrewsPolicyIgnored(t *testing.T) {
	got := placementEnv(t, "", placementPolicyCR("p", "other-crew", testPlacementLearned))
	if got != defaultPlacementPolicy {
		t.Fatalf("another crew's policy leaked: got %q", got)
	}
}

func TestPlacementPolicyEnv_NoConfigCache(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: testPlacementNS}}
	if got := r.placementPolicyEnvVar(context.Background(), agent).Value; got != defaultPlacementPolicy {
		t.Fatalf("got %q, want %q", got, defaultPlacementPolicy)
	}
}

func TestPolicyAppliesTo(t *testing.T) {
	p := placementPolicyCR("p", testPlacementCrew, "")
	if !policyAppliesTo(p, testPlacementCrew) || !policyAppliesTo(p, "") || policyAppliesTo(p, "other") {
		t.Fatal("crewRef matching is wrong")
	}
	if !policyAppliesTo(placementPolicyCR("q", "", ""), "any") {
		t.Fatal("a policy without crewRef applies to every crew")
	}
}
