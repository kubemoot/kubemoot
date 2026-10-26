/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

// defaultServiceAccount is the account a pod runs as when its spec names none.
const defaultServiceAccount = "default"

// automountTokenFor decides whether a pod the operator builds for an Agent or an
// MCPServer gets a ServiceAccount token. A pod that names no ServiceAccount runs as the
// namespace default account and has no business with the Kubernetes API, so it gets no
// token: a RoleBinding someone later adds to the default account cannot arm it. A pod
// that names a ServiceAccount (the kubernetes MCP server, for one) keeps the Kubernetes
// default, because that account exists to call the API and its RBAC bounds the pod.
// Naming the default account explicitly is the same as naming none: it is the account the
// pod runs as anyway, and the use-grant policy treats it as no reference.
// The field is returned as a pointer: nil leaves the Kubernetes default in place.
func automountTokenFor(serviceAccountName string) *bool {
	if serviceAccountName != "" && serviceAccountName != defaultServiceAccount {
		return nil
	}
	return boolPtr(false)
}
