package a2a

import (
	"encoding/json"
	"strings"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
)

// Signals the sidecar publishes for a turn, as the bus contract names them.
const (
	SignalAgree      = "agree"
	SignalConcern    = "concern"
	SignalBlock      = "block"
	SignalFailure    = "failure"
	SignalStandAside = "stand_aside"
	// FailureEmpty is the failureType of a reply with no content; empty is never a silent stand aside.
	FailureEmpty = "empty_result"
)

// Evidence is what a harness says its answer rests on.
type Evidence struct {
	Tool string `json:"tool,omitempty"`
	Note string `json:"note,omitempty"`
}

// Outcome is what the sidecar publishes for a turn.
type Outcome struct {
	// Signal is agree, concern, block, stand_aside or failure.
	Signal      string
	Content     string
	Reason      string
	FailureType string
	Evidence    []Evidence
}

// Interpret applies the design's table to a harness reply. A transport error is a failure, never
// silence.
func Interpret(r Result, sendErr error) Outcome {
	if sendErr != nil {
		return Outcome{Signal: SignalFailure, FailureType: "harness_error", Reason: sendErr.Error()}
	}
	switch r.State {
	case sdk.TaskStateCompleted:
		return fromParts(parts(r))
	case sdk.TaskStateRejected:
		return Outcome{Signal: SignalStandAside, Reason: "rejected"}
	case sdk.TaskStateInputRequired, sdk.TaskStateAuthRequired:
		return Outcome{Signal: SignalFailure, FailureType: "input_required"}
	case sdk.TaskStateCanceled:
		return Outcome{Signal: SignalFailure, FailureType: "harness_canceled"}
	default:
		return Outcome{Signal: SignalFailure, FailureType: "harness_failed", Reason: r.State.String()}
	}
}

// parts are the content parts of a finished result: the artifacts', else the final message's.
func parts(r Result) []*sdk.Part {
	if len(r.Parts) > 0 {
		return r.Parts
	}
	if r.Message != nil {
		return r.Message.Parts
	}
	return nil
}

var terminal = map[string]bool{SignalAgree: true, SignalConcern: true, SignalBlock: true, SignalStandAside: true}

func fromParts(ps []*sdk.Part) Outcome {
	var text strings.Builder
	for _, p := range ps {
		if v := p.Data(); v != nil {
			if o, ok := fromData(v); ok {
				return o
			}
			continue
		}
		text.WriteString(p.Text())
	}
	if strings.TrimSpace(text.String()) == "" {
		return Outcome{Signal: SignalFailure, FailureType: FailureEmpty}
	}
	return Outcome{Signal: SignalAgree, Content: text.String()}
}

// fromData reads the turn profile's result data part. A data part that is not a result (no
// recognized signal) is ignored, so the text parts still count.
func fromData(v any) (Outcome, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Outcome{}, false
	}
	var d struct {
		Signal   string     `json:"signal"`
		Content  string     `json:"content"`
		Reason   string     `json:"reason"`
		Evidence []Evidence `json:"evidence"`
	}
	if err := json.Unmarshal(raw, &d); err != nil || !terminal[d.Signal] {
		return Outcome{}, false
	}
	if d.Signal != SignalStandAside && strings.TrimSpace(d.Content) == "" {
		return Outcome{Signal: SignalFailure, FailureType: FailureEmpty}, true
	}
	return Outcome{Signal: d.Signal, Content: d.Content, Reason: d.Reason, Evidence: d.Evidence}, true
}
