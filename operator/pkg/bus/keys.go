/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

import (
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
