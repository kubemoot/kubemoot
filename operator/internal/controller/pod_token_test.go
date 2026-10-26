package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

func TestAutomountTokenFor(t *testing.T) {
	if got := automountTokenFor(""); got == nil || *got {
		t.Errorf("no ServiceAccount named: want automount false, got %v", got)
	}
	if got := automountTokenFor(defaultServiceAccount); got == nil || *got {
		t.Errorf("the default account named explicitly is the same as none, got %v", got)
	}
	if got := automountTokenFor("kubemoot-kubernetes-mcp"); got != nil {
		t.Errorf("a named ServiceAccount keeps the Kubernetes default (nil), got %v", *got)
	}
}

func TestMCPServerPod_TokenAutomount(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	build := func(sa string) *bool {
		mcp := &kubemootv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "tool", Namespace: "ns"},
			Spec: kubemootv1alpha1.MCPServerSpec{
				Image: "tool:1", Transport: kubemootv1alpha1.TransportHTTP, ServiceAccountName: sa,
			},
		}
		return r.buildDeployment(mcp).Spec.Template.Spec.AutomountServiceAccountToken
	}
	if got := build(""); got == nil || *got {
		t.Errorf("an MCPServer without a ServiceAccount must get no token, got %v", got)
	}
	if got := build(defaultServiceAccount); got == nil || *got {
		t.Errorf("an MCPServer naming the default account must get no token, got %v", got)
	}
	if got := build("cluster-reader"); got != nil {
		t.Errorf("an MCPServer with a ServiceAccount keeps the default, got %v", *got)
	}
}

func TestAgentPod_TokenAutomount(t *testing.T) {
	scheme := agentReconcileScheme(t)
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme, ConfigCache: NewConfigCache()}
	build := func(sa string) *bool {
		agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: testNS1}}
		if sa != "" {
			agent.Spec.Deployment = &kubemootv1alpha1.DeploymentConfig{ServiceAccountName: sa}
		}
		pick := &modelPick{
			ModelID:  testModelID,
			Endpoint: "http://" + testRig0 + ":11434",
			Server:   &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: testRig0}},
		}
		d := r.buildDeployment(context.Background(), agent, pick, pick, "cm", "hash")
		return d.Spec.Template.Spec.AutomountServiceAccountToken
	}
	if got := build(""); got == nil || *got {
		t.Errorf("an Agent without a ServiceAccount must get no token, got %v", got)
	}
	if got := build(defaultServiceAccount); got == nil || *got {
		t.Errorf("an Agent naming the default account must get no token, got %v", got)
	}
	if got := build("agent-sa"); got != nil {
		t.Errorf("an Agent with a ServiceAccount keeps the default, got %v", *got)
	}
}

// With no token mounted the agent cannot read its namespace from the ServiceAccount files,
// so the downward-API KUBEMOOT_NAMESPACE must stay set.
func TestPodsWithoutToken_KeepNamespaceEnv(t *testing.T) {
	scheme := agentReconcileScheme(t)
	r := &AgentReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme, ConfigCache: NewConfigCache()}
	pick := &modelPick{ModelID: testModelID, Endpoint: "http://" + testRig0 + ":11434",
		Server: &kubemootv1alpha1.ModelServer{ObjectMeta: metav1.ObjectMeta{Name: testRig0}}}
	agent := &kubemootv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "a1", Namespace: testNS1}}
	pod := r.buildDeployment(context.Background(), agent, pick, pick, "cm", "hash").Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("precondition: the pod mounts no token")
	}
	for _, e := range pod.Containers[0].Env {
		if e.Name == crewscope.NamespaceEnv && e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			return
		}
	}
	t.Error("a pod without a token needs KUBEMOOT_NAMESPACE from the downward API")
}

func TestMCPServerPodWithoutToken_KeepsNamespaceEnv(t *testing.T) {
	r := &MCPServerReconciler{ConfigCache: NewConfigCache()}
	mcp := &kubemootv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "tool", Namespace: "ns"},
		Spec:       kubemootv1alpha1.MCPServerSpec{Image: "tool:1", Transport: kubemootv1alpha1.TransportHTTP},
	}
	for _, e := range r.buildDeployment(mcp).Spec.Template.Spec.Containers[0].Env {
		if e.Name == crewscope.NamespaceEnv {
			return
		}
	}
	t.Error("an MCP server pod without a token needs KUBEMOOT_NAMESPACE")
}
