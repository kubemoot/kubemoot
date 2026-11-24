package a2a

import (
	"strings"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
)

// The phases a turn runs in, as the turn profile names them.
const (
	PhaseEvaluating = "EVALUATING"
	PhaseReview     = "REVIEW"
)

// TurnInput is the Kubemoot turn profile data part the sidecar sends with the question.
type TurnInput struct {
	Profile string `json:"profile"`
	// Kind is discussion or chat.
	Kind  string `json:"kind"`
	Agent string `json:"agent"`
	Crew  string `json:"crew"`
	Role  string `json:"role,omitempty"`
	Phase string `json:"phase"`
	// Instructions are the agent's assembled instructions (the PromptModules, plus the bodies of the
	// skills the coordinator selected).
	Instructions string         `json:"instructions,omitempty"`
	Thread       []ThreadEntry  `json:"thread,omitempty"`
	Models       map[string]any `json:"models,omitempty"`
	// TurnKey is sent back as the OpenAI API key and the MCP bearer token, so the sidecar can
	// attribute each call to its turn.
	TurnKey         string `json:"turnKey"`
	DeadlineSeconds int    `json:"deadlineSeconds"`
	// Now is the time the turn was handed over (RFC 3339), for a harness without a time tool.
	Now string `json:"now,omitempty"`
	// RequestDate (YYYY-MM-DD, fixed when the question arrived) and TimeZone are the requester's,
	// when the bus message carried them.
	RequestDate string `json:"requestDate,omitempty"`
	TimeZone    string `json:"timeZone,omitempty"`
	// Context is the one line every turn starts with: the asker's date and zone, and the crew's
	// namespace and name. A harness puts it in front of the question as it is.
	Context string `json:"context,omitempty"`
}

// ContextLine is the turn context line, for example "Today is 2026-11-20 (America/New_York).
// You are in namespace crew-homelab-pilot, crew homelab-pilot." A part that is not known is left
// out. It carries the date and never a clock time, so it is the same for every turn of a
// discussion. caller is the sentence that says who is asking (callerline.Sentence), added at the
// end; empty adds nothing, which is every crew's default. The native agents build the same line
// (agent-runtime, TurnContext); change them together.
func ContextLine(requestDate, timeZone, namespace, crew, caller string) string {
	var parts []string
	if requestDate != "" {
		today := "Today is " + requestDate
		if timeZone != "" {
			today += " (" + timeZone + ")"
		}
		parts = append(parts, today+".")
	}
	if place := placeOf(namespace, crew); place != "" {
		parts = append(parts, "You are in "+place+".")
	}
	if caller != "" {
		parts = append(parts, caller)
	}
	return strings.Join(parts, " ")
}

func placeOf(namespace, crew string) string {
	switch {
	case namespace != "" && crew != "":
		return "namespace " + namespace + ", crew " + crew
	case namespace != "":
		return "namespace " + namespace
	case crew != "":
		return "crew " + crew
	}
	return ""
}

// ThreadEntry is one earlier message on the thread.
type ThreadEntry struct {
	Agent   string `json:"agent"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// NewTurn builds the A2A message for one turn: the question as text and the profile as data.
func NewTurn(threadID, question string, in TurnInput) *sdk.Message {
	in.Profile = ProfileURI
	m := sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart(question), sdk.NewDataPart(in))
	m.ContextID = threadID
	return m
}
