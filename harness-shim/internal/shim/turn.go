package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Turn is one request from the sidecar, reduced to what the command needs.
type Turn struct {
	ContextID    string
	Question     string
	Instructions string
	Role         string
	Thread       []ThreadEntry
	TurnKey      string
	TimeZone     string
	Now          string
	Deadline     time.Duration
}

// ThreadEntry is one earlier message on the thread.
type ThreadEntry struct {
	Agent   string `json:"agent"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

type sendRequest struct {
	Message struct {
		ContextID string `json:"contextId"`
		Parts     []struct {
			Text string          `json:"text"`
			Data json.RawMessage `json:"data"`
		} `json:"parts"`
	} `json:"message"`
}

type profile struct {
	Role            string        `json:"role"`
	Instructions    string        `json:"instructions"`
	Thread          []ThreadEntry `json:"thread"`
	TurnKey         string        `json:"turnKey"`
	DeadlineSeconds int           `json:"deadlineSeconds"`
	Now             string        `json:"now"`
	TimeZone        string        `json:"timeZone"`
}

// ParseTurn reads a SendMessage body. The question is the text parts; the turn profile is the data
// part. A message with no question is refused. A zero deadline in the profile means the default.
func ParseTurn(body []byte, maxDeadline time.Duration) (Turn, error) {
	var req sendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return Turn{}, fmt.Errorf("the body is not a SendMessage request: %w", err)
	}
	t := Turn{ContextID: req.Message.ContextID, Deadline: DefaultDeadline}
	var q []string
	for _, p := range req.Message.Parts {
		if p.Text != "" {
			q = append(q, p.Text)
		}
		if len(p.Data) > 0 {
			applyProfile(&t, p.Data)
		}
	}
	t.Question = strings.TrimSpace(strings.Join(q, "\n"))
	if t.Question == "" {
		return Turn{}, errors.New("the message has no text part to answer")
	}
	t.Deadline = min(t.Deadline, maxDeadline)
	return t, nil
}

// applyProfile copies the data part into t. A data part that is not the profile is ignored.
func applyProfile(t *Turn, raw json.RawMessage) {
	var p profile
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	t.Role, t.Instructions, t.Thread = p.Role, p.Instructions, p.Thread
	t.TurnKey, t.Now = p.TurnKey, p.Now
	if _, err := time.LoadLocation(p.TimeZone); err == nil {
		t.TimeZone = p.TimeZone
	}
	if p.DeadlineSeconds > 0 {
		t.Deadline = time.Duration(p.DeadlineSeconds) * time.Second
	}
}

// Prompt is the text the command is asked: the agent's instructions, the thread so far, the
// current time, and the question.
func (t Turn) Prompt() string {
	var b strings.Builder
	if t.Instructions != "" {
		b.WriteString(t.Instructions + "\n\n")
	}
	if len(t.Thread) > 0 {
		b.WriteString("The discussion so far:\n")
		for _, e := range t.Thread {
			fmt.Fprintf(&b, "- %s (%s): %s\n", e.Agent, e.Type, e.Content)
		}
		b.WriteString("\n")
	}
	if t.Now != "" {
		fmt.Fprintf(&b, "The current time is %s", t.Now)
		if t.TimeZone != "" {
			fmt.Fprintf(&b, " (the requester's time zone is %s)", t.TimeZone)
		}
		b.WriteString(".\n\n")
	}
	b.WriteString(t.Question)
	return b.String()
}
