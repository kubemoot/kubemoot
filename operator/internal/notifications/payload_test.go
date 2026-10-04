/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package notifications

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

func TestBuildPayload_PopulatesCoreFields(t *testing.T) {
	msg := &DiscussionMessage{
		MessageType: ConcernMessageType,
		AgentName:   testAgentK8sStorage,
		Content:     "Partition /var on rig0 is 96% full",
		ThreadID:    "abc-123",
		Channel:     testChannelGeneral,
		Timestamp:   "2026-05-17T04:00:00Z",
	}
	p := BuildPayload(msg, crewscope.Scope{Namespace: "team-a", Crew: "homelab-pilot"}, "https://dashboard.example.com")
	if p.SchemaVersion != PayloadSchemaVersion {
		t.Errorf("schemaVersion: got %d, want %d", p.SchemaVersion, PayloadSchemaVersion)
	}
	if p.Agent != testAgentK8sStorage {
		t.Errorf("agent: got %q", p.Agent)
	}
	if p.Crew != "homelab-pilot" {
		t.Errorf("crew: got %q", p.Crew)
	}
	if p.Namespace != "team-a" {
		t.Errorf("namespace: got %q", p.Namespace)
	}
	if p.Concern != msg.Content {
		t.Errorf("concern not propagated; got %q", p.Concern)
	}
	if p.ThreadID != "abc-123" {
		t.Errorf("threadId: got %q", p.ThreadID)
	}
	if !strings.Contains(p.DashboardURL, "threadId=abc-123") {
		t.Errorf("dashboardUrl should contain threadId; got %q", p.DashboardURL)
	}
}

func TestBuildPayload_OmitsDashboardURLWhenBaseEmpty(t *testing.T) {
	msg := &DiscussionMessage{AgentName: "x", Content: "y", ThreadID: "t1"}
	p := BuildPayload(msg, crewscope.Scope{Namespace: "ns", Crew: "c"}, "")
	if p.DashboardURL != "" {
		t.Errorf("dashboardUrl should be empty; got %q", p.DashboardURL)
	}
}

func TestBuildPayload_OmitsDashboardURLWhenThreadMissing(t *testing.T) {
	msg := &DiscussionMessage{AgentName: "x", Content: "y"} // no ThreadID
	p := BuildPayload(msg, crewscope.Scope{Namespace: "ns", Crew: "c"}, "https://dash.example.com")
	if p.DashboardURL != "" {
		t.Errorf("dashboardUrl should be empty without threadId; got %q", p.DashboardURL)
	}
}

// TestMarshalPayload_OmitemptyTrimsTheBody pins the self-contained-but-tight
// invariant: the notification body never carries empty-string keys for
// optional fields. Receivers like ntfy parse JSON best when the body is
// minimal.
func TestMarshalPayload_OmitemptyTrimsTheBody(t *testing.T) {
	p := &Payload{
		SchemaVersion: 1,
		Agent:         "a",
		Crew:          "c",
		Concern:       "msg",
	}
	raw, err := MarshalPayload(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	for _, banned := range []string{`"channel":""`, `"threadId":""`, `"timestamp":""`, `"dashboardUrl":""`} {
		if strings.Contains(s, banned) {
			t.Errorf("payload should omit empty optional field: found %q in %s", banned, s)
		}
	}
	// And confirm load-bearing fields are present.
	var decoded Payload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Concern != "msg" || decoded.Agent != "a" || decoded.Crew != "c" {
		t.Errorf("required fields not preserved: %+v", decoded)
	}
}
