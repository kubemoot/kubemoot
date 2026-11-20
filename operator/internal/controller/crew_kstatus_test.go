package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cli-utils/pkg/kstatus/status"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Helm's and Flux's waits judge a custom resource with kstatus, which reads the object's
// generation, its Reconciling and Stalled conditions and its Ready condition, and never
// status.phase. These tests run the same Compute on the status the operator writes.

const kstatusGeneration = 4

// crewAfterReconcile is the Crew as the API holds it once the reconciler wrote the status
// for the given agents.
func crewAfterReconcile(t *testing.T, agents []kubemootv1alpha1.Agent, coordinator string) *unstructured.Unstructured {
	t.Helper()
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: testCrewNamespace, Generation: kstatusGeneration}}
	r := &CrewReconciler{Client: crewStatusClient(t, crew)}
	if _, err := r.updateAssessedStatus(t.Context(), crew, assessCrew(agents, coordinator)); err != nil {
		t.Fatal(err)
	}
	return toUnstructured(t, crew, "Crew")
}

func toUnstructured(t *testing.T, obj runtime.Object, kind string) *unstructured.Unstructured {
	t.Helper()
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: content}
	u.SetAPIVersion(kubemootv1alpha1.GroupVersion.String())
	u.SetKind(kind)
	return u
}

func computeStatus(t *testing.T, u *unstructured.Unstructured) status.Status {
	t.Helper()
	res, err := status.Compute(u)
	if err != nil {
		t.Fatal(err)
	}
	return res.Status
}

func TestCrewKstatus(t *testing.T) {
	coordinator := agentIn(testRoleCoordinator, agentPhaseRunning, "")
	tooler := agentIn(testK8sAgent, agentPhaseRunning, "")
	stuckCoordinator := agentIn(testRoleCoordinator, agentPhaseUnschedulable, "no model")
	stuckTooler := agentIn(testK8sAgent, agentPhaseUnschedulable, "no model")
	pendingCoordinator := agentIn(testRoleCoordinator, "", "")

	cases := []struct {
		name        string
		agents      []kubemootv1alpha1.Agent
		coordinator string
		want        status.Status
	}{
		{"capacity present", []kubemootv1alpha1.Agent{coordinator, tooler}, testRoleCoordinator, status.CurrentStatus},
		{"coordinator without a model, capacity absent", []kubemootv1alpha1.Agent{stuckCoordinator, tooler}, testRoleCoordinator, status.CurrentStatus},
		{"every specialist without a model, capacity absent", []kubemootv1alpha1.Agent{coordinator, stuckTooler}, testRoleCoordinator, status.CurrentStatus},
		{"coordinator not running yet is still in progress", []kubemootv1alpha1.Agent{pendingCoordinator, tooler}, testRoleCoordinator, status.InProgressStatus},
		{"no coordinator is not ready", []kubemootv1alpha1.Agent{tooler}, "", status.InProgressStatus},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computeStatus(t, crewAfterReconcile(t, tc.agents, tc.coordinator)); got != tc.want {
				t.Fatalf("kstatus %s, want %s", got, tc.want)
			}
		})
	}
}

// A Crew whose reconcile failed is not Ready, so a wait does not succeed on it.
func TestCrewKstatusReconcileErrorIsInProgress(t *testing.T) {
	crew := &kubemootv1alpha1.Crew{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: testCrewNamespace, Generation: kstatusGeneration}}
	r := &CrewReconciler{Client: crewStatusClient(t, crew)}
	if _, err := r.updateStatus(t.Context(), crew, phaseError, false, "Failed to reconcile deployment: boom"); err != nil {
		t.Fatal(err)
	}
	if got := computeStatus(t, toUnstructured(t, crew, "Crew")); got != status.InProgressStatus {
		t.Fatalf("kstatus %s, want %s", got, status.InProgressStatus)
	}
}

// A spec change the reconciler has not yet written status for is in progress, so a wait on a
// capacity-waiting crew still waits for the new spec to be reconciled.
func TestCrewKstatusUnobservedGenerationIsInProgress(t *testing.T) {
	u := crewAfterReconcile(t, []kubemootv1alpha1.Agent{agentIn(testRoleCoordinator, agentPhaseUnschedulable, "no model")}, testRoleCoordinator)
	u.SetGeneration(kstatusGeneration + 1)
	if got := computeStatus(t, u); got != status.InProgressStatus {
		t.Fatalf("kstatus %s, want %s", got, status.InProgressStatus)
	}
}

// An Agent that no model can run carries no Ready condition, so kstatus reads it as Current
// and a wait on it does not block. The condition check guards that: adding a Ready condition
// to an Agent would make capacity block a Helm wait again.
func TestAgentKstatusUnschedulableDoesNotBlock(t *testing.T) {
	agent := agentIn(testRoleCoordinator, agentPhaseUnschedulable, "no model in the cluster catalog")
	agent.Generation = kstatusGeneration
	u := toUnstructured(t, &agent, "Agent")
	if got := computeStatus(t, u); got != status.CurrentStatus {
		t.Fatalf("kstatus %s, want %s", got, status.CurrentStatus)
	}
	if meta.FindStatusCondition(agent.Status.Conditions, conditionTypeReady) != nil {
		t.Fatal("an Agent must not carry a Ready condition while capacity is absent")
	}
}
