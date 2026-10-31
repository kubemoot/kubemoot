/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package crewmemory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

// Fact is one crew-memory entry as the agent runtime stores it: the key
// <ns>.<crew>.<topic>.<key> and the JSON value {value, learnedBy, learnedAt, usedAt}.
type Fact struct {
	Scope     crewscope.Scope
	Topic     string
	Key       string
	Value     string
	LearnedBy string
	// LearnedAt and UsedAt are zero when the stored text is missing or not RFC 3339.
	LearnedAt time.Time
	UsedAt    time.Time
}

// Reader is the read-only subset of the NATS key-value store the export uses.
type Reader interface {
	ListKVKeys(bucket string) ([]string, error)
	GetKVValue(bucket, key string) ([]byte, error)
}

// storedFact is the JSON value the agent runtime writes.
type storedFact struct {
	Value     string `json:"value"`
	LearnedBy string `json:"learnedBy"`
	LearnedAt string `json:"learnedAt"`
	UsedAt    string `json:"usedAt"`
}

// ReadFacts returns every readable fact of one crew, ordered by topic then key.
// Keys of other crews, keys that do not parse, and values that are not the
// expected JSON are skipped, never fatal: one bad entry must not hide the rest.
func ReadFacts(r Reader, scope crewscope.Scope) ([]Fact, error) {
	keys, err := r.ListKVKeys(Bucket)
	if err != nil {
		return nil, fmt.Errorf("list %s keys: %w", Bucket, err)
	}
	var facts []Fact
	for _, key := range keys {
		if !strings.HasPrefix(key, scope.MemoryPrefix()) {
			continue
		}
		raw, err := r.GetKVValue(Bucket, key)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", key, err)
		}
		if f, ok := ParseFact(scope, key, raw); ok {
			facts = append(facts, f)
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Topic != facts[j].Topic {
			return facts[i].Topic < facts[j].Topic
		}
		return facts[i].Key < facts[j].Key
	})
	return facts, nil
}

// ParseFact decodes one KV entry. It reports false for a missing value, a key
// outside the scope, or a value that is not JSON.
func ParseFact(scope crewscope.Scope, key string, raw []byte) (Fact, bool) {
	mk, err := crewscope.ParseMemoryKey(key)
	if err != nil || mk.Scope != scope || len(raw) == 0 {
		return Fact{}, false
	}
	topic, name, ok := strings.Cut(mk.Rest, ".")
	if !ok || topic == "" || name == "" {
		return Fact{}, false
	}
	var s storedFact
	if err := json.Unmarshal(raw, &s); err != nil {
		return Fact{}, false
	}
	return Fact{
		Scope:     scope,
		Topic:     topic,
		Key:       name,
		Value:     s.Value,
		LearnedBy: s.LearnedBy,
		LearnedAt: parseTime(s.LearnedAt),
		// usedAt defaults to learnedAt for entries written before touch-on-read.
		UsedAt: parseTime(firstNonEmpty(s.UsedAt, s.LearnedAt)),
	}, true
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
