/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mesh

import (
	"fmt"
	"slices"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// The names the sidecar uses on the bus. The formats are the bus contract's (asyncapi.yaml);
// the operator's internal/crewscope builds the same subjects for its own use.
const (
	// AgentStateBucket is the KV bucket holding each agent's liveness heartbeat.
	AgentStateBucket = "kubemoot_agent_state"
	// LatencyBucket is the KV bucket holding each agent's turn latency samples.
	LatencyBucket = "kubemoot_latency"
	// ArtifactBucket is the Object Store bucket holding spilled contributions.
	ArtifactBucket = "kubemoot_discussion_artifacts"
	// BroadcastChannel is the channel the coordinator's steps go to.
	BroadcastChannel = "broadcast"
)

// DiscussSubject is kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>.
func (i Identity) DiscussSubject(channel, threadID string) string {
	return fmt.Sprintf("kubemoot.discuss.%s.%s.%s.%s", i.Namespace, i.Crew, channel, threadID)
}

// DiscussWildcard matches every discussion subject of the agent's crew.
func (i Identity) DiscussWildcard() string {
	return fmt.Sprintf("kubemoot.discuss.%s.%s.>", i.Namespace, i.Crew)
}

// DiscussFilters are the subjects the agent's durable consumer filters on: the broadcast
// channel and each channel the agent listens on.
// Each channel appears once: JetStream refuses a consumer whose filters overlap.
func (i Identity) DiscussFilters() []string {
	out := make([]string, 0, 1+len(i.Channels))
	for _, ch := range append([]string{BroadcastChannel}, i.Channels...) {
		if f := i.channelFilter(ch); !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

func (i Identity) channelFilter(channel string) string {
	return fmt.Sprintf("kubemoot.discuss.%s.%s.%s.>", i.Namespace, i.Crew, channel)
}

// Listens reports whether the agent listens on a channel: broadcast, or one it declares.
func (i Identity) Listens(channel string) bool {
	return channel == BroadcastChannel || slices.Contains(i.Channels, channel)
}

// LifecycleSubject is kubemoot.lifecycle.<namespace>.<crew>.<signal>.<agent>, for the waking
// and ready signals.
func (i Identity) LifecycleSubject(signal string) string {
	return fmt.Sprintf("kubemoot.lifecycle.%s.%s.%s.%s", i.Namespace, i.Crew, signal, i.Agent)
}

// ArtifactSubject is kubemoot.artifacts.<namespace>.<crew>.<threadId>.
func (i Identity) ArtifactSubject(threadID string) string {
	return fmt.Sprintf("kubemoot.artifacts.%s.%s.%s", i.Namespace, i.Crew, threadID)
}

// ArtifactKey is the Object Store key of a spilled contribution:
// <namespace>/<crew>/<threadId>/<agent>/<signal>-<id>.
func (i Identity) ArtifactKey(threadID, signal, id string) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s-%s", i.Namespace, i.Crew, threadID, i.Agent, signal, id)
}

// HeartbeatKey is the kubemoot_agent_state key <namespace>.<agent>.
func (i Identity) HeartbeatKey() string { return i.Namespace + "." + i.Agent }

// LatencyKey is the kubemoot_latency key latency.<namespace>.<agent>.<server>; the server name
// is encoded as a key token the way the agent runtime does.
func (i Identity) LatencyKey(server string) string {
	return fmt.Sprintf("latency.%s.%s.%s", i.Namespace, i.Agent, bus.KeyToken(server))
}

// ConsumerName is the name of the agent's durable JetStream consumer. A durable name cannot
// hold a dot, a wildcard, a space or a slash; the identity's tokens hold none, so joining them
// with hyphens is enough.
func (i Identity) ConsumerName() string {
	return strings.Join([]string{"sidecar", i.Namespace, i.Crew, i.Agent}, "-")
}
