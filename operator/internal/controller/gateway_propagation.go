/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// gateway_propagation.go wires MCPGateway changes to the agents in the same
// namespace. An agent's KUBEMOOT_GATEWAY_* env is built from the namespace's
// MCPGateway (agentGatewayEnvVars), so a gateway appearing, starting to
// terminate, going away, or changing its port re-renders the namespace's agents,
// including agents created before the gateway. Tools registered through
// MCPServers need no watch here: the gateway serves them and the agent-runtime
// re-reads the gateway's tool list on its own.
package controller

import (
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// gatewayWiringChanged passes creates and deletes, the start of a finalizer-held
// deletion (findCrewGateway skips a terminating gateway), and updates that change
// the port the agents' gateway endpoint is built from. The gateway controller's
// frequent status updates do not pass.
func gatewayWiringChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldGW, okOld := e.ObjectOld.(*kubemootv1alpha1.MCPGateway)
			newGW, okNew := e.ObjectNew.(*kubemootv1alpha1.MCPGateway)
			if !okOld || !okNew {
				return false
			}
			return oldGW.Spec.Port != newGW.Spec.Port ||
				oldGW.DeletionTimestamp.IsZero() != newGW.DeletionTimestamp.IsZero()
		},
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}
