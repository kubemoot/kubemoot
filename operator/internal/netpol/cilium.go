/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CiliumPolicyGVK is the kind of the objects the operator emits for host names and the
// entities a NetworkPolicy cannot name. They are built as unstructured objects, so the
// operator carries no dependency on the Cilium Go module.
var CiliumPolicyGVK = schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"}

// Keys of the Cilium policy documents.
const (
	keyMatchLabels = "matchLabels"
	keyPort        = "port"
	keyPorts       = "ports"
	keyToPorts     = "toPorts"
)

// selectorOf is a Cilium endpoint selector over the labels.
func selectorOf(labels map[string]any) map[string]any {
	return map[string]any{keyMatchLabels: labels}
}

// toPorts is a Cilium port list with the given protocol; rules, when set, is the L7 section.
func toPorts(protocol string, ports []int32, rules map[string]any) []any {
	list := make([]any, 0, len(ports))
	for _, p := range ports {
		list = append(list, map[string]any{keyPort: fmt.Sprint(p), "protocol": protocol})
	}
	entry := map[string]any{keyPorts: list}
	if rules != nil {
		entry["rules"] = rules
	}
	return []any{entry}
}
