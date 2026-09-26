package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kubemootv1alpha1 "github.com/javajon/kubemoot/operator/api/v1alpha1"
)

func agentIn(name, phase, message string) kubemootv1alpha1.Agent {
	return kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "crew-x", Labels: map[string]string{crewLabelKey: "x"}},
		Status:     kubemootv1alpha1.AgentStatus{Phase: phase, Message: message},
	}
}

func TestAssessCrew(t *testing.T) {
	running := agentIn("coordinator", agentPhaseRunning, "")
	stuckCoord := agentIn("coordinator", agentPhaseUnschedulable, "no feasible Model for mulling phase")
	newCoord := agentIn("coordinator", "", "")
	tooler := agentIn("k8s", agentPhaseRunning, "")
	stuckA := agentIn("obs", agentPhaseUnschedulable, "no ready provider")
	stuckB := agentIn("gpu", agentPhaseUnschedulable, "no ready provider")

	cases := []struct {
		name        string
		agents      []kubemootv1alpha1.Agent
		coordinator string
		phase       string
		ready       bool
		contains    string
	}{
		{"no coordinator", []kubemootv1alpha1.Agent{tooler}, "", crewPhasePending, false, "No coordinator"},
		{"coordinator missing from list", []kubemootv1alpha1.Agent{tooler}, "coordinator", crewPhasePending, false, "not found"},
		{"coordinator unschedulable", []kubemootv1alpha1.Agent{stuckCoord, tooler}, "coordinator", crewPhaseDegraded, false, "coordinator coordinator is unschedulable: no feasible Model"},
		{"coordinator not yet running", []kubemootv1alpha1.Agent{newCoord, tooler}, "coordinator", crewPhasePending, false, "coordinator coordinator is pending"},
		{"all specialists unschedulable", []kubemootv1alpha1.Agent{running, stuckB, stuckA}, "coordinator", crewPhaseDegraded, false, "no specialist can be scheduled: gpu, obs"},
		{"some specialists unschedulable", []kubemootv1alpha1.Agent{running, tooler, stuckA}, "coordinator", crewPhaseReady, true, "3 agents, coordinator=coordinator; unschedulable: obs"},
		{"all running", []kubemootv1alpha1.Agent{running, tooler}, "coordinator", crewPhaseReady, true, "Crew operational: 2 agents"},
		{"coordinator alone", []kubemootv1alpha1.Agent{running}, "coordinator", crewPhaseReady, true, "1 agents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assessCrew(tc.agents, tc.coordinator)
			if got.phase != tc.phase || got.ready != tc.ready || !strings.Contains(got.message, tc.contains) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestEnqueueCrewForAgent(t *testing.T) {
	h := enqueueCrewForAgent()
	labelled := agentIn("k8s", agentPhaseRunning, "")
	unlabelled := agentIn("stray", agentPhaseRunning, "")
	unlabelled.Labels = nil
	reqs := mapRequests(t, h, &labelled)
	if len(reqs) != 1 || reqs[0].Name != "x" || reqs[0].Namespace != "crew-x" {
		t.Fatalf("labelled agent enqueued %v", reqs)
	}
	if reqs := mapRequests(t, h, &unlabelled); len(reqs) != 0 {
		t.Fatalf("unlabelled agent enqueued %v", reqs)
	}
}
