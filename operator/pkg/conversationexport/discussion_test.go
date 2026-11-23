/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package conversationexport

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

const (
	agentCoordinator = "coordinator"
	agentGPU         = "gpu-specialist"
	signalAdvisory   = "advisory"
	signalConcern    = "concern"
	signalSynthesis  = "synthesis"
)

// mootMessages is a small discussion of one thread: three agents, three signals.
func mootMessages(thread string) []Message {
	at := answered.Add(-time.Minute)
	return []Message{
		{ID: thread + "-m1", Agent: agentCoordinator, Signal: signalAdvisory, Content: "ask the " + thread + " specialists", At: at},
		{ID: thread + "-m2", Agent: agentGPU, Signal: signalConcern, Content: "memory is tight on " + thread, At: at.Add(10 * time.Second)},
		{ID: thread + "-m3", Agent: agentCoordinator, Signal: signalSynthesis, Content: "answer for " + thread, At: at.Add(20 * time.Second)},
	}
}

type exportedMessage struct {
	MessageID string `json:"messageId"`
	Agent     string `json:"agent"`
	Signal    string `json:"signal"`
	At        string `json:"at"`
	AtUTC     string `json:"atUtc"`
	Text      string `json:"text"`
	Withheld  bool   `json:"withheld"`
}

type exportedConversation struct {
	Turns []struct {
		ThreadID string            `json:"threadId"`
		Messages []exportedMessage `json:"messages"`
	} `json:"turns"`
}

func decodeConversation(t *testing.T, raw []byte) exportedConversation {
	t.Helper()
	var c exportedConversation
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEveryMessageOfEveryTurnIsExportedInOrderWithAgentSignalAndTime(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: fullHeld()}, nil), Options{TimeZone: zoneNewYork})
	md := string(files["discussion.md"])
	assertInOrder(t, md, []string{
		"## Turn 1", "### coordinator, advisory, 2026-11-20 14:02:00 EST", "ask the th-1 specialists",
		"### gpu-specialist, concern, 2026-11-20 14:02:10 EST", "memory is tight on th-1", "answer for th-1",
		"## Turn 2", "ask the th-2 specialists", "memory is tight on th-2", "answer for th-2",
	})
	assertJSONMessages(t, files["conversation.json"])
	if p := part(m, FileMoot); p.Status != StatusIncluded || p.Count != 6 || p.Reason != "" {
		t.Errorf("discussion part = %+v", p)
	}
}

func assertJSONMessages(t *testing.T, raw []byte) {
	t.Helper()
	conv := decodeConversation(t, raw)
	if len(conv.Turns) != 2 || len(conv.Turns[0].Messages) != 3 || len(conv.Turns[1].Messages) != 3 {
		t.Fatalf("conversation.json turns = %+v", conv.Turns)
	}
	first, second := conv.Turns[0].Messages[0], conv.Turns[0].Messages[1]
	if got := first.Agent + "," + first.Signal + "," + second.Agent + "," + second.Signal; got !=
		agentCoordinator+","+signalAdvisory+","+agentGPU+","+signalConcern {
		t.Errorf("messages = %+v", conv.Turns[0].Messages)
	}
	if first.AtUTC != "2026-11-20T19:02:00Z" || first.At != "2026-11-20T14:02:00-05:00" || first.MessageID != "th-1-m1" {
		t.Errorf("first message = %+v", first)
	}
}

// assertInOrder fails unless every want is in md, each after the one before.
func assertInOrder(t *testing.T, md string, wants []string) {
	t.Helper()
	last := -1
	for _, want := range wants {
		at := strings.Index(md, want)
		if at < 0 || at < last {
			t.Fatalf("discussion.md lacks %q or has it out of order:\n%s", want, md)
		}
		last = at
	}
}

func TestAMessageHoldingACredentialKeepsItsPlaceButNotItsText(t *testing.T) {
	held := fullHeld()
	held.Messages[thread1][1].Content = "use " + credential + " to log in"
	files, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	for name, body := range files {
		if strings.Contains(string(body), "abcdefgh12345678") {
			t.Errorf("%s holds the credential", name)
		}
	}
	assertWithheld(t, files)
	if len(m.LeftOut) != 1 || m.LeftOut[0].Key != "discussion/th-1/th-1-m2" || !strings.Contains(m.LeftOut[0].Reason, "credential") {
		t.Fatalf("leftOut = %+v", m.LeftOut)
	}
	if p := part(m, FileMoot); p.Count != 5 {
		t.Errorf("count = %d, want the five messages that were exported", p.Count)
	}
}

