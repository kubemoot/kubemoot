package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	realmWithoutConfig = "realm without a KubemootConfig"
	roleAnalystName    = "analyst"
)

// perCallArtifactsEnv builds the env of an Agent with the given discuss role in a realm with the
// given artifacts block (nil: no KubemootConfig at all) and returns every value of the variable.
func perCallArtifactsEnv(t *testing.T, role string, artifacts *kubemootv1alpha1.ArtifactsConfig,
	agentEnv ...corev1.EnvVar) []string {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cache := NewConfigCache()
	if artifacts != nil {
		cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Artifacts: *artifacts}})
	}
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-workloads", Namespace: testCrewNamespace,
			Labels: map[string]string{labelCrew: testCrewName}},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: role},
	}
	if len(agentEnv) > 0 {
		agent.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Env: agentEnv}
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build(), ConfigCache: cache}
	pick := &modelPick{ModelID: testModel32B, Endpoint: testOllamaURL}

	var values []string
	for _, e := range r.buildEnvVars(context.Background(), agent, pick, pick, 8080) {
		if e.Name == envPerCallArtifacts {
			values = append(values, e.Value)
		}
	}
	return values
}

func TestPerCallArtifactsEnv_AbsentUnlessTheRealmTurnsItOn(t *testing.T) {
	for name, artifacts := range map[string]*kubemootv1alpha1.ArtifactsConfig{
		realmWithoutConfig: nil,
		"artifacts unset":  {},
		"explicitly off":   {PerCallArtifacts: false},
	} {
		if got := perCallArtifactsEnv(t, roleToolerResume, artifacts); len(got) != 0 {
			t.Errorf("%s: want no variable, got %v", name, got)
		}
	}
}

func TestPerCallArtifactsEnv_SetOnAToolerWhenTheRealmTurnsItOn(t *testing.T) {
	got := perCallArtifactsEnv(t, roleToolerResume, &kubemootv1alpha1.ArtifactsConfig{PerCallArtifacts: true})
	if len(got) != 1 || got[0] != valueTrue {
		t.Fatalf("want KUBEMOOT_PER_CALL_ARTIFACTS=true once, got %v", got)
	}
}

func TestPerCallArtifactsEnv_OnlyAToolerGetsIt(t *testing.T) {
	on := &kubemootv1alpha1.ArtifactsConfig{PerCallArtifacts: true}
	for _, role := range []string{roleCoordinator, roleAnalystName, testResearcher, ""} {
		if got := perCallArtifactsEnv(t, role, on); len(got) != 0 {
			t.Errorf("role %q: want no variable, got %v", role, got)
		}
	}
}

func TestPerCallArtifactsEnv_TheAgentsOwnValueComesLastAndWins(t *testing.T) {
	got := perCallArtifactsEnv(t, roleToolerResume, &kubemootv1alpha1.ArtifactsConfig{PerCallArtifacts: true},
		corev1.EnvVar{Name: envPerCallArtifacts, Value: valueFalse})
	if len(got) != 2 || got[len(got)-1] != valueFalse {
		t.Fatalf("want the agent's own false last, got %v", got)
	}
}
