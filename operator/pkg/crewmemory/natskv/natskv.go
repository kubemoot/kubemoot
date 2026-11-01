/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package natskv opens the crew-memory bucket of a NATS server for reading. The
// reader it returns can list keys and get entries and nothing else, so a tool
// that exports memory cannot change a fact. An unreachable server or a missing
// bucket is an error; only a bucket with no keys is an empty result.
package natskv

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
)

// Timeout bounds each call to the server.
const Timeout = 30 * time.Second

// Open binds the crew-memory bucket on an existing connection. It never creates
// the bucket.
func Open(nc *nats.Conn) (crewmemory.EntryReader, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	kv, err := js.KeyValue(ctx, crewmemory.Bucket)
	if err != nil {
		return nil, fmt.Errorf("open bucket %s: %w", crewmemory.Bucket, err)
	}
	return New(kv), nil
}

// New adapts a key-value bucket to the read-only crewmemory.EntryReader. Each
// call gets its own timeout.
func New(kv jetstream.KeyValue) crewmemory.EntryReader {
	return &reader{kv: kv}
}

type reader struct {
	kv jetstream.KeyValue
}

func (r *reader) ListKVKeys(string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	keys, err := r.kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return nil, nil
	}
	return keys, err
}

// GetKVEntry returns an empty entry for a key that is gone, which the fact
// parser reports as an entry with no value.
func (r *reader) GetKVEntry(_, key string) (crewmemory.Entry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	e, err := r.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return crewmemory.Entry{}, nil
		}
		return crewmemory.Entry{}, err
	}
	return crewmemory.Entry{Value: e.Value(), Revision: e.Revision(), Created: e.Created()}, nil
}
