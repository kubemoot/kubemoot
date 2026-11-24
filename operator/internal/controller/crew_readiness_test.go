package controller

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

func agentIn(name, phase, message string) kubemootv1alpha1.Agent {
	return kubemootv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testCrewNamespace, Labels: map[string]string{labelCrew: "x"}},
		Status:     kubemootv1alpha1.AgentStatus{Phase: phase, Message: message},
	}
}

// requireCapacity fails unless the capacity answer has the wanted status; an empty want
// means the answer must be absent.
func requireCapacity(t *testing.T, got *crewCapacity, want metav1.ConditionStatus) {
	t.Helper()
	if want == "" {
		if got != nil {
			t.Fatalf("capacity %+v, want none", got)
		}
		return
	}
	if got == nil || got.status != want {
		t.Fatalf("capacity %+v, want %s", got, want)
	}
}

func TestAssessCrew(t *testing.T) {
	running := agentIn(testRoleCoordinator, agentPhaseRunning, "")
	stuckCoord := agentIn(testRoleCoordinator, agentPhaseUnschedulable, "no feasible Model for mulling phase")
	newCoord := agentIn(testRoleCoordinator, "", "")
	failedCoord := agentIn(testRoleCoordinator, phaseError, "harness spec cannot be built")
	tooler := agentIn(testK8sAgent, agentPhaseRunning, "")
	stuckA := agentIn("obs", agentPhaseUnschedulable, "no ready server")
	stuckB := agentIn("gpu", agentPhaseUnschedulable, "no ready server")

	cases := []struct {
		name        string
		agents      []kubemootv1alpha1.Agent
		coordinator string
		phase       string
		ready       bool
		contains    string
		capacity    metav1.ConditionStatus
	}{
		{"no coordinator", []kubemootv1alpha1.Agent{tooler}, "", crewPhasePending, false, "No coordinator", metav1.ConditionUnknown},
		{"coordinator missing from list", []kubemootv1alpha1.Agent{tooler}, testRoleCoordinator, crewPhasePending, false, "not found", metav1.ConditionUnknown},
		{"coordinator unschedulable: capacity absent, crew reconciled", []kubemootv1alpha1.Agent{stuckCoord, tooler}, testRoleCoordinator, crewPhaseWaitingForCapacity, true, "coordinator coordinator is unschedulable: no feasible Model", metav1.ConditionFalse},
		{"coordinator not yet running", []kubemootv1alpha1.Agent{newCoord, tooler}, testRoleCoordinator, crewPhasePending, false, "coordinator coordinator is pending", metav1.ConditionUnknown},
		{"coordinator in error is a real failure", []kubemootv1alpha1.Agent{failedCoord, tooler}, testRoleCoordinator, crewPhasePending, false, "coordinator coordinator is error", metav1.ConditionUnknown},
		{"all specialists unschedulable", []kubemootv1alpha1.Agent{running, stuckB, stuckA}, testRoleCoordinator, crewPhaseWaitingForCapacity, true, "no specialist can be scheduled: gpu, obs", metav1.ConditionFalse},
		{"some specialists unschedulable", []kubemootv1alpha1.Agent{running, tooler, stuckA}, testRoleCoordinator, crewPhaseReady, true, "3 agents, coordinator=coordinator; unschedulable: obs", metav1.ConditionTrue},
		{"all running", []kubemootv1alpha1.Agent{running, tooler}, testRoleCoordinator, crewPhaseReady, true, "Crew operational: 2 agents", metav1.ConditionTrue},
		{"coordinator alone", []kubemootv1alpha1.Agent{running}, testRoleCoordinator, crewPhaseReady, true, "1 agents", metav1.ConditionTrue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assessCrew(tc.agents, tc.coordinator)
			if got.phase != tc.phase || got.ready != tc.ready || !strings.Contains(got.message, tc.contains) {
				t.Fatalf("got %+v", got)
			}
			requireCapacity(t, got.capacity, tc.capacity)
		})
	}
}