func assertWithheld(t *testing.T, files map[string][]byte) {
	t.Helper()
	got := decodeConversation(t, files["conversation.json"]).Turns[0].Messages[1]
	if !got.Withheld || got.Text != "" || got.Agent != agentGPU {
		t.Errorf("withheld message = %+v", got)
	}
	if !strings.Contains(string(files["discussion.md"]), "(withheld: the text holds a credential)") {
		t.Error("discussion.md does not say a message was withheld")
	}
}

func TestScreeningIsTheMemoryExportsScreen(t *testing.T) {
	held := fullHeld()
	held.Messages[thread2][0].ID = ""
	held.Messages[thread2][0].Content = credential
	_, m := exportOne(t, builderWith(fakeSource{convID: held}, crewMemory()), Options{})
	var message, fact int
	for _, l := range m.LeftOut {
		switch {
		case l.Key == "discussion/th-2/#1":
			message++
		case strings.Contains(l.Key, "ops"):
			fact++
		}
		if strings.Contains(l.Reason, "abcdefgh12345678") {
			t.Errorf("reason holds the credential: %q", l.Reason)
		}
	}
	if message != 1 || fact != 1 {
		t.Errorf("leftOut = %+v, want one message and one fact, both listed without their text", m.LeftOut)
	}
}

func TestAnEmptyThreadExportsCleanly(t *testing.T) {
	held := fullHeld()
	delete(held.Messages, thread2)
	files, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if !strings.Contains(string(files["discussion.md"]), "No message of this turn's discussion is stored.") {
		t.Errorf("discussion.md = %s", files["discussion.md"])
	}
	conv := decodeConversation(t, files["conversation.json"])
	if len(conv.Turns[1].Messages) != 0 || !strings.Contains(string(files["conversation.json"]), `"messages": []`) {
		t.Errorf("turn 2 messages = %+v", conv.Turns[1].Messages)
	}
	if p := part(m, FileMoot); p.Status != StatusIncluded || !strings.Contains(p.Reason, "1 of 2 turns") {
		t.Errorf("discussion part = %+v", p)
	}
}

func TestAConversationWithNoStoredMessagesSaysNotKept(t *testing.T) {
	files, m := exportOne(t, builderWith(fakeSource{convID: Held{Turns: fullHeld().Turns}}, nil), Options{})
	if p := part(m, FileMoot); p.Status != StatusNotKept || p.Count != 0 || p.Reason == "" {
		t.Errorf("discussion part = %+v", p)
	}
	if _, ok := files["discussion.md"]; !ok {
		t.Error("an empty discussion is still a file, so the zip's layout does not vary")
	}
}

func TestTheDiscussionStopsAtTheSizeBoundAndSaysSo(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.MaxDiscussionBytes = 70 // three of the six messages
	files, m := exportOne(t, b, Options{})
	conv := decodeConversation(t, files["conversation.json"])
	total := len(conv.Turns[0].Messages) + len(conv.Turns[1].Messages)
	if total >= 6 || total == 0 {
		t.Fatalf("exported %d messages, want some but not all", total)
	}
	p := part(m, FileMoot)
	if p.Status != StatusIncluded || !strings.Contains(p.Reason, "size limit") {
		t.Errorf("discussion part = %+v", p)
	}
	var listed bool
	for _, l := range m.LeftOut {
		listed = listed || l.Key == "discussion/size-limit"
	}
	if !listed {
		t.Errorf("leftOut = %+v lacks the size limit", m.LeftOut)
	}
}

func TestTheDefaultSizeBoundIsUsedWhenNoneIsSet(t *testing.T) {
	held := fullHeld()
	held.Messages[thread1][0].Content = strings.Repeat("x", DefaultMaxDiscussionBytes+1)
	_, m := exportOne(t, builderWith(fakeSource{convID: held}, nil), Options{})
	if p := part(m, FileMoot); !strings.Contains(p.Reason, "size limit") {
		t.Errorf("discussion part = %+v, want the default bound applied", p)
	}
}

func TestTheDiscussionOwnershipCheckIsUnchanged(t *testing.T) {
	b := builderWith(fakeSource{convID: fullHeld()}, nil)
	b.Allow = func(context.Context, crewscope.Scope, string) error { return ErrRefused }
	var buf strings.Builder
	_, err := b.WriteConversation(context.Background(), &buf, testScope, convID, Options{})
	if !errors.Is(err, ErrRefused) || buf.Len() != 0 {
		t.Errorf("err = %v, wrote %d bytes; a refused caller gets no discussion", err, buf.Len())
	}
	if _, err := b.ConversationManifest(context.Background(), testScope, convID, Options{}); !errors.Is(err, ErrRefused) {
		t.Errorf("manifest err = %v, want ErrRefused", err)
	}
}
