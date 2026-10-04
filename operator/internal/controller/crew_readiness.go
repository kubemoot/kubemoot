package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// Crew phases derived from the agents' own phases.
const (
	crewPhasePending  = "Pending"
	crewPhaseReady    = phaseReady
	crewPhaseDegraded = "Degraded"

	agentPhaseRunning       = "Running"
	agentPhaseUnschedulable = "Unschedulable"
)

// crewAssessment is what the crew's status should say given its agents.
type crewAssessment struct {
	phase   string
	ready   bool
	message string
}

// assessCrew derives the crew's readiness from its agents. A crew is Ready only when
// its coordinator is Running and at least one specialist can be scheduled; an
// unschedulable coordinator, or specialists that are all unschedulable, make it
// Degraded with a message naming them, so a crew that cannot answer never reports
// itself operational.
func assessCrew(agents []kubemootv1alpha1.Agent, coordinatorName string) crewAssessment {
	if coordinatorName == "" {
		return crewAssessment{crewPhasePending, false, "No coordinator found"}
	}
	coordinator := findAgent(agents, coordinatorName)
	if coordinator == nil {
		return crewAssessment{crewPhasePending, false, fmt.Sprintf("coordinator %s not found", coordinatorName)}
	}
	if coordinator.Status.Phase == agentPhaseUnschedulable {
		return crewAssessment{crewPhaseDegraded, false,
			fmt.Sprintf("coordinator %s is unschedulable: %s", coordinatorName, coordinator.Status.Message)}
	}
	if coordinator.Status.Phase != agentPhaseRunning {
		return crewAssessment{crewPhasePending, false,
			fmt.Sprintf("coordinator %s is %s", coordinatorName, phaseOrPending(coordinator.Status.Phase))}
	}
	return assessSpecialists(agents, coordinatorName)
}

// assessSpecialists decides between Ready and Degraded once the coordinator runs.
func assessSpecialists(agents []kubemootv1alpha1.Agent, coordinatorName string) crewAssessment {
	specialists := len(agents) - 1
	stuck := unschedulableAgents(agents, coordinatorName)
	operational := fmt.Sprintf("Crew operational: %d agents, coordinator=%s", len(agents), coordinatorName)
	switch {
	case specialists > 0 && len(stuck) == specialists:
		return crewAssessment{crewPhaseDegraded, false,
			fmt.Sprintf("no specialist can be scheduled: %s", strings.Join(stuck, ", "))}
	case len(stuck) > 0:
		return crewAssessment{crewPhaseReady, true,
			fmt.Sprintf("%s; unschedulable: %s", operational, strings.Join(stuck, ", "))}
	default:
		return crewAssessment{crewPhaseReady, true, operational}
	}
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
		crewName := obj.GetLabels()[crewLabelKey]
		if crewName == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: crewName, Namespace: obj.GetNamespace()}}}
	})
}
