/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const markdownTime = "2006-01-02 15:04 MST"

const markdownTimeSeconds = "2006-01-02 15:04:05 MST"

// renderMarkdown is the conversation as a person reads it, times in the asker's zone.
func renderMarkdown(p *prepared) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Conversation %s\n\nCrew: %s/%s\n\nTimes are in %s.\n",
		p.id, p.scope.Namespace, p.scope.Crew, p.zone)
	for i, t := range p.held.Turns {
		fmt.Fprintf(&b, "\n## Turn %d", i+1)
		if !t.AnsweredAt.IsZero() {
			fmt.Fprintf(&b, ", answered %s", t.AnsweredAt.In(p.loc).Format(markdownTime))
		}
		fmt.Fprintf(&b, "\n\n### Question\n\n%s\n\n### Answer\n\n%s\n",
			strings.TrimSpace(t.Question), strings.TrimSpace(t.Answer))
	}
	return []byte(b.String())
}

type jsonTurn struct {
	ThreadID      string        `json:"threadId"`
	Question      string        `json:"question"`
	Answer        string        `json:"answer"`
	AnsweredAt    string        `json:"answeredAt,omitempty"`
	AnsweredAtUTC string        `json:"answeredAtUtc,omitempty"`
	Messages      []jsonMessage `json:"messages"`
}

// jsonMessage is one message of the turn's discussion. A message the screening withheld
// has no text and says so.
type jsonMessage struct {
	MessageID string `json:"messageId,omitempty"`
	Agent     string `json:"agent"`
	Signal    string `json:"signal"`
	Channel   string `json:"channel,omitempty"`
	At        string `json:"at,omitempty"`
	AtUTC     string `json:"atUtc,omitempty"`
	Text      string `json:"text"`
	Withheld  bool   `json:"withheld,omitempty"`
}

type jsonConversation struct {
	ConversationID string     `json:"conversationId"`
	Namespace      string     `json:"namespace"`
	Crew           string     `json:"crew"`
	TimeZone       string     `json:"timeZone"`
	Turns          []jsonTurn `json:"turns"`
}

// renderJSON is the same conversation structured: ids, the crew, and each time both in
// the asker's zone and in UTC.
func renderJSON(p *prepared) ([]byte, error) {
	out := jsonConversation{
		ConversationID: p.id, Namespace: p.scope.Namespace, Crew: p.scope.Crew, TimeZone: p.zone,
		Turns: make([]jsonTurn, 0, len(p.held.Turns)),
	}
	for _, t := range p.held.Turns {
		jt := jsonTurn{ThreadID: t.ThreadID, Question: t.Question, Answer: t.Answer, Messages: p.jsonMessages(t.ThreadID)}
		if !t.AnsweredAt.IsZero() {
			jt.AnsweredAt = t.AnsweredAt.In(p.loc).Format(time.RFC3339)
			jt.AnsweredAtUTC = t.AnsweredAt.UTC().Format(time.RFC3339)
		}
		out.Turns = append(out.Turns, jt)
	}
	return json.MarshalIndent(out, "", "  ")
}

func (p *prepared) jsonMessages(thread string) []jsonMessage {
	out := make([]jsonMessage, 0, len(p.moot.threads[thread]))
	for _, m := range p.moot.threads[thread] {
		jm := jsonMessage{
			MessageID: m.ID, Agent: m.Agent, Signal: m.Signal, Channel: m.Channel, Text: m.Text, Withheld: m.Withheld,
		}
		if !m.At.IsZero() {
			jm.At = m.At.In(p.loc).Format(time.RFC3339)
			jm.AtUTC = m.At.UTC().Format(time.RFC3339)
		}
		out = append(out, jm)
	}
	return out
}

// renderMoot is the deliberation of every turn as a person reads it: each message in the
// order the stream held it, with who sent it, its signal and its time in the asker's zone.
func renderMoot(p *prepared) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Discussion of conversation %s\n\nCrew: %s/%s\n\nTimes are in %s.\n",
		p.id, p.scope.Namespace, p.scope.Crew, p.zone)
	for i, t := range p.held.Turns {
		fmt.Fprintf(&b, "\n## Turn %d\n\nQuestion: %s\n", i+1, strings.TrimSpace(t.Question))
		msgs := p.moot.threads[t.ThreadID]
		if len(msgs) == 0 {
			b.WriteString("\nNo message of this turn's discussion is stored.\n")
		}
		for _, m := range msgs {
			writeMootMessage(&b, p, m)
		}
	}
	return []byte(b.String())
}

func writeMootMessage(b *strings.Builder, p *prepared, m shown) {
	fmt.Fprintf(b, "\n### %s, %s", m.Agent, m.Signal)
	if !m.At.IsZero() {
		fmt.Fprintf(b, ", %s", m.At.In(p.loc).Format(markdownTimeSeconds))
	}
	b.WriteString("\n\n")
	switch {
	case m.Withheld:
		b.WriteString("(withheld: the text holds a credential)\n")
	case strings.TrimSpace(m.Text) == "":
		b.WriteString("(no text)\n")
	default:
		b.WriteString(strings.TrimSpace(m.Text) + "\n")
	}
}
