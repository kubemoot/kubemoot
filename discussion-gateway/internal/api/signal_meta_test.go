package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// agentNameKey is the key of an agent's name in triage_result metadata.
const agentNameKey = "name"

// emitted translates one message and returns the single event it produced.
func emitted(t *testing.T, m bus.DiscussionMessage) SSEEvent {
	t.Helper()
	var got []SSEEvent
	translateAndEmit(m, func(e SSEEvent) { got = append(got, e) })
	if len(got) != 1 {
		t.Fatalf("%s: want one event, got %+v", m.MessageType, got)
	}
	return got[0]
}

// The metadata as it arrives from JSON: numbers are float64, lists []interface{}.
func agreeMetadata() map[string]interface{} {
	return map[string]interface{}{
		"modelName":     actModel,
		"modelsUsed":    []interface{}{"qwen3:32b@rig-a", actModel + "@" + testGPU},
		"gpuLabel":      testGPU,
		metaInputTokens: float64(1200), metaOutputTokens: float64(340), "totalTokens": float64(1540),
		"inferenceMs": float64(6100), "toolsUsed": []interface{}{"pods_list", "", 7, "nodes_top"},
		"artifact": map[string]interface{}{"key": testArtifact, "bytes": float64(8499)},
	}
}

func TestFindingCarriesTheSignalMetadata(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: signalAgree, AgentName: testAgent, Content: "x", Metadata: agreeMetadata()})
	want := &bus.SignalMeta{
		ModelName:   actModel,
		ModelsUsed:  []string{"qwen3:32b@rig-a", actModel + "@" + testGPU},
		InputTokens: 1200, OutputTokens: 340, TotalTokens: 1540, InferenceMs: 6100,
		ToolsUsed: []string{"pods_list", "nodes_top"},
		Artifact:  &bus.ArtifactRef{Key: testArtifact, Bytes: 8499},
	}
	if !reflect.DeepEqual(e.SignalMeta, want) || e.GPU != testGPU {
		t.Fatalf("got %+v (gpu %q), want %+v", e.SignalMeta, e.GPU, want)
	}
	data, _ := json.Marshal(e)
	for _, field := range []string{`"inputTokens":1200`, `"modelName":"qwen3:14b"`, `"modelsUsed":["qwen3:32b@rig-a","qwen3:14b@`, `"artifact":{"key":"ns/pilot/t1/k8s/agree-1","bytes":8499}`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("JSON lacks %s: %s", field, data)
		}
	}
}

func TestStandAsideCarriesItsTokens(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: signalStandAside, AgentName: testAgent, Metadata: map[string]interface{}{
		metaReason: "not relevant", metaInputTokens: float64(300), metaOutputTokens: float64(12), "modelName": "qwen3:8b",
	}})
	if e.SignalMeta == nil || e.InputTokens != 300 || e.OutputTokens != 12 || e.ModelName != "qwen3:8b" || e.Reason != "not relevant" {
		t.Fatalf("got %+v", e)
	}
}

// A failure says what failed: the published tool errors.
func TestFailureCarriesTheToolError(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: signalFailure, AgentName: testAgent, Content: "tools failed", Metadata: map[string]interface{}{
		"failureType": "too_many_tool_failures", "failedTool": "pods_log", "failureCount": float64(3), "lastError": "timeout",
	}})
	if e.FailureType != "too_many_tool_failures" || e.FailedTool != "pods_log" || e.FailureCount != 3 || e.LastError != "timeout" {
		t.Fatalf("got %+v", e.SignalMeta)
	}
}

// A signal from an agent that sends no metadata carries none of the new fields.
func TestSignalWithoutMetadataAddsNoFields(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: signalAgree, AgentName: testAgent, Content: "x"})
	if e.SignalMeta != nil {
		t.Fatalf("want no signal metadata, got %+v", e.SignalMeta)
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "Tokens") || strings.Contains(string(data), "members") {
		t.Fatalf("unexpected fields: %s", data)
	}
	if signalMetaOf(bus.DiscussionMessage{Metadata: map[string]interface{}{"artifact": map[string]interface{}{"bytes": 3.0}}}) != nil {
		t.Error("an artifact without a key is no artifact")
	}
}

func TestAdvisoryReadyIsTheSubcommittee(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: msgAdvisoryReady, AgentName: "coord", Metadata: map[string]interface{}{
		"innerCircle": []interface{}{"storage", testAgent}, msgAdvisory: "Check the nodes first.",
		"triageConfidence": 0.9, "technologies": []interface{}{"proxmox"},
	}})
	want := &bus.Subcommittee{
		Source: subcommitteeFromAdvisory, Members: []bus.SubcommitteeMember{{Agent: testAgent}, {Agent: "storage"}},
		Brief: "Check the nodes first.", Confidence: 0.9, Technologies: []string{"proxmox"},
	}
	if e.Type != eventSubcommittee || e.Agent != "coord" || !reflect.DeepEqual(e.Subcommittee, want) {
		t.Fatalf("got %+v %+v", e, e.Subcommittee)
	}
}

func TestAdvisoryReadyWithoutAnInnerCircleConvenesEveryone(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: msgAdvisoryReady, Metadata: map[string]interface{}{msgAdvisory: "brief"}})
	if !e.AllAgents || len(e.Members) != 0 || e.Brief != "brief" {
		t.Fatalf("got %+v", e.Subcommittee)
	}
}

func TestTriageResultCarriesEachAgentsReason(t *testing.T) {
	e := emitted(t, bus.DiscussionMessage{MessageType: msgTriageResult, Metadata: map[string]interface{}{
		"overallConfidence": 0.7,
		"agents": []interface{}{
			map[string]interface{}{agentNameKey: "rules", "confidence": 0.8, metaReason: "policy question"},
			map[string]interface{}{agentNameKey: "", metaReason: "nameless"},
			"not an object",
			map[string]interface{}{agentNameKey: testAgent, "confidence": 0.6},
		},
	}})
	want := &bus.Subcommittee{Source: subcommitteeFromTriage, Confidence: 0.7, Members: []bus.SubcommitteeMember{
		{Agent: testAgent, Confidence: 0.6}, {Agent: "rules", Reason: "policy question", Confidence: 0.8},
	}}
	if !reflect.DeepEqual(e.Subcommittee, want) {
		t.Fatalf("got %+v, want %+v", e.Subcommittee, want)
	}
}

func TestTimestampPrefersTheMessagesOwn(t *testing.T) {
	stored := time.Date(2026, 10, 15, 12, 0, 5, 0, time.UTC)
	cases := []struct {
		name string
		m    discussMsg
		want string
	}{
		{"own, normalized to UTC", discussMsg{data: bus.DiscussionMessage{Timestamp: "2026-10-15T14:00:01.5+02:00"}, published: stored}, "2026-10-15T12:00:01.5Z"},
		{"stored when absent", discussMsg{published: stored}, "2026-10-15T12:00:05Z"},
		{"stored when malformed", discussMsg{data: bus.DiscussionMessage{Timestamp: "yesterday"}, published: stored}, "2026-10-15T12:00:05Z"},
		{"none", discussMsg{}, ""},
	}
	for _, c := range cases {
		if got := c.m.timestamp(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestToFloatReadsJSONNumbersOnly(t *testing.T) {
	if toFloat(float64(2)) != 2 || toInt(float64(2.9)) != 2 {
		t.Error("a JSON number is read")
	}
	if toFloat("2") != 0 || toFloat(nil) != 0 {
		t.Error("a non-number is zero")
	}
}
