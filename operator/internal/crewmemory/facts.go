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
	"errors"
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
	// Revision and Recorded come from the key-value store, not from the stored
	// text: the revision number of the current value and when it was written.
	// They are zero when the reader does not know them.
	Revision uint64
	Recorded time.Time
}

// Entry is what the store returns for one key: the stored bytes and the
// store's own record of the revision. Value is empty when the key is gone.
type Entry struct {
	Value    []byte
	Revision uint64
	Created  time.Time
}

// Reader is the read-only subset of the NATS key-value store: list the keys
// and get one value.
type Reader interface {
	ListKVKeys(bucket string) ([]string, error)
	GetKVValue(bucket, key string) ([]byte, error)
}

// EntryReader is the read-only subset the export uses: list the keys and get
// one entry with the store's revision and time. It offers no way to write.
type EntryReader interface {
	ListKVKeys(bucket string) ([]string, error)
	GetKVEntry(bucket, key string) (Entry, error)
}

// storedFact is the JSON value the agent runtime writes.
type storedFact struct {
	Value     string `json:"value"`
	LearnedBy string `json:"learnedBy"`
	LearnedAt string `json:"learnedAt"`
	UsedAt    string `json:"usedAt"`
}

// Skipped names an entry that was read but could not be turned into a fact,
// and why. A skipped entry is reported, never dropped without a trace.
type Skipped struct {
	Key    string
	Reason string
}

// ReadResult is what ReadFacts found: the facts in order and the entries it skipped.
type ReadResult struct {
	Facts   []Fact
	Skipped []Skipped
}

// ErrOutOfScope reports a key that belongs to another crew or is not a memory key.
var ErrOutOfScope = errors.New("key is not in the crew's scope")

// ReadFacts returns every readable fact of one crew, ordered by topic then key.
// Keys of other crews are ignored. An entry that does not parse is listed in
// Skipped and never fatal: one bad entry must not hide the rest. A store error
// is fatal, so an unreachable server is never mistaken for "no facts".
func ReadFacts(r EntryReader, scope crewscope.Scope) (ReadResult, error) {
	keys, err := r.ListKVKeys(Bucket)
	if err != nil {
		return ReadResult{}, fmt.Errorf("list %s keys: %w", Bucket, err)
	}
	var res ReadResult
	for _, key := range keys {
		if !strings.HasPrefix(key, scope.MemoryPrefix()) {
			continue
		}
		entry, err := r.GetKVEntry(Bucket, key)
		if err != nil {
			return ReadResult{}, fmt.Errorf("read %s: %w", key, err)
		}
		f, perr := ParseFact(scope, key, entry)
		res.add(key, f, perr)
	}
	SortFacts(res.Facts)
	return res, nil
}

func (r *ReadResult) add(key string, f Fact, err error) {
	switch {
	case errors.Is(err, ErrOutOfScope):
	case err != nil:
		r.Skipped = append(r.Skipped, Skipped{Key: key, Reason: err.Error()})
	default:
		r.Facts = append(r.Facts, f)
	}
}

// StoreKey is the fact's key in the bucket: <namespace>.<crew>.<topic>.<key>.
func (f Fact) StoreKey() string {
	return f.Scope.Namespace + "." + f.Scope.Crew + "." + f.Topic + "." + f.Key
}

// SortFacts orders facts by topic, then key.
func SortFacts(facts []Fact) {
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Topic != facts[j].Topic {
			return facts[i].Topic < facts[j].Topic
		}
		return facts[i].Key < facts[j].Key
	})
}

// ParseFact decodes one KV entry. It returns ErrOutOfScope for a key outside
// the scope, and another error for a missing value or a value that is not JSON.
func ParseFact(scope crewscope.Scope, key string, entry Entry) (Fact, error) {
	mk, err := crewscope.ParseMemoryKey(key)
	if err != nil || mk.Scope != scope {
		return Fact{}, ErrOutOfScope
	}
	topic, name, ok := strings.Cut(mk.Rest, ".")
	if !ok || topic == "" || name == "" {
		return Fact{}, errors.New("key has no topic and name")
	}
	if len(entry.Value) == 0 {
		return Fact{}, errors.New("entry has no value")
	}
	var s storedFact
	if err := json.Unmarshal(entry.Value, &s); err != nil {
		return Fact{}, errors.New("value is not the expected JSON")
	}
	return Fact{
		Scope:     scope,
		Topic:     topic,
		Key:       name,
		Value:     s.Value,
		LearnedBy: s.LearnedBy,
		LearnedAt: parseTime(s.LearnedAt),
		// usedAt defaults to learnedAt for entries written before touch-on-read.
		UsedAt:   parseTime(firstNonEmpty(s.UsedAt, s.LearnedAt)),
		Revision: entry.Revision,
		Recorded: entry.Created.UTC(),
	}, nil
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
