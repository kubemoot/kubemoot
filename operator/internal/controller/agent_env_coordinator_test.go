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
	return coordinatorEnvValues(t, envCoordinatorEventLog, coordinator, agentEnv...)
}

// coordinatorEnvValues builds the env of a coordinator Agent in a realm with the given
// coordinator block and returns every value of the variable called name.
func coordinatorEnvValues(t *testing.T, name string, coordinator *kubemootv1alpha1.CoordinatorConfig,
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
		if e.Name == name {
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

func TestCoordinatorResumeEnv_AbsentUnlessTheRealmTurnsItOn(t *testing.T) {
	if got := coordinatorEnvValues(t, envCoordinatorResume, nil); len(got) != 0 {
		t.Fatalf("no coordinator block: want no variable, got %v", got)
	}
	if got := coordinatorEnvValues(t, envCoordinatorResume,
		&kubemootv1alpha1.CoordinatorConfig{EventLog: true}); len(got) != 0 {
		t.Fatalf("resume false: want no variable, got %v", got)
	}
}

func TestCoordinatorResumeEnv_SetOnTheCoordinatorWhenTheRealmTurnsItOn(t *testing.T) {
	got := coordinatorEnvValues(t, envCoordinatorResume,
		&kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true})
	if len(got) != 1 || got[0] != valueTrue {
		t.Fatalf("want KUBEMOOT_COORDINATOR_RESUME=true once, got %v", got)
	}
}

func TestCoordinatorResumeEnv_TheAgentsOwnValueComesLastAndWins(t *testing.T) {
	got := coordinatorEnvValues(t, envCoordinatorResume,
		&kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true},
		corev1.EnvVar{Name: envCoordinatorResume, Value: valueFalse})
	if len(got) != 2 || got[len(got)-1] != valueFalse {
		t.Fatalf("the Agent's own env must follow the realm's so it wins, got %v", got)
	}
}

// testResumeMaxAge is a resume age limit the tests set on the realm.
const testResumeMaxAge = "45m"

func TestCoordinatorResumeMaxAgeEnv_FollowsResumeAndCarriesTheRealmsValue(t *testing.T) {
	cases := []struct {
		name   string
		config *kubemootv1alpha1.CoordinatorConfig
		want   []string
	}{
		{"resume off: no limit variable", &kubemootv1alpha1.CoordinatorConfig{ResumeMaxAge: testResumeMaxAge}, nil},
		{"resume on, no limit set: none", &kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true}, nil},
		{"resume on, limit set", &kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true, ResumeMaxAge: testResumeMaxAge}, []string{testResumeMaxAge}},
		{"resume on, limit off", &kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true, ResumeMaxAge: "0"}, []string{"0"}},
	}
	for _, c := range cases {
		got := coordinatorEnvValues(t, envCoordinatorResumeMaxAge, c.config)
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("%s: want %v, got %v", c.name, c.want, got)
		}
	}
}

func TestCoordinatorResumeMaxAgeEnv_TheAgentsOwnValueComesLastAndWins(t *testing.T) {
	got := coordinatorEnvValues(t, envCoordinatorResumeMaxAge,
		&kubemootv1alpha1.CoordinatorConfig{EventLog: true, Resume: true, ResumeMaxAge: "30m"},
		corev1.EnvVar{Name: envCoordinatorResumeMaxAge, Value: "5m"})
	if len(got) != 2 || got[len(got)-1] != "5m" {
		t.Fatalf("the Agent's own env must follow the realm's so it wins, got %v", got)
	}
}

func TestConfigCache_CoordinatorResumeMaxAge(t *testing.T) {
	cache := NewConfigCache()
	if got := cache.GetCoordinatorResumeMaxAge(); got != "" {
		t.Fatalf("an empty cache has no limit, got %q", got)
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Coordinator: &kubemootv1alpha1.CoordinatorConfig{ResumeMaxAge: "1h"}}})
	if got := cache.GetCoordinatorResumeMaxAge(); got != "1h" {
		t.Fatalf("want 1h, got %q", got)
	}
}

func TestConfigCache_CoordinatorResumeIsOffWithoutAConfigOrABlock(t *testing.T) {
	cache := NewConfigCache()
	if cache.GetCoordinatorResume() {
		t.Fatal("an empty cache must report resume off")
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Coordinator: &kubemootv1alpha1.CoordinatorConfig{EventLog: true}}})
	if cache.GetCoordinatorResume() {
		t.Fatal("a config that leaves resume off must report it off")
	}
	cache.Update(&kubemootv1alpha1.KubemootConfig{Spec: kubemootv1alpha1.KubemootConfigSpec{
		Coordinator: &kubemootv1alpha1.CoordinatorConfig{Resume: true}}})
	if !cache.GetCoordinatorResume() {
		t.Fatal("resume true must be reported")
	}
}
