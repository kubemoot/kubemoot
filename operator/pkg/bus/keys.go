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
// one per provider, model, and agent.
const PerformanceBucket = "kubemoot_scheduler_performance"

// PerformanceKey is the key of one agent's entry for a model on a provider.
func PerformanceKey(provider, model, agent string) string {
	return KeyToken(provider) + "." + KeyToken(model) + "." + KeyToken(agent)
}

// PerformanceKeyPrefix starts the key of every entry about the provider.
func PerformanceKeyPrefix(provider string) string { return KeyToken(provider) + "." }

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

func isLetterOrDigit(u uint16) bool {
	return (u >= 'a' && u <= 'z') || (u >= 'A' && u <= 'Z') || (u >= '0' && u <= '9')
}
