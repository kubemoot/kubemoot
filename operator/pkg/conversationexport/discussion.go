/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"fmt"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/secretscreen"
)

// DefaultMaxDiscussionBytes bounds the message text one conversation export carries. A
// message past the bound is left out and the manifest says so; the stream's own limits
// already bound what is stored, this keeps one download from growing without end.
const DefaultMaxDiscussionBytes = 8 << 20

// shown is one message as the export carries it: the text, or none when the screening
// withheld it. A withheld message keeps its agent, signal and time so the order of the
// discussion stays readable.
type shown struct {
	Message
	Text     string
	Withheld bool
}

// discussionPart is the deliberation of every turn, screened and bounded before anything
// is written, so a report or a refusal knows what the zip would hold.
type discussionPart struct {
	threads map[string][]shown
	part    Part
	leftOut []LeftOut
}

// discussionOf screens the held messages with the screen the memory export uses
// (secretscreen.Find): a message whose text holds a credential keeps its place but not
// its text, and is listed by its id and the credential's shape, never by its text.
func (b *Builder) discussionOf(p *prepared) discussionPart {
	limit := b.MaxDiscussionBytes
	if limit <= 0 {
		limit = DefaultMaxDiscussionBytes
	}
	out := discussionPart{threads: map[string][]shown{}, part: Part{Path: FileMoot, Status: StatusNotKept}}
	used, cut, threadsWith := 0, 0, 0
	for _, thread := range threadIDs(p.held.Turns) {
		msgs := p.held.Messages[thread]
		if len(msgs) == 0 {
			continue
		}
		threadsWith++
		for i, m := range msgs {
			if used+len(m.Content) > limit {
				cut += len(msgs) - i
				break
			}
			used += len(m.Content)
			out.add(thread, i, m)
		}
	}
	out.summarize(len(threadIDs(p.held.Turns)), threadsWith, cut)
	return out
}

func (d *discussionPart) add(thread string, index int, m Message) {
	s := shown{Message: m, Text: m.Content}
	if shape, found := secretscreen.Find(m.Content); found {
		s.Text, s.Withheld = "", true
		d.leftOut = append(d.leftOut, LeftOut{
			Key:    messageKey(thread, index, m),
			Reason: "its text holds a credential (" + shape + "); not exported",
		})
	} else {
		d.part.Count++
	}
	d.threads[thread] = append(d.threads[thread], s)
}

// messageKey names a left-out message: its thread and id, or its place when it has no id.
func messageKey(thread string, index int, m Message) string {
	if m.ID != "" {
		return "discussion/" + safeName(thread) + "/" + safeName(m.ID)
	}
	return fmt.Sprintf("discussion/%s/#%d", safeName(thread), index+1)
}

func (d *discussionPart) summarize(threads, withMessages, cut int) {
	if withMessages == 0 {
		d.part.Reason = "no discussion message is stored for this conversation; " +
			"messages are kept only while the discussion stream holds them"
		return
	}
	d.part.Status = StatusIncluded
	var notes []string
	if withMessages < threads {
		notes = append(notes, fmt.Sprintf("%d of %d turns still have stored messages", withMessages, threads))
	}
	if cut > 0 {
		notes = append(notes, fmt.Sprintf("%d later messages are left out: the discussion passed the size limit", cut))
		d.leftOut = append(d.leftOut, LeftOut{Key: "discussion/size-limit", Reason: notes[len(notes)-1]})
	}
	d.part.Reason = strings.Join(notes, "; ")
}
