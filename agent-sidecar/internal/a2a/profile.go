package a2a

import (
	"encoding/json"
	"strings"
)

// TurnInput is the Kubemoot turn profile data part the sidecar sends with the question.
type TurnInput struct {
	Profile         string         `json:"profile"`
	Kind            string         `json:"kind"`
	Agent           string         `json:"agent"`
	Crew            string         `json:"crew"`
	Role            string         `json:"role,omitempty"`
	Phase           string         `json:"phase"`
	Instructions    string         `json:"instructions,omitempty"`
	Thread          []ThreadEntry  `json:"thread,omitempty"`
	Models          map[string]any `json:"models,omitempty"`
	TurnKey         string         `json:"turnKey"`
	DeadlineSeconds int            `json:"deadlineSeconds"`
	Now             string         `json:"now,omitempty"`
}

// ThreadEntry is one earlier message on the thread.
type ThreadEntry struct {
	Agent   string `json:"agent"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// NewTurn builds the A2A message for one discussion turn.
func NewTurn(messageID, threadID, question string, in TurnInput) (Message, error) {
	in.Profile = ProfileURI
	data, err := json.Marshal(in)
	if err != nil {
		return Message{}, err
	}
	return Message{
		MessageID: messageID, ContextID: threadID, Role: "ROLE_USER",
		Parts: []Part{{Text: question}, {Data: data}},
	}, nil
}

// Signals the sidecar publishes for a turn, as the bus contract names them.
const (
	SignalAgree      = "agree"
	SignalFailure    = "failure"
	SignalStandAside = "stand_aside"
	// FailureEmpty is the failureType of a reply with no content; empty is never a silent stand aside.
	FailureEmpty = "empty_result"
)

// Outcome is what the sidecar publishes for a turn.
type Outcome struct {
	// Signal is agree, concern, block, stand_aside or failure.
	Signal      string
	Content     string
	Reason      string
	FailureType string
}

// Interpret applies the design's table to a harness reply. A transport error is a failure, never silence.
func Interpret(resp SendResponse, sendErr error) Outcome {
	if sendErr != nil {
		return Outcome{Signal: SignalFailure, FailureType: "harness_error", Reason: sendErr.Error()}
	}
	if resp.Task == nil {
		return fromParts(resp.Message.partsOrNil())
	}
	switch resp.Task.Status.State {
	case "TASK_STATE_COMPLETED":
		return fromParts(allParts(resp.Task))
	case "TASK_STATE_REJECTED":
		return Outcome{Signal: SignalStandAside, Reason: "rejected"}
	case "TASK_STATE_INPUT_REQUIRED", "TASK_STATE_AUTH_REQUIRED":
		return Outcome{Signal: SignalFailure, FailureType: "input_required"}
	default:
		return Outcome{Signal: SignalFailure, FailureType: "harness_failed", Reason: resp.Task.Status.State}
	}
}

func (m *Message) partsOrNil() []Part {
	if m == nil {
		return nil
	}
	return m.Parts
}

func allParts(t *Task) []Part {
	var parts []Part
	for _, a := range t.Artifacts {
		parts = append(parts, a.Parts...)
	}
	if len(parts) == 0 {
		parts = t.Status.Message.partsOrNil()
	}
	return parts
}

var terminal = map[string]bool{SignalAgree: true, "concern": true, "block": true, SignalStandAside: true}

func fromParts(parts []Part) Outcome {
	var text string
	for _, p := range parts {
		if len(p.Data) > 0 {
			if o, ok := fromData(p.Data); ok {
				return o
			}
			continue
		}
		text += p.Text
	}
	if strings.TrimSpace(text) == "" {
		return Outcome{Signal: SignalFailure, FailureType: FailureEmpty}
	}
	return Outcome{Signal: SignalAgree, Content: text}
}

func fromData(raw json.RawMessage) (Outcome, bool) {
	var d struct {
		Profile string `json:"profile"`
		Signal  string `json:"signal"`
		Content string `json:"content"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || !terminal[d.Signal] {
		return Outcome{}, false
	}
	if d.Signal != SignalStandAside && strings.TrimSpace(d.Content) == "" {
		return Outcome{Signal: SignalFailure, FailureType: FailureEmpty}, true
	}
	return Outcome{Signal: d.Signal, Content: d.Content, Reason: d.Reason}, true
}
