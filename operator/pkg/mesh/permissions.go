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

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// Permissions are the NATS subjects one agent's sidecar may publish to and subscribe to. A
// per-agent NATS user with exactly these permissions holds its own signals, its own heartbeat
// and its crew's discussion, and nothing else: it cannot publish another agent's lifecycle
// signal, write another agent's heartbeat, or read another crew's threads.
type Permissions struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// Permissions returns the subjects the sidecar of an agent with no declared workspace access
// needs: the workspace write grant is included, as it was before access could be declared.
func (i Identity) Permissions() Permissions { return i.PermissionsFor(true) }

// PermissionsFor returns the subjects the sidecar of this agent needs. The set follows what the
// sidecar does: it signals on its crew's discussion subjects, announces itself on the
// lifecycle subjects, writes its heartbeat key, asks the operator to wake a model, and uses the
// JetStream API to bind its consumer and reach the KV bucket it writes. When workspaceWrite is
// true it also spills tool results to the workspace Object Store bucket; an agent whose declared
// workspace access does not include write holds none of those subjects.
func (i Identity) PermissionsFor(workspaceWrite bool) Permissions {
	publish := []string{
		i.DiscussWildcard(),
		fmt.Sprintf("kubemoot.lifecycle.%s.%s.*.%s", i.Namespace, i.Crew, i.Agent),
		bus.ModelLifecycleSubjectPrefix + ">",
		fmt.Sprintf("$KV.%s.%s", AgentStateBucket, i.HeartbeatKey()),
		// Acknowledgements, and the API calls that bind the consumer and the bucket.
		fmt.Sprintf("$JS.ACK.%s.%s.>", bus.DiscussStream, i.ConsumerName()),
		fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", bus.DiscussStream, i.ConsumerName()),
		fmt.Sprintf("$JS.API.STREAM.INFO.KV_%s", AgentStateBucket),
	}
	if workspaceWrite {
		publish = append(publish, i.workspaceWriteSubjects()...)
	}
	return Permissions{
		Publish: publish,
		Subscribe: []string{
			// Replies, and the push consumer's delivery subject, which is an inbox.
			"_INBOX.>",
			i.DiscussWildcard(),
		},
	}
}

// workspaceWriteSubjects are the subjects a spill to the workspace needs.
func (i Identity) workspaceWriteSubjects() []string {
	return []string{
		fmt.Sprintf("kubemoot.artifacts.%s.%s.*", i.Namespace, i.Crew),
		// Object Store chunks are named by a random id, so the bucket cannot be narrowed per agent.
		fmt.Sprintf("$O.%s.>", ArtifactBucket),
		fmt.Sprintf("$JS.API.STREAM.INFO.OBJ_%s", ArtifactBucket),
		// The first spill creates the bucket when nothing has yet.
		fmt.Sprintf("$JS.API.STREAM.CREATE.OBJ_%s", ArtifactBucket),
	}
}
