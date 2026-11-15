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
// JetStream API to bind its consumer and reach the KV bucket it writes.
//
// The workspace Object Store buckets have three separate grants, so declaring an access never
// removes one an agent still uses:
//   - write: spilling tool results (publish on the buckets, create them). Held only when
//     workspaceWrite is true.
//   - read: listing a thread's objects and reading them. Held by a coordinator, whose review
//     catalog and stored-result read need it whatever the access.
//   - delete: removing a thread's objects when it closes, and the orphan reaper. Held by a
//     coordinator whatever the access.
//
// Nothing provisions a NATS user from these yet, so they are a contract for that work and are
// not enforced in 0.94; the agent-runtime enforces write by not storing (see KUBEMOOT_WORKSPACE_ACCESS).
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
	if i.Role == roleCoordinator {
		publish = appendMissing(publish, workspaceReadSubjects()...)
		publish = appendMissing(publish, workspaceDeleteSubjects()...)
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

// roleCoordinator is the discuss role of a crew's coordinator.
const roleCoordinator = "coordinator"

// workspaceBuckets are the buckets the workspace is stored in, oldest first.
var workspaceBuckets = []string{ArtifactBucket, WorkspaceBucket}

// workspaceWriteSubjects are the subjects a spill needs: the artifact reference subject, the
// bucket's chunk and meta subjects, and the call that creates a bucket nothing has yet.
func (i Identity) workspaceWriteSubjects() []string {
	out := make([]string, 0, 1+4*len(workspaceBuckets))
	out = append(out, fmt.Sprintf("kubemoot.artifacts.%s.%s.*", i.Namespace, i.Crew))
	for _, b := range workspaceBuckets {
		out = append(out,
			// Object Store chunks are named by a random id, so the bucket cannot be narrowed per agent.
			fmt.Sprintf("$O.%s.C.*", b),
			fmt.Sprintf("$O.%s.M.*", b),
			fmt.Sprintf("$JS.API.STREAM.INFO.OBJ_%s", b),
			fmt.Sprintf("$JS.API.STREAM.CREATE.OBJ_%s", b))
	}
	return out
}

// workspaceReadSubjects are the subjects listing and reading a bucket's objects need: the stream
// info and a consumer over its chunks.
func workspaceReadSubjects() []string {
	out := make([]string, 0, 5*len(workspaceBuckets))
	for _, b := range workspaceBuckets {
		out = append(out,
			fmt.Sprintf("$JS.API.STREAM.INFO.OBJ_%s", b),
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.OBJ_%s", b),
			fmt.Sprintf("$JS.API.CONSUMER.CREATE.OBJ_%s.>", b),
			fmt.Sprintf("$JS.API.DIRECT.GET.OBJ_%s", b),
			fmt.Sprintf("$JS.API.DIRECT.GET.OBJ_%s.>", b))
	}
	return out
}

// workspaceDeleteSubjects are the subjects deleting an object needs: the meta record that marks
// it deleted and the purge of its chunks.
func workspaceDeleteSubjects() []string {
	out := make([]string, 0, 2*len(workspaceBuckets))
	for _, b := range workspaceBuckets {
		out = append(out,
			fmt.Sprintf("$O.%s.M.*", b),
			fmt.Sprintf("$JS.API.STREAM.PURGE.OBJ_%s", b))
	}
	return out
}

// appendMissing appends each subject that dst does not already hold.
func appendMissing(dst []string, subjects ...string) []string {
	for _, s := range subjects {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
}
