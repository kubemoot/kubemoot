package controller

import (
	corev1 "k8s.io/api/core/v1"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

const (
	// managedNamespaceLabel on a Namespace is the consent to have the operator
	// delete that namespace when its Crew is deleted. It is set by someone with
	// rights on the Namespace object; a namespaced Crew author cannot set it, and the
	// operator never writes it.
	managedNamespaceLabel = "kubemoot.ai/managed-namespace"
	// managedNamespaceOptIn is the value of managedNamespaceLabel that grants consent.
	managedNamespaceOptIn = "true"
	// manageNamespaceRequested is the value of the Crew annotation manageNamespaceAnno.
	manageNamespaceRequested = "true"
)

// systemNamespaces are never deleted or adopted, whatever their labels say.
var systemNamespaces = map[string]bool{
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
	"default":         true,
}

// crewRequestsNamespaceManagement reports whether the Crew asked the operator to
// manage (label, and on deletion remove) its namespace.
func crewRequestsNamespaceManagement(crew *kubemootv1alpha1.Crew) bool {
	return crew.Annotations[manageNamespaceAnno] == manageNamespaceRequested
}

// isProtectedNamespace reports whether the operator must never delete or label
// the namespace: the Kubernetes system namespaces and the operator's own.
func isProtectedNamespace(name string) bool {
	return systemNamespaces[name] || name == operatorNamespace()
}

// namespaceDeletableForCrew reports whether the operator may delete ns when the
// named Crew is deleted. All must hold: the namespace is not protected, it is
// not already terminating, it carries the crew bookkeeping label for this crew,
// and the namespace itself carries the opt-in label. The operator's own crew
// label is bookkeeping only and never counts as consent.
func namespaceDeletableForCrew(ns *corev1.Namespace, crewName string) bool {
	if isProtectedNamespace(ns.Name) || !ns.DeletionTimestamp.IsZero() {
		return false
	}
	return ns.Labels[crewLabelKey] == crewName && ns.Labels[managedNamespaceLabel] == managedNamespaceOptIn
}
