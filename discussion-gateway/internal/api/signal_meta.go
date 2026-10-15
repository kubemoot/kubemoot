package api

import (
	"reflect"
	"sort"
	"time"
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

// SignalMeta is what an agent's signal metadata says about how it was made: the model,
// token counts and inference time of its call, the tools it has enabled, the artifact
// it spilled, and for a failure, what failed. Absent values are left out.
type SignalMeta struct {
	ModelName    string `json:"modelName,omitempty"`
	InputTokens  int64  `json:"inputTokens,omitempty"`
	OutputTokens int64  `json:"outputTokens,omitempty"`
	TotalTokens  int64  `json:"totalTokens,omitempty"`
	InferenceMs  int64  `json:"inferenceMs,omitempty"`
	// ToolsUsed is the agent runtime's toolsUsed: the tools the agent has enabled,
	// sent on agree signals.
	ToolsUsed []string `json:"toolsUsed,omitempty"`
	// FailureType, FailedTool, FailureCount and LastError describe a failure signal:
	// the kind of failure, the tool that failed, how many tool calls failed, and the
	// last error text.
	FailureType  string       `json:"failureType,omitempty"`
	FailedTool   string       `json:"failedTool,omitempty"`
	FailureCount int64        `json:"failureCount,omitempty"`
	LastError    string       `json:"lastError,omitempty"`
	Artifact     *ArtifactRef `json:"artifact,omitempty"`
}

// ArtifactRef names a contribution spilled to the discussion artifact store.
type ArtifactRef struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes,omitempty"`
}

// Subcommittee is the agents the coordinator convened for a thread. From the advisory
// it carries the brief; from a triage selection, each agent's reason and confidence.
type Subcommittee struct {
	Source  string               `json:"source,omitempty"`
	Members []SubcommitteeMember `json:"members,omitempty"`
	// AllAgents is set when the coordinator convened no subcommittee and every agent
	// may take part.
	AllAgents    bool     `json:"allAgents,omitempty"`
	Brief        string   `json:"brief,omitempty"`
	Confidence   float64  `json:"confidence,omitempty"`
	Technologies []string `json:"technologies,omitempty"`
}

// SubcommitteeMember is one convened agent.
type SubcommitteeMember struct {
	Agent      string  `json:"agent"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// signalMetaOf reads the signal metadata of data, or nil when it carries none.
func signalMetaOf(data natsMessage) *SignalMeta {
	m := SignalMeta{
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
func artifactRefOf(data natsMessage) *ArtifactRef {
	ref, ok := data.Metadata["artifact"].(map[string]interface{})
	if !ok {
		return nil
	}
	key, _ := ref["key"].(string)
	if key == "" {
		return nil
	}
	return &ArtifactRef{Key: key, Bytes: toInt(ref["bytes"])}
}

// subcommitteeEvent maps the coordinator's advisory_ready and triage_result to a
// subcommittee event.
func subcommitteeEvent(data natsMessage) (SSEEvent, bool) {
	var s Subcommittee
	switch data.MessageType {
	case msgAdvisoryReady:
		s = advisorySubcommittee(data)
	case msgTriageResult:
		s = triageSubcommittee(data)
	default:
		return SSEEvent{}, false
	}
	return SSEEvent{Type: eventSubcommittee, Agent: data.AgentName, Subcommittee: &s}, true
}

// advisorySubcommittee is the inner circle of advisory_ready with the coordinator's
// brief. Without an inner circle, every agent may take part.
func advisorySubcommittee(data natsMessage) Subcommittee {
	circle := metaStrings(data, "innerCircle")
	members := make([]SubcommitteeMember, 0, len(circle))
	for _, agent := range circle {
		members = append(members, SubcommitteeMember{Agent: agent})
	}
	sortMembers(members)
	return Subcommittee{
		Source: subcommitteeFromAdvisory, Members: members, AllAgents: len(members) == 0,
		Brief: metaString(data, msgAdvisory), Confidence: metaFloat(data, "triageConfidence"),
		Technologies: metaStrings(data, "technologies"),
	}
}

// triageSubcommittee is the triage selection: each agent with its reason and confidence.
func triageSubcommittee(data natsMessage) Subcommittee {
	raw, _ := data.Metadata["agents"].([]interface{})
	members := []SubcommitteeMember{}
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
		members = append(members, SubcommitteeMember{Agent: name, Reason: reason, Confidence: toFloat(a["confidence"])})
	}
	sortMembers(members)
	return Subcommittee{Source: subcommitteeFromTriage, Members: members, Confidence: metaFloat(data, "overallConfidence")}
}

func sortMembers(members []SubcommitteeMember) {
	sort.Slice(members, func(i, j int) bool { return members[i].Agent < members[j].Agent })
}

// metaInt is a numeric metadata value as an integer, or zero.
func metaInt(data natsMessage, key string) int64 { return toInt(data.Metadata[key]) }

// metaFloat is a numeric metadata value, or zero.
func metaFloat(data natsMessage, key string) float64 { return toFloat(data.Metadata[key]) }

// metaStrings is a metadata list of strings, skipping anything that is not a string.
func metaStrings(data natsMessage, key string) []string {
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
