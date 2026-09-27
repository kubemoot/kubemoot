/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package crewscope builds and parses every name the operator gives a crew
// outside Kubernetes: NATS subjects, KV keys, vector collections, cluster-scoped
// object names, and shared-gateway routes. Each carries the namespace first,
// then the crew, so the same crew name runs in many namespaces with no crosstalk.
// A namespace is a DNS label (no dots), so it is always exactly one NATS token.
//
// IMPORTANT: the subject formats and token validation are shared with
// kubemoot/discussion-gateway/internal/crewscope, kubemoot/scheduling-mcp/internal/scope
// and kubemoot/artifact-access/internal/scope (per-module builds cannot share
// source). If you change one, change the others.
package crewscope

import (
	"fmt"
	"strings"
)

// NamespaceEnv is the environment variable the operator sets from the downward
// API on every workload that builds these names.
const NamespaceEnv = "KUBEMOOT_NAMESPACE"

const (
	subjectDiscuss = "kubemoot.discuss"
	discussTokens  = 6 // kubemoot.discuss.<ns>.<crew>.<channel>.<thread>
)

// Scope is one crew in one namespace.
type Scope struct {
	Namespace string
	Crew      string
}

// New validates both names as single NATS tokens and returns the scope.
func New(namespace, crew string) (Scope, error) {
	if err := validToken("namespace", namespace); err != nil {
		return Scope{}, err
	}
	if err := validToken("crew", crew); err != nil {
		return Scope{}, err
	}
	return Scope{Namespace: namespace, Crew: crew}, nil
}

// DiscussSubject is kubemoot.discuss.<ns>.<crew>.<channel>.<thread>.
func (s Scope) DiscussSubject(channel, thread string) string {
	return fmt.Sprintf("%s.%s.%s.%s.%s", subjectDiscuss, s.Namespace, s.Crew, channel, thread)
}

// ResumeKey is the kubemoot_crew_resumes key <ns>.<crew>.
func (s Scope) ResumeKey() string {
	return s.Namespace + "." + s.Crew
}

// ResumeCollection is the resume vector collection crew_<ns>_<crew>_resumes,
// hyphens turned to underscores.
func (s Scope) ResumeCollection() string {
	return underscore(fmt.Sprintf("crew_%s_%s_resumes", s.Namespace, s.Crew))
}

// MemoryPrefix is the kubemoot_crew_memory key prefix <ns>.<crew>. that holds
// every working-memory fact of the crew.
func (s Scope) MemoryPrefix() string {
	return s.Namespace + "." + s.Crew + "."
}

// ClusterRBACName names the crew's cluster-scoped discussion gateway RBAC
// objects: crew-<ns>-<crew>-discussion.
func (s Scope) ClusterRBACName() string {
	return fmt.Sprintf("crew-%s-%s-discussion", s.Namespace, s.Crew)
}

// RoutePathPrefix is the crew's path on the shared Gateway:
// /api/v1/namespaces/<ns>/discussions/<crew>.
func (s Scope) RoutePathPrefix() string {
	return fmt.Sprintf("/api/v1/namespaces/%s/discussions/%s", s.Namespace, s.Crew)
}

// GatewayAPIPath is the discussion gateway's own API path for the crew,
// /api/v1/discussions/<crew>, which the shared route rewrites to.
func (s Scope) GatewayAPIPath() string {
	return "/api/v1/discussions/" + s.Crew
}

// LegacyResumeKey is the unscoped kubemoot_crew_resumes key <crew> that the
// namespaced key replaces.
func LegacyResumeKey(crew string) string {
	return crew
}

// LegacyClusterRBACName is the unscoped cluster RBAC name crew-<crew>-discussion
// that the namespaced name replaces.
func LegacyClusterRBACName(crew string) string {
	return fmt.Sprintf("crew-%s-discussion", crew)
}

// Discussion is a parsed discussion subject.
type Discussion struct {
	Scope
	Channel string
	Thread  string
}

// ParseDiscussSubject parses kubemoot.discuss.<ns>.<crew>.<channel>.<thread>.
// A thread id may itself contain dots; everything after the channel is the thread.
func ParseDiscussSubject(subject string) (Discussion, error) {
	prefix := subjectDiscuss + "."
	if !strings.HasPrefix(subject, prefix) {
		return Discussion{}, fmt.Errorf("subject %q is not a discussion subject", subject)
	}
	parts := strings.SplitN(strings.TrimPrefix(subject, prefix), ".", discussTokens-2)
	if len(parts) < discussTokens-2 {
		return Discussion{}, fmt.Errorf("subject %q lacks namespace, crew, channel, or thread", subject)
	}
	scope, err := New(parts[0], parts[1])
	if err != nil {
		return Discussion{}, fmt.Errorf("subject %q: %w", subject, err)
	}
	if parts[2] == "" || parts[3] == "" {
		return Discussion{}, fmt.Errorf("subject %q has an empty channel or thread", subject)
	}
	return Discussion{Scope: scope, Channel: parts[2], Thread: parts[3]}, nil
}

// MemoryKey is a parsed kubemoot_crew_memory key.
type MemoryKey struct {
	Scope
	// Rest is <topic>.<key>.
	Rest string
}

// Key renders <ns>.<crew>.<topic>.<key>.
func (k MemoryKey) Key() string {
	return k.MemoryPrefix() + k.Rest
}

// SplitLegacyMemoryKey splits an unscoped <crew>.<topic>.<key> into the crew
// name and <topic>.<key>. It reports false when the key has no dot.
func SplitLegacyMemoryKey(key string) (crew, rest string, ok bool) {
	crew, rest, ok = strings.Cut(key, ".")
	if !ok || crew == "" || rest == "" {
		return "", "", false
	}
	return crew, rest, true
}

// ParseMemoryKey parses <ns>.<crew>.<topic>.<key>. It cannot by itself tell a
// scoped key from an unscoped one with a dotted key; callers that care check
// the namespace and crew against the cluster.
func ParseMemoryKey(key string) (MemoryKey, error) {
	parts := strings.SplitN(key, ".", 3)
	if len(parts) < 3 || parts[2] == "" {
		return MemoryKey{}, fmt.Errorf("memory key %q lacks namespace, crew, or topic", key)
	}
	scope, err := New(parts[0], parts[1])
	if err != nil {
		return MemoryKey{}, fmt.Errorf("memory key %q: %w", key, err)
	}
	return MemoryKey{Scope: scope, Rest: parts[2]}, nil
}

// underscore turns hyphens into underscores for vector-store identifiers.
func underscore(s string) string {
	return strings.ReplaceAll(s, "-", "_")
}

// validToken rejects a value that is empty or would not be exactly one NATS token.
func validToken(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s is required", what)
	}
	if strings.ContainsAny(v, ".*> \t\r\n") {
		return fmt.Errorf("%s %q is not a single NATS subject token", what, v)
	}
	return nil
}
