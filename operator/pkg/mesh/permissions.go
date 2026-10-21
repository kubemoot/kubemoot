/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package mesh

import "fmt"

// Permissions are the NATS subjects one agent's sidecar may publish to and subscribe to. A
// per-agent NATS user with exactly these permissions holds its own signals, its own heartbeat
// and its crew's discussion, and nothing else: it cannot publish another agent's lifecycle
// signal, write another agent's heartbeat, or read another crew's threads.
type Permissions struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
}

// Permissions returns the subjects the sidecar of this agent needs. The set follows what the
// sidecar does: it signals on its crew's discussion subjects, announces itself on the
// lifecycle subjects, spills artifacts, writes its heartbeat and latency keys, asks the
// operator to wake a model, and uses the JetStream API to bind its consumer, acknowledge
// messages and reach the KV and Object Store buckets it writes.
func (i Identity) Permissions() Permissions {
	return Permissions{
		Publish: []string{
			i.DiscussWildcard(),
			fmt.Sprintf("kubemoot.lifecycle.%s.%s.*.%s", i.Namespace, i.Crew, i.Agent),
			fmt.Sprintf("kubemoot.artifacts.%s.%s.*", i.Namespace, i.Crew),
			"kubemoot.server.model.>",
			fmt.Sprintf("$KV.%s.%s", AgentStateBucket, i.HeartbeatKey()),
			fmt.Sprintf("$KV.%s.latency.%s.%s.>", LatencyBucket, i.Namespace, i.Agent),
			fmt.Sprintf("$O.%s.>", ArtifactBucket),
			// Acknowledgements, and the API calls that bind the consumer and the buckets.
			"$JS.ACK.>",
			fmt.Sprintf("$JS.API.CONSUMER.INFO.%s.%s", DiscussStream, ConsumerName(i)),
			fmt.Sprintf("$JS.API.STREAM.INFO.KV_%s", AgentStateBucket),
			fmt.Sprintf("$JS.API.STREAM.INFO.KV_%s", LatencyBucket),
			fmt.Sprintf("$JS.API.STREAM.INFO.OBJ_%s", ArtifactBucket),
		},
		Subscribe: []string{
			// Replies, and the push consumer's delivery subject, which is an inbox.
			"_INBOX.>",
			i.DiscussWildcard(),
		},
	}
}
