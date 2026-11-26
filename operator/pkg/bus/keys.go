/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// PerformanceBucket is the NATS KV bucket holding SchedulerPerformance entries,
// one per server, model, and agent.
const PerformanceBucket = "kubemoot_scheduler_performance"

// PerformanceKey is the key of one agent's entry for a model on a server.
func PerformanceKey(server, model, agent string) string {
	return KeyToken(server) + "." + KeyToken(model) + "." + KeyToken(agent)
}

// PerformanceKeyPrefix starts the key of every entry about the server.
func PerformanceKeyPrefix(server string) string { return KeyToken(server) + "." }

// ConversationsBucket is the NATS KV bucket holding each conversation's history and owner.
const ConversationsBucket = "kubemoot_conversations"

// ConversationOwnerKey is the key of a conversation's owner record in ConversationsBucket:
// own.<namespace>.<crew>.<conversationId>. The history of the same conversation is under
// conv.<namespace>.<crew>.<conversationId>, so the two expire together.
func ConversationOwnerKey(namespace, crew, conversationID string) string {
	return ConversationOwnerPrefix(namespace, crew) + conversationID
}

// ConversationOwnerPrefix starts the key of every owner record of a crew.
func ConversationOwnerPrefix(namespace, crew string) string {
	return "own." + namespace + "." + crew + "."
}

// DecodeConversationOwner reads an owner record's value. ok is false when the value cannot
// be read: such a record owns nothing, so the conversation is the operator's. Every reader
// of owner records decodes through here.
func DecodeConversationOwner(value []byte) (owner Principal, ok bool) {
	var rec ConversationOwner
	if err := json.Unmarshal(value, &rec); err != nil || rec.Owner.Kind == "" {
		return Principal{}, false
	}
	return rec.Owner, true
}

// ConversationNeedsKey is the key of the record of the connected accounts a conversation's
// latest turn was refused for, in ConversationsBucket: needs.<namespace>.<crew>.<conversationId>.
// The gateway's delete of a conversation removes it with the history and the owner record.
func ConversationNeedsKey(namespace, crew, conversationID string) string {
	return "needs." + namespace + "." + crew + "." + conversationID
}

// NeedsAccounts is the value of a ConversationNeedsKey record: for each account name, when a
// tool call was last refused for it, RFC 3339 with fractional seconds in UTC. An account belongs
// to the turn that was running at that time, so a turn that began later ignores it.
type NeedsAccounts struct {
	Refused map[string]string `json:"refused"`
}

// ConversationHistoryKey is the key of a conversation's history in ConversationsBucket.
func ConversationHistoryKey(namespace, crew, conversationID string) string {
	return ConversationHistoryPrefix(namespace, crew) + conversationID
}

// ConversationHistoryPrefix starts the key of every conversation history of a crew. It is
// the one place the prefix is spelled: crewscope's ConversationPrefix returns it.
func ConversationHistoryPrefix(namespace, crew string) string {
	return "conv." + namespace + "." + crew + "."
}

// KeyToken encodes a free-form name (a model such as qwen3:14b, an agent id) into a
// NATS KV key token exactly as the agent runtime's ModelKeys.token does: letters,
// digits and '-' pass through; any other UTF-16 code unit becomes '_' and two hex
// digits, or '__' and four above U+00FF. So qwen3:14b becomes qwen3_3a14b. It walks
// UTF-16 code units, as Java does, so a character outside the Basic Multilingual
// Plane encodes as its two surrogates in both runtimes.
func KeyToken(name string) string {
	if name == "" {
		return "_"
	}
	var b strings.Builder
	for _, u := range utf16.Encode([]rune(name)) {
		switch {
		case u < 128 && (isLetterOrDigit(u) || u == '-'):
			b.WriteByte(byte(u))
		case u <= 0xff:
			fmt.Fprintf(&b, "_%02x", u)
		default:
			fmt.Fprintf(&b, "__%04x", u)
		}
	}
	return b.String()
}

// ModelLifecycleSubjectPrefix starts the NATS subject an agent sends a
// ModelLifecycleRequest to; the server's token follows.
const ModelLifecycleSubjectPrefix = "kubemoot.server.model."

// ModelLifecycleSubject is the subject of requests to make a model ready on, or
// release it from, the server. The token is escaped as KeyToken does, which the
// agent runtime's ModelKeys.token matches, so a server name with a dot stays one token.
func ModelLifecycleSubject(server string) string {
	return ModelLifecycleSubjectPrefix + KeyToken(server)
}

func isLetterOrDigit(u uint16) bool {
	return (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z') || (u >= '0' && u <= '9')
}
