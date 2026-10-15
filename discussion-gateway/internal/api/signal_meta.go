package api

import (
	"reflect"
	"sort"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// eventSubcommittee is the SSE event that names the agents the coordinator convened.
const eventSubcommittee = "subcommittee"

// msgTriageResult is the coordinator's triage selection, with a reason per agent.
const msgTriageResult = "triage_result"

// Discussion message metadata keys: the conversation a thread_start belongs to, an
// agent's reason, and a signal's input tokens.
const (
	metaConversationID = "conversationId"
	metaReason         = "reason"
	metaInputTokens    = "inputTokens"
	metaOutputTokens   = "outputTokens"
)

// Where a subcommittee event comes from.
const (
	subcommitteeFromAdvisory = "advisory"
	subcommitteeFromTriage   = "triage"
)

// signalMetaOf reads the signal metadata of data, or nil when it carries none.
func signalMetaOf(data bus.DiscussionMessage) *bus.SignalMeta {
	m := bus.SignalMeta{
		ModelName:    metaString(data, "modelName"),
		InputTokens:  metaInt(data, metaInputTokens),
		OutputTokens: metaInt(data, metaOutputTokens),
		TotalTokens:  metaInt(data, "totalTokens"),
		InferenceMs:  metaInt(data, "inferenceMs"),
		ToolsUsed:    metaStrings(data, "toolsUsed"),
		FailureType:  metaString(data, "failureType"),
		FailedTool:   metaString(data, "failedTool"),
		FailureCount: metaInt(data, "failureCount"),
		LastError:    metaString(data, "lastError"),
		Artifact:     artifactRefOf(data),
	}
	if reflect.ValueOf(m).IsZero() {
		return nil
	}
	return &m
}

// artifactRefOf reads metadata.artifact, the reference an agent adds when it spills a
// large contribution, or nil.
func artifactRefOf(data bus.DiscussionMessage) *bus.ArtifactRef {
	ref, ok := data.Metadata["artifact"].(map[string]interface{})
	if !ok {
		return nil
	}
	key, _ := ref["key"].(string)
	if key == "" {
		return nil
	}
	return &bus.ArtifactRef{Key: key, Bytes: toInt(ref["bytes"])}
}

// subcommitteeEvent maps the coordinator's advisory_ready and triage_result to a
// subcommittee event.
func subcommitteeEvent(data bus.DiscussionMessage) (SSEEvent, bool) {
	var s bus.Subcommittee
	switch data.MessageType {
	case msgAdvisoryReady:
		s = advisorySubcommittee(data)
	case msgTriageResult:
		s = triageSubcommittee(data)
	default:
		return SSEEvent{}, false
	}
	e := sseEvent(bus.StreamEventBase{Type: eventSubcommittee, Agent: data.AgentName})
	e.Subcommittee = &s
	return e, true
}

// advisorySubcommittee is the inner circle of advisory_ready with the coordinator's
// brief. Without an inner circle, every agent may take part.
func advisorySubcommittee(data bus.DiscussionMessage) bus.Subcommittee {
	circle := metaStrings(data, "innerCircle")
	members := make([]bus.SubcommitteeMember, 0, len(circle))
	for _, agent := range circle {
		members = append(members, bus.SubcommitteeMember{Agent: agent})
	}
	sortMembers(members)
	return bus.Subcommittee{
		Source: subcommitteeFromAdvisory, Members: members, AllAgents: len(members) == 0,
		Brief: metaString(data, msgAdvisory), Confidence: metaFloat(data, "triageConfidence"),
		Technologies: metaStrings(data, "technologies"),
	}
}

// triageSubcommittee is the triage selection: each agent with its reason and confidence.
func triageSubcommittee(data bus.DiscussionMessage) bus.Subcommittee {
	raw, _ := data.Metadata["agents"].([]interface{})
	members := []bus.SubcommitteeMember{}
	for _, item := range raw {
		a, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := a["name"].(string)
		if name == "" {
			continue
		}
		reason, _ := a[metaReason].(string)
		members = append(members, bus.SubcommitteeMember{Agent: name, Reason: reason, Confidence: toFloat(a["confidence"])})
	}
	sortMembers(members)
	return bus.Subcommittee{
		Source: subcommitteeFromTriage, Members: members, Confidence: metaFloat(data, "overallConfidence"),
	}
}

func sortMembers(members []bus.SubcommitteeMember) {
	sort.Slice(members, func(i, j int) bool { return members[i].Agent < members[j].Agent })
}

// metaInt is a numeric metadata value as an integer, or zero.
func metaInt(data bus.DiscussionMessage, key string) int64 { return toInt(data.Metadata[key]) }

// metaFloat is a numeric metadata value, or zero.
func metaFloat(data bus.DiscussionMessage, key string) float64 { return toFloat(data.Metadata[key]) }

// metaStrings is a metadata list of strings, skipping anything that is not a string.
func metaStrings(data bus.DiscussionMessage, key string) []string {
	raw, _ := data.Metadata[key].([]interface{})
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func toInt(v interface{}) int64 {
	return int64(toFloat(v))
}

// toFloat is v as a number when it is one; JSON decodes every number as float64.
func toFloat(v interface{}) float64 {
	f, _ := v.(float64)
	return f
}

// timestamp is when m was published: the message's own timestamp, else the time NATS
// stored it, as RFC 3339 in UTC; empty when neither is known.
func (m discussMsg) timestamp() string {
	if t, err := time.Parse(time.RFC3339Nano, m.data.Timestamp); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	if m.published.IsZero() {
		return ""
	}
	return m.published.UTC().Format(time.RFC3339Nano)
}

// stamped wraps emit so every event it sends carries m's timestamp.
func stamped(m discussMsg, emit func(SSEEvent)) func(SSEEvent) {
	ts := m.timestamp()
	return func(e SSEEvent) {
		e.Timestamp = ts
		emit(e)
	}
}
