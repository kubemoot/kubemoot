package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func discussEnv(t *testing.T, role string, channels ...string) map[string]string {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}
	agent := &kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "crew-ns"},
		Spec:       kubemootv1alpha1.AgentSpec{DiscussRole: role, DiscussChannels: channels},
	}
	out := map[string]string{}
	for _, e := range r.agentDiscussRoleEnvVars(context.Background(), agent, "demo") {
		out[e.Name] = e.Value
	}
	return out
}

// Only a coordinator turns its discussion subscriber off; every agent the
// coordinator can convene, researchers included, keeps it, or it can never answer.
func TestDiscussRoleEnv(t *testing.T) {
	coordinator := discussEnv(t, "coordinator")
	if coordinator["KUBEMOOT_DISCUSS_COORDINATOR"] != "true" || coordinator["KUBEMOOT_DISCUSS_TOOLER"] != "false" {
		t.Fatalf("coordinator env = %v", coordinator)
	}
	if coordinator["KUBEMOOT_RESUME_SEARCH_ENDPOINT"] == "" {
		t.Fatalf("coordinator needs its resume search endpoint: %v", coordinator)
	}
	for _, role := range []string{"researcher", "tooler", "analyst", ""} {
		env := discussEnv(t, role)
		if v, set := env["KUBEMOOT_DISCUSS_TOOLER"]; set {
			t.Errorf("%q: KUBEMOOT_DISCUSS_TOOLER=%q would silence a convened agent", role, v)
		}
		if _, set := env["KUBEMOOT_DISCUSS_COORDINATOR"]; set {
			t.Errorf("%q: only a coordinator runs the orchestrator", role)
		}
	}
}

func TestDiscussChannelsEnv(t *testing.T) {
	if got := discussEnv(t, "researcher", "kubernetes", "general")["KUBEMOOT_DISCUSS_CHANNELS"]; got != "kubernetes,general" {
		t.Fatalf("channels = %q", got)
	}
	if _, set := discussEnv(t, "tooler")["KUBEMOOT_DISCUSS_CHANNELS"]; set {
		t.Fatal("no channels declared means no channels env")
	}
}

// spec.discussKeywords reaches the coordinator through the resume, and
// spec.discussRelevance is deprecated: neither becomes an env var, while an
// agent that still sets them keeps its role and triage summary.
func TestDiscussIdentityEnv_DropsUnreadKeywordAndRelevanceVars(t *testing.T) {
	agent := &kubemootv1alpha1.Agent{Spec: kubemootv1alpha1.AgentSpec{
		DiscussRole:      "analyst",
		TriageSummary:    "reviews gathered data",
		DiscussKeywords:  []string{"proxmox", "zfs"},
		DiscussRelevance: &kubemootv1alpha1.DiscussRelevance{Mode: "llm", PromptHint: "hint"},
	}}
	env := map[string]string{}
	for _, e := range agentDiscussIdentityEnvVars(agent) {
		env[e.Name] = e.Value
	}
	for _, unread := range []string{"KUBEMOOT_DISCUSS_KEYWORDS", "KUBEMOOT_DISCUSS_RELEVANCE_MODE",
		"KUBEMOOT_DISCUSS_RELEVANCE_PROMPT_HINT"} {
		if _, set := env[unread]; set {
			t.Errorf("%s is injected but the runtime never reads it", unread)
		}
	}
	if env["KUBEMOOT_DISCUSS_ROLE"] != "analyst" || env["KUBEMOOT_TRIAGE_SUMMARY"] != "reviews gathered data" {
		t.Fatalf("role and triage summary must still be set: %v", env)
	}
	if got := agentDiscussIdentityEnvVars(&kubemootv1alpha1.Agent{}); len(got) != 0 {
		t.Fatalf("an agent with no discuss identity gets no env: %v", got)
	}
}
