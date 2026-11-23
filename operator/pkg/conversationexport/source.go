/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package conversationexport assembles everything Kubemoot still holds about one
// conversation into a zip, or several conversations into a zip of zips. It is the
// access and portability half of the data principle: Kubemoot keeps little, and what
// it keeps the person can take away. The zip is built when it is asked for, written
// to the caller as it is built, and never stored.
//
// The builder reads through a Source and a crew-memory reader, so the operator's
// download endpoint and any other tool share one implementation of the zip.
package conversationexport

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// ErrNotHeld means no store holds the conversation any more: it expired, or it never
// existed in this crew. The two cannot be told apart, and the export does not try.
var ErrNotHeld = errors.New("the conversation is not held: it expired or does not exist in this crew")

// ErrRefused means the caller may not export the conversation.
var ErrRefused = errors.New("not allowed to export this conversation")

// Turn is one question and its answer.
type Turn struct {
	ThreadID string
	Question string
	Answer   string
	// AnsweredAt is when the answer was given, UTC; zero when the store did not record it.
	AnsweredAt time.Time
}

// Message is one message of a turn's discussion thread, as the discussion stream held it.
type Message struct {
	ID string
	// Agent is who sent it: an agent's name, human, scheduler or discussion-gateway.
	Agent string
	// Signal is the message type: agree, concern, advisory, synthesis and so on.
	Signal  string
	Channel string
	Content string
	// At is when it was published, UTC; zero when the message carried no time.
	At time.Time
}

// File is a stored file of a thread's workspace.
type File struct {
	Thread string
	// Name is the object's name below the thread, <agent>/<object>.
	Name string
	// Open reads the file's bytes.
	Open func(ctx context.Context) (io.ReadCloser, error)
}

// Held is what the stores still hold about one conversation.
type Held struct {
	// Turns are the conversation's turns, oldest first.
	Turns []Turn
	// Receipts is the receipt per thread, as the asker's stream carried it. A thread
	// whose receipt is no longer stored has no entry.
	Receipts map[string][]byte
	// Messages is the discussion of each thread, every message in the order the stream
	// held it. A thread the stream no longer holds has no entry.
	Messages map[string][]Message
	// Files are the workspace files of the conversation's threads that are still stored.
	Files []File
}

// Source reads what the stores hold about a conversation of a crew. It returns
// ErrNotHeld when no store holds the conversation, and any other error when a store
// could not be read.
type Source interface {
	Held(ctx context.Context, scope crewscope.Scope, id string) (Held, error)
}
