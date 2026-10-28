/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package netpol

import (
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Dest is one destination a workload is always allowed to reach, derived from a URL the
// operator already hands the workload (the bus, a model server, the placement service).
// Exactly one of Namespace, SameNamespace, Host, and CIDR is set.
type Dest struct {
	// Namespace is the namespace of a cluster Service.
	Namespace string
	// SameNamespace selects pods of the workload's own namespace by PodLabels.
	SameNamespace bool
	// PodLabels narrow a Namespace or SameNamespace destination to pods with these labels.
	PodLabels map[string]string
	// Host is a DNS name outside the cluster.
	Host string
	// CIDR is an address outside the cluster, as a /32 or /128.
	CIDR string
	// Port is the TCP port.
	Port int32
}

// schemePorts are the ports a URL without one uses.
var schemePorts = map[string]int32{
	"http": 80, "ws": 80, "https": 443, "wss": 443, "nats": 4222, "tls": 4222,
}

// DestsFromURL derives the destinations of a URL, or of a comma separated list of URLs.
// A host of the form name.namespace, name.namespace.svc, or name.namespace.svc.<domain>
// is a cluster Service, and a host of one label is a Service in the workload's own
// namespace. Every other name is outside the cluster. A URL that does not parse, names no host, or has neither a port
// nor a scheme with a default port yields nothing.
func DestsFromURL(raw string) []Dest {
	items := splitList(raw)
	out := make([]Dest, 0, len(items))
	for _, item := range items {
		out = append(out, destsFromOne(item)...)
	}
	return out
}

func destsFromOne(raw string) []Dest {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	port, ok := urlPort(u)
	if !ok {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		return []Dest{{CIDR: AddrCIDR(host), Port: port}}
	}
	labels := strings.Split(host, ".")
	switch {
	case len(labels) == 1:
		return []Dest{{SameNamespace: true, Port: port}}
	case len(labels) == 2 || labels[2] == "svc":
		return []Dest{{Namespace: labels[1], Port: port}}
	}
	return []Dest{{Host: host, Port: port}}
}

// ServiceRef is a cluster Service named by a URL host.
type ServiceRef struct {
	Namespace string
	Name      string
	Port      int32
}

// ServiceFromURL reads the cluster Service a URL names: a host of the form
// name.namespace, name.namespace.svc, or name.namespace.svc.<domain>, or a single label,
// which is a Service in defaultNamespace. It reports false for a URL that names an
// address or a host outside the cluster, or that has no usable port.
func ServiceFromURL(raw, defaultNamespace string) (ServiceRef, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil {
		return ServiceRef{}, false
	}
	port, ok := urlPort(u)
	if !ok {
		return ServiceRef{}, false
	}
	labels := strings.Split(strings.ToLower(u.Hostname()), ".")
	switch {
	case len(labels) == 1:
		return ServiceRef{Namespace: defaultNamespace, Name: labels[0], Port: port}, true
	case len(labels) == 2 || labels[2] == "svc":
		return ServiceRef{Namespace: labels[1], Name: labels[0], Port: port}, true
	}
	return ServiceRef{}, false
}

func urlPort(u *url.URL) (int32, bool) {
	if p := u.Port(); p != "" {
		n, err := strconv.ParseInt(p, 10, 32)
		return int32(n), err == nil && n > 0 && n <= 65535
	}
	p, ok := schemePorts[u.Scheme]
	return p, ok
}

// AddrCIDR writes an address as a /32, or a /128 for IPv6. An unparsable address comes
// back with a /32, which the API server then rejects.
func AddrCIDR(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return addr + "/32"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String() + "/32"
	}
	return ip.String() + "/128"
}
