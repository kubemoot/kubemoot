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
	"maps"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// ServiceDests turns a Service into the destinations a NetworkPolicy can express: the
// Service's namespace, the pod selector it routes to, and the port its pods listen on
// (the target port, since a CNI applies policy after the Service address is translated).
// ports lists the Service ports wanted; empty means every TCP port. A target port given
// by name is read from the Service's EndpointSlices. It fails when the Service has no pod
// selector, exposes none of the wanted ports, or has a named target port no endpoint
// resolves yet.
func ServiceDests(svc *corev1.Service, slices []discoveryv1.EndpointSlice, ports []int32) ([]Dest, error) {
	if len(svc.Spec.Selector) == 0 {
		return nil, fmt.Errorf("service %s/%s has no pod selector; declare its addresses with allowedCIDRs", svc.Namespace, svc.Name)
	}
	chosen, err := chooseServicePorts(svc, ports)
	if err != nil {
		return nil, err
	}
	targets := map[int32]bool{}
	for _, sp := range chosen {
		found := targetPorts(sp, slices)
		if len(found) == 0 {
			return nil, fmt.Errorf("service %s/%s port %d targets the named port %q, which no endpoint resolves yet",
				svc.Namespace, svc.Name, sp.Port, sp.TargetPort.StrVal)
		}
		for _, target := range found {
			targets[target] = true
		}
	}
	out := make([]Dest, 0, len(targets))
	for _, p := range portList(targets) {
		out = append(out, Dest{Namespace: svc.Namespace, PodLabels: maps.Clone(svc.Spec.Selector), Port: p})
	}
	return out, nil
}

// chooseServicePorts picks the TCP ports of the Service the caller wants.
func chooseServicePorts(svc *corev1.Service, wanted []int32) ([]corev1.ServicePort, error) {
	want := map[int32]bool{}
	for _, p := range wanted {
		want[p] = true
	}
	every := len(wanted) == 0
	var chosen []corev1.ServicePort
	for _, sp := range svc.Spec.Ports {
		if sp.Protocol != "" && sp.Protocol != corev1.ProtocolTCP {
			continue
		}
		if every || want[sp.Port] {
			chosen = append(chosen, sp)
			delete(want, sp.Port)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("service %s/%s does not expose TCP port(s) %v", svc.Namespace, svc.Name, portList(want))
	}
	if len(chosen) == 0 {
		return nil, fmt.Errorf("service %s/%s exposes no TCP port", svc.Namespace, svc.Name)
	}
	return chosen, nil
}

// targetPorts are the ports the Service's pods listen on for one Service port. A named
// target port can differ between pods (during a rolling change), so every number any
// endpoint reports for the name is returned.
func targetPorts(sp corev1.ServicePort, slices []discoveryv1.EndpointSlice) []int32 {
	if sp.TargetPort.Type == intstr.String {
		return namedTargetPorts(sp, slices)
	}
	if sp.TargetPort.IntVal > 0 {
		return []int32{sp.TargetPort.IntVal}
	}
	return []int32{sp.Port}
}

// namedTargetPorts reads the pod ports behind a named target port: an EndpointSlice port
// carries the Service port's name and the number the pods listen on.
func namedTargetPorts(sp corev1.ServicePort, slices []discoveryv1.EndpointSlice) []int32 {
	set := map[int32]bool{}
	for i := range slices {
		for _, p := range slices[i].Ports {
			if p.Port != nil && p.Name != nil && *p.Name == sp.Name {
				set[*p.Port] = true
			}
		}
	}
	return portList(set)
}
