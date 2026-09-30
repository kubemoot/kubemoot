/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package notifications implements the NotificationSink dispatcher: a
// leader-elected runnable that subscribes to discussion messages, filters
// for the configured signal types (first release: `concern` only), and
// POSTs a self-contained payload to the sink's webhook.
//
// The payload carries enough information that the notification stands on
// its own even if the discussion thread is later deleted — content, agent,
// namespace, crew, channel, threadId, timestamp, and an optional dashboard deep-link.
package notifications

import (
	"encoding/json"
	"fmt"

	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

// PayloadSchemaVersion is bumped only on incompatible payload changes.
const PayloadSchemaVersion = 1

// ConcernMessageType is the discussion messageType this package dispatches
// on. Other signal types are ignored in the first release.
const ConcernMessageType = "concern"

// Payload is the JSON body sent to the webhook. Marshaled with
// json.Marshal — `omitempty` keeps the body tight when fields are unset.
type Payload struct {
	SchemaVersion int    `json:"schemaVersion"`
	Agent         string `json:"agent"`
	Namespace     string `json:"namespace"`
	Crew          string `json:"crew"`
	Channel       string `json:"channel,omitempty"`
	ThreadID      string `json:"threadId,omitempty"`
	Concern       string `json:"concern"`
	Timestamp     string `json:"timestamp,omitempty"`
	DashboardURL  string `json:"dashboardUrl,omitempty"`
}

// DiscussionMessage is the subset of a NATS discussion envelope this
// package consumes. The dashboard/coordinator publish a richer shape;
// the dispatcher only reads what it dispatches on.
type DiscussionMessage struct {
	MessageType string `json:"messageType"`
	AgentName   string `json:"agentName"`
	Content     string `json:"content"`
	ThreadID    string `json:"threadId"`
	Channel     string `json:"channel,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}

// BuildPayload constructs the Payload for a concern dispatch. dashboardBase
// is optional ("" to omit the deepLink). When provided, the link points at
// the dashboard's threads view with a query param the dashboard parses.
func BuildPayload(msg *DiscussionMessage, scope crewscope.Scope, dashboardBase string) Payload {
	p := Payload{
		SchemaVersion: PayloadSchemaVersion,
		Agent:         msg.AgentName,
		Namespace:     scope.Namespace,
		Crew:          scope.Crew,
		Channel:       msg.Channel,
		ThreadID:      msg.ThreadID,
		Concern:       msg.Content,
		Timestamp:     msg.Timestamp,
	}
	if dashboardBase != "" && msg.ThreadID != "" {
		p.DashboardURL = fmt.Sprintf("%s/discussions?threadId=%s", dashboardBase, msg.ThreadID)
	}
	return p
}

// MarshalPayload returns the JSON bytes for the HTTP body. Errors only on
// programming bugs (unreachable for the Payload type as defined).
func MarshalPayload(p *Payload) ([]byte, error) {
	return json.Marshal(p)
}