// The CapacityAvailable condition carries the scheduler's message unchanged, with no prefix.
func TestAssessCrewCapacityMessageIsTheAgentsMessage(t *testing.T) {
	const reason = "no model in the cluster catalog (2 available) satisfies the require of phase=mulling (capability/tool-calling=true)"
	got := assessCrew([]kubemootv1alpha1.Agent{agentIn(testRoleCoordinator, agentPhaseUnschedulable, reason)}, testRoleCoordinator)
	want := "coordinator coordinator is unschedulable: " + reason
	if got.capacity == nil || got.capacity.message != want || got.capacity.reason != reasonNoModelAvailable {
		t.Fatalf("capacity %+v, want message %q", got.capacity, want)
	}
	if !got.ready || got.phase != crewPhaseWaitingForCapacity || !strings.HasSuffix(got.message, want) {
		t.Fatalf("assessment %+v", got)
	}
}

func crewStatusClient(t *testing.T, crew *kubemootv1alpha1.Crew) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := kubemootv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(crew).WithStatusSubresource(crew).Build()
}

func TestCrewStatusFollowsAssessment(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: testCrewNamespace, Generation: 3}}
	r := &CrewReconciler{Client: crewStatusClient(t, crew)}
	a := assessCrew([]kubemootv1alpha1.Agent{agentIn(testRoleCoordinator, agentPhaseUnschedulable, "no model")}, testRoleCoordinator)
	res, err := r.updateAssessedStatus(context.Background(), crew, a)
	if err != nil || res.RequeueAfter != 0 {
		t.Fatalf("a crew waiting for capacity is reconciled and needs no requeue, got %+v %v", res, err)
	}
	requireCrewCondition(t, crew, conditionTypeReady, metav1.ConditionTrue, reasonCrewReconciled)
	requireCrewCondition(t, crew, conditionTypeCapacityAvailable, metav1.ConditionFalse, reasonNoModelAvailable)
	if crew.Status.ObservedGeneration != 3 || !crew.Status.Ready || crew.Status.Phase != crewPhaseWaitingForCapacity {
		t.Fatalf("status %+v", crew.Status)
	}
}

func requireCrewCondition(t *testing.T, crew *kubemootv1alpha1.Crew, condition string, want metav1.ConditionStatus, reason string) {
	t.Helper()
	c := meta.FindStatusCondition(crew.Status.Conditions, condition)
	if c == nil || c.Status != want || c.Reason != reason || c.ObservedGeneration != crew.Generation {
		t.Fatalf("%s condition %+v, want %s (%s) at generation %d", condition, c, want, reason, crew.Generation)
	}
}

// A reconcile error stays not Ready and leaves the capacity answer as it was.
func TestCrewStatusErrorIsNotReady(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: testCrewNamespace}}
	r := &CrewReconciler{Client: crewStatusClient(t, crew)}
	res, err := r.updateStatus(context.Background(), crew, phaseError, false, "Failed to reconcile deployment: boom")
	if err != nil || res.RequeueAfter == 0 {
		t.Fatalf("an error requeues, got %+v %v", res, err)
	}
	ready := meta.FindStatusCondition(crew.Status.Conditions, conditionTypeReady)
	if ready == nil || ready.Status != metav1.ConditionFalse || ready.Reason != phaseError || crew.Status.Ready {
		t.Fatalf("Ready condition %+v", ready)
	}
	if meta.FindStatusCondition(crew.Status.Conditions, conditionTypeCapacityAvailable) != nil {
		t.Fatal("an error before the agents are assessed says nothing about capacity")
	}
}

func TestEnqueueCrewForAgent(t *testing.T) {
	h := enqueueCrewForAgent()
	labelled := agentIn(testK8sAgent, agentPhaseRunning, "")
	unlabelled := agentIn("stray", agentPhaseRunning, "")
	unlabelled.Labels = nil
	reqs := mapRequests(t, h, &labelled)
	if len(reqs) != 1 || reqs[0].Name != "x" || reqs[0].Namespace != testCrewNamespace {
		t.Fatalf("labelled agent enqueued %v", reqs)
	}
	if reqs := mapRequests(t, h, &unlabelled); len(reqs) != 0 {
		t.Fatalf("unlabelled agent enqueued %v", reqs)
	}
}
