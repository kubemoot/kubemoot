/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package bus

import (
	"strings"
	"testing"
)

func TestKeyToken_matchesTheAgentRuntimeEncoding(t *testing.T) {
	cases := map[string]string{
		"qwen3:14b":       "qwen3_3a14b",
		"crew-ns/agent-a": "crew-ns_2fagent-a",
		"":                "_",
		"model.v2":        "model_2ev2",
		"caf\u00e9":       "caf_e9",
		"\u4e2d":          "__4e2d",
		// Outside the Basic Multilingual Plane: two UTF-16 surrogates, as Java iterates chars.
		"\U0001F600": "__d83d__de00",
	}
	for in, want := range cases {
		if got := KeyToken(in); got != want {
			t.Errorf("KeyToken(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPerformanceKey(t *testing.T) {
	key := PerformanceKey("gpu-a", "qwen3:8b", "crew/a")
	if key != "gpu-a.qwen3_3a8b.crew_2fa" {
		t.Errorf("key: %q", key)
	}
	if !strings.HasPrefix(key, PerformanceKeyPrefix("gpu-a")) || strings.HasPrefix(key, PerformanceKeyPrefix("gpu")) {
		t.Errorf("prefix does not pick out exactly the server's entries: %q", key)
	}
}

func TestConversationKeys(t *testing.T) {
	if got := ConversationOwnerKey("ns", "crew", "c1"); got != "own.ns.crew.c1" {
		t.Errorf("owner key %q", got)
	}
	if got := ConversationHistoryKey("ns", "crew", "c1"); got != "conv.ns.crew.c1" {
		t.Errorf("history key %q", got)
	}
	// The owner prefix of one crew never matches another crew's or namespace's records.
	prefix := ConversationOwnerPrefix("ns", "crew")
	for _, other := range []string{ConversationOwnerKey("ns", "crewx", "c"), ConversationOwnerKey("ns2", "crew", "c"), ConversationHistoryKey("ns", "crew", "c")} {
		if strings.HasPrefix(other, prefix) {
			t.Errorf("%s matches %s", other, prefix)
		}
	}
	if !strings.HasPrefix(ConversationOwnerKey("ns", "crew", "c"), prefix) {
		t.Error("an owner key starts with its crew's prefix")
	}
}
