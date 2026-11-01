/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package coordchaos

import (
	"sort"
	"sync"
)

// Store collects what the watchers read: the coordinator log events and the discussion
// messages of one crew. It is safe for concurrent use. A stream sequence is kept once,
// so a reconnecting consumer that delivers a message again changes nothing.
type Store struct {
	mu     sync.RWMutex
	events []Event
	msgs   []Msg
	evSeq  map[uint64]bool
	msgSeq map[uint64]bool
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{evSeq: map[uint64]bool{}, msgSeq: map[uint64]bool{}}
}

// AddEvent records a coordinator log event.
func (s *Store) AddEvent(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Seq != 0 && s.evSeq[e.Seq] {
		return
	}
	s.evSeq[e.Seq] = true
	s.events = append(s.events, e)
}

// AddMsg records a discussion message.
func (s *Store) AddMsg(m Msg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.Seq != 0 && s.msgSeq[m.Seq] {
		return
	}
	s.msgSeq[m.Seq] = true
	s.msgs = append(s.msgs, m)
}

// Counts returns how many events and messages the store holds.
func (s *Store) Counts() (events, msgs int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.events), len(s.msgs)
}

// Thread returns what the store holds about the thread, in stream order.
func (s *Store) Thread(id string) Thread {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := Thread{ID: id}
	for i := range s.events {
		if s.events[i].ThreadID == id {
			t.Events = append(t.Events, s.events[i])
		}
	}
	for i := range s.msgs {
		if s.msgs[i].ThreadID == id {
			t.Msgs = append(t.Msgs, s.msgs[i])
		}
	}
	sort.SliceStable(t.Events, func(a, b int) bool { return t.Events[a].Seq < t.Events[b].Seq })
	sort.SliceStable(t.Msgs, func(a, b int) bool { return t.Msgs[a].Seq < t.Msgs[b].Seq })
	return t
}

// ThreadsFor returns the ids of the threads started for the conversation, oldest first.
// A thread belongs to a conversation by its thread_start message (which names the
// conversation) or by its thread_opened event.
func (s *Store) ThreadsFor(conversationID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]uint64{}
	for i := range s.msgs {
		m := s.msgs[i]
		if m.MessageType == MsgThreadStart && m.ConversationID == conversationID {
			noteFirst(seen, m.ThreadID, m.Seq)
		}
	}
	for i := range s.events {
		e := s.events[i]
		if e.Type == EvThreadOpened && e.Data.ConversationID == conversationID {
			noteFirst(seen, e.ThreadID, e.Seq)
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return seen[ids[a]] < seen[ids[b]] })
	return ids
}

func noteFirst(m map[string]uint64, id string, seq uint64) {
	if _, ok := m[id]; !ok {
		m[id] = seq
	}
}

// OpenThreads returns the ids of the threads in the log that opened and never logged
// thread_closed, among those whose id the filter keeps (nil keeps all).
func (s *Store) OpenThreads(keep func(id string) bool) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	opened := map[string]bool{}
	closed := map[string]bool{}
	for i := range s.events {
		switch s.events[i].Type {
		case EvThreadOpened:
			opened[s.events[i].ThreadID] = true
		case EvThreadClosed:
			closed[s.events[i].ThreadID] = true
		}
	}
	var open []string
	for id := range opened {
		if !closed[id] && (keep == nil || keep(id)) {
			open = append(open, id)
		}
	}
	sort.Strings(open)
	return open
}
