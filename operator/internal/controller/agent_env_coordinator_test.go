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

// coordinatorEventLogEnv builds the env of a coordinator Agent in a realm with the given
// KubemootConfig coordinator block (nil: none) and returns every value of the variable.
func coordinatorEventLogEnv(t *testing.T, coordinator *kubemootv1alpha1.CoordinatorConfig,
	agentEnv ...corev1.EnvVar) []string {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{Coordinator: coordinator}})
	coord := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: testRoleCoordinator, Namespace: testCrewNamespace,
			Labels: map[string]string{labelCrew: testCrewName}},
		Spec: kubemootv1alpha1.AgentSpec{DiscussRole: testRoleCoordinator},
	}
	if len(agentEnv) > 0 {
		coord.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{Env: agentEnv}
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(coord).Build(), ConfigCache: cache}
	pick := &modelPick{ModelID: testModel32B, Endpoint: testOllamaURL}

	var values []string
	for _, e := range r.buildEnvVars(context.Background(), coord, pick, pick, 8080) {
		if e.Name == envCoordinatorEventLog {
			values = append(values, e.Value)
		}
	}
	return values
}

func TestCoordinatorEventLogEnv_AbsentUnlessTheRealmTurnsItOn(t *testing.T) {
	if got := coordinatorEventLogEnv(t, nil); len(got) != 0 {
		t.Fatalf("no coordinator block: want no variable, got %v", got)
	}
	if got := coordinatorEventLogEnv(t, &kubemootv1alpha1.CoordinatorConfig{EventLog: false}); len(got) != 0 {
		t.Fatalf("eventLog false: want no variable, got %v", got)
	}
}

func TestCoordinatorEventLogEnv_SetOnTheCoordinatorWhenTheRealmTurnsItOn(t *testing.T) {
	got := coordinatorEventLogEnv(t, &kubemootv1alpha1.CoordinatorConfig{EventLog: true})
	if len(got) != 1 || got[0] != valueTrue {
		t.Fatalf("want KUBEMOOT_COORDINATOR_EVENT_LOG=true once, got %v", got)
	}
}

func TestCoordinatorEventLogEnv_TheAgentsOwnValueComesLastAndWins(t *testing.T) {
	got := coordinatorEventLogEnv(t, &kubemootv1alpha1.CoordinatorConfig{EventLog: true},
		corev1.EnvVar{Name: envCoordinatorEventLog, Value: valueFalse})
	if len(got) != 2 || got[len(got)-1] != valueFalse {
		t.Fatalf("the Agent's own env must follow the realm's so it wins, got %v", got)
	}
}

func TestCoordinatorEventLogEnv_NotSetOnAnAgentThatIsNotACoordinator(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cache := NewConfigCache()
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Coordinator: &kubemootv1alpha1.CoordinatorConfig{EventLog: true}}})
	tooler := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "k8s-metrics", Namespace: testCrewNamespace},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: roleToolerResume},
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(tooler).Build(), ConfigCache: cache}
	pick := &modelPick{ModelID: testModel32B, Endpoint: testOllamaURL}

	for _, e := range r.buildEnvVars(context.Background(), tooler, pick, pick, 8080) {
		if e.Name == envCoordinatorEventLog {
			t.Fatalf("a tooler must not get the coordinator's variable: %v", e)
		}
	}
}

func TestConfigCache_CoordinatorEventLogIsOffWithoutAConfigOrABlock(t *testing.T) {
	cache := NewConfigCache()
	if cache.GetCoordinatorEventLog() {
		t.Fatal("an empty cache must report the log off")
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{})
	if cache.GetCoordinatorEventLog() {
		t.Fatal("a config with no coordinator block must report the log off")
	}
}
