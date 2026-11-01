/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package natskv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// fakeKV implements only what the reader calls; any other method panics on the
// embedded nil interface, which would show the reader reaching past Keys and Get.
type fakeKV struct {
	jetstream.KeyValue
	keys    []string
	keysErr error
	entries map[string]jetstream.KeyValueEntry
	getErr  error
}

func (f fakeKV) Keys(context.Context, ...jetstream.WatchOpt) ([]string, error) {
	return f.keys, f.keysErr
}

func (f fakeKV) Get(_ context.Context, key string) (jetstream.KeyValueEntry, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	e, ok := f.entries[key]
	if !ok {
		return nil, jetstream.ErrKeyNotFound
	}
	return e, nil
}

type fakeEntry struct {
	jetstream.KeyValueEntry
	value []byte
	rev   uint64
	at    time.Time
}

func (e fakeEntry) Value() []byte      { return e.value }
func (e fakeEntry) Revision() uint64   { return e.rev }
func (e fakeEntry) Created() time.Time { return e.at }

func TestListKeysReturnsTheBucketKeys(t *testing.T) {
	keys, err := New(fakeKV{keys: []string{"ns.c.t.k"}}).ListKVKeys("any")
	if err != nil || len(keys) != 1 || keys[0] != "ns.c.t.k" {
		t.Errorf("ListKVKeys() = %v, %v", keys, err)
	}
}

func TestListKeysOfAnEmptyBucketIsEmptyNotAnError(t *testing.T) {
	keys, err := New(fakeKV{keysErr: jetstream.ErrNoKeysFound}).ListKVKeys("any")
	if err != nil || len(keys) != 0 {
		t.Errorf("ListKVKeys() = %v, %v, want no keys and no error", keys, err)
	}
}

func TestListKeysReportsAServerError(t *testing.T) {
	boom := errors.New("server went away")
	if _, err := New(fakeKV{keysErr: boom}).ListKVKeys("any"); !errors.Is(err, boom) {
		t.Errorf("ListKVKeys() error = %v, want the server error", err)
	}
}

func TestGetReturnsValueRevisionAndTime(t *testing.T) {
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	kv := fakeKV{entries: map[string]jetstream.KeyValueEntry{"k": fakeEntry{value: []byte("v"), rev: 7, at: at}}}
	e, err := New(kv).GetKVEntry("any", "k")
	if err != nil || string(e.Value) != "v" || e.Revision != 7 || !e.Created.Equal(at) {
		t.Errorf("GetKVEntry() = %+v, %v", e, err)
	}
}

func TestGetOfAGoneKeyIsAnEmptyEntry(t *testing.T) {
	e, err := New(fakeKV{}).GetKVEntry("any", "missing")
	if err != nil || len(e.Value) != 0 {
		t.Errorf("GetKVEntry() = %+v, %v, want an empty entry", e, err)
	}
}

func TestGetReportsAServerError(t *testing.T) {
	boom := errors.New("timeout")
	if _, err := New(fakeKV{getErr: boom}).GetKVEntry("any", "k"); !errors.Is(err, boom) {
		t.Errorf("GetKVEntry() error = %v, want the server error", err)
	}
}
