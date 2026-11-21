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
	ThreadID      string `json:"threadId"`
	Question      string `json:"question"`
	Answer        string `json:"answer"`
	AnsweredAt    string `json:"answeredAt,omitempty"`
	AnsweredAtUTC string `json:"answeredAtUtc,omitempty"`
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
		jt := jsonTurn{ThreadID: t.ThreadID, Question: t.Question, Answer: t.Answer}
		if !t.AnsweredAt.IsZero() {
			jt.AnsweredAt = t.AnsweredAt.In(p.loc).Format(time.RFC3339)
			jt.AnsweredAtUTC = t.AnsweredAt.UTC().Format(time.RFC3339)
		}
		out.Turns = append(out.Turns, jt)
	}
	return json.MarshalIndent(out, "", "  ")
}
