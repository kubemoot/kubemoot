package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Crew phases derived from the agents' own phases.
const (
	crewPhasePending = "Pending"
	// crewPhaseReady is the Crew status protocol value, kept independent of phaseReady.
	crewPhaseReady = "Ready"
	// crewPhaseWaitingForCapacity is a reconciled crew that no model in the catalog can serve
	// now. kstatus, and so Helm and Flux waits, read only the Ready condition, which stays True.
	crewPhaseWaitingForCapacity = "WaitingForCapacity"

	agentPhaseRunning       = "Running"
	agentPhaseUnschedulable = "Unschedulable"

	reasonCrewReconciled        = "Reconciled"
	reasonModelAvailable        = "ModelAvailable"
	reasonNoModelAvailable      = "NoModelAvailable"
	reasonCoordinatorNotRunning = "CoordinatorNotRunning"
)

// crewAssessment is what the crew's status should say given its agents.
type crewAssessment struct {
	phase string
	// ready is the Ready condition: the crew is reconciled as declared. A crew that waits
	// for capacity is ready; a missing or not yet running coordinator is not.
	ready bool
	// message is the status message and the Ready condition's message.
	message string
	// capacity is the CapacityAvailable condition; nil leaves the condition as it is.
	capacity *crewCapacity
}

// crewCapacity is the CapacityAvailable condition: whether a model in the cluster catalog
// can run the crew's agents now.
type crewCapacity struct {
	status  metav1.ConditionStatus
	reason  string
	message string
}

// assessCrew derives the crew's readiness and capacity from its agents. Ready means the
// crew is reconciled as declared: its coordinator exists and is running. Whether a model can
// run the agents is the separate CapacityAvailable condition. An unschedulable coordinator,
// or specialists that are all unschedulable, leave the crew Ready with phase
// WaitingForCapacity and CapacityAvailable=False naming them, so a crew that cannot answer
// never reports the phase Ready, while a GitOps upgrade that waits for Ready still succeeds
// when accelerators are away.
func assessCrew(agents []kubemootv1alpha1.Agent, coordinatorName string) crewAssessment {
	if coordinatorName == "" {
		return coordinatorNotRunning("No coordinator found")
	}
	coordinator := findAgent(agents, coordinatorName)
	if coordinator == nil {
		return coordinatorNotRunning(fmt.Sprintf("coordinator %s not found", coordinatorName))
	}
	switch coordinator.Status.Phase {
	case agentPhaseUnschedulable:
		return waitingForCapacity(fmt.Sprintf("coordinator %s is unschedulable: %s", coordinatorName, coordinator.Status.Message))
	case agentPhaseRunning:
		return assessSpecialists(agents, coordinatorName)
	}
	return coordinatorNotRunning(fmt.Sprintf("coordinator %s is %s", coordinatorName, phaseOrPending(coordinator.Status.Phase)))
}

// coordinatorNotRunning is the assessment of a crew with no running coordinator: not Ready,
// and nothing to say yet about capacity, so a capacity answer from before is cleared.
func coordinatorNotRunning(message string) crewAssessment {
	return crewAssessment{phase: crewPhasePending, message: message,
		capacity: &crewCapacity{metav1.ConditionUnknown, reasonCoordinatorNotRunning, message}}
}

// waitingForCapacity is the assessment of a reconciled crew with no model to run it.
func waitingForCapacity(message string) crewAssessment {
	return crewAssessment{phase: crewPhaseWaitingForCapacity, ready: true,
		message:  "Crew reconciled, waiting for capacity: " + message,
		capacity: &crewCapacity{metav1.ConditionFalse, reasonNoModelAvailable, message}}
}

// assessSpecialists decides between Ready and WaitingForCapacity once the coordinator runs.
func assessSpecialists(agents []kubemootv1alpha1.Agent, coordinatorName string) crewAssessment {
	specialists := len(agents) - 1
	stuck := unschedulableAgents(agents, coordinatorName)
	if specialists > 0 && len(stuck) == specialists {
		return waitingForCapacity(fmt.Sprintf("no specialist can be scheduled: %s", strings.Join(stuck, ", ")))
	}
	message := fmt.Sprintf("Crew operational: %d agents, coordinator=%s", len(agents), coordinatorName)
	capacity := fmt.Sprintf("a model in the cluster catalog can run coordinator %s", coordinatorName)
	if len(stuck) > 0 {
		message += fmt.Sprintf("; unschedulable: %s", strings.Join(stuck, ", "))
		capacity += fmt.Sprintf("; no model for: %s", strings.Join(stuck, ", "))
	}
	return crewAssessment{phase: crewPhaseReady, ready: true, message: message,
		capacity: &crewCapacity{metav1.ConditionTrue, reasonModelAvailable, capacity}}
}

func findAgent(agents []kubemootv1alpha1.Agent, name string) *kubemootv1alpha1.Agent {
	for i := range agents {
		if agents[i].Name == name {
			return &agents[i]
		}
	}
	return nil
}

// unschedulableAgents names the agents, other than the coordinator, that cannot be
// scheduled, sorted so the message is stable.
func unschedulableAgents(agents []kubemootv1alpha1.Agent, coordinatorName string) []string {
	var names []string
	for i := range agents {
		a := &agents[i]
		if a.Name != coordinatorName && a.Status.Phase == agentPhaseUnschedulable {
			names = append(names, a.Name)
		}
	}
	sort.Strings(names)
	return names
}

func phaseOrPending(phase string) string {
	if phase == "" {
		return "pending"
	}
	return strings.ToLower(phase)
}

// enqueueCrewForAgent maps an Agent event to its crew, named by the crew label, so a
// crew's readiness follows its agents' phases without waiting for a requeue.
func enqueueCrewForAgent() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		crewName := obj.GetLabels()[labelCrew]
		if crewName == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: crewName, Namespace: obj.GetNamespace()}}}
	})
}
