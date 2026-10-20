/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package store

import (
	"errors"
	"sort"
	"strings"

	"github.com/nats-io/nats.go"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// NATS is the KV over a JetStream connection. A bucket that does not exist yet, or a
// server that cannot be reached, comes back as an error that wraps bus.ErrNotReady,
// so callers can wait for the bus instead of reporting a failure.
type NATS struct {
	JS nats.JetStreamContext
}

// GetKVValue returns the value, or nil when the key does not exist.
func (n NATS) GetKVValue(bucket, key string) ([]byte, error) {
	kv, err := n.JS.KeyValue(bucket)
	if err != nil {
		return nil, bus.AsNotReady(err)
	}
	entry, err := kv.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, bus.AsNotReady(err)
	}
	return entry.Value(), nil
}

// PutKVValue writes the value.
func (n NATS) PutKVValue(bucket, key string, value []byte) error {
	kv, err := n.JS.KeyValue(bucket)
	if err != nil {
		return bus.AsNotReady(err)
	}
	_, err = kv.Put(key, value)
	return bus.AsNotReady(err)
}

// ListKVKeys lists the bucket's keys from the backing stream's subject index,
// as the agent runtime does: KeyValue.Keys creates a consumer on every call, which
// churns and can collide with another process's consumer.
func (n NATS) ListKVKeys(bucket string) ([]string, error) {
	prefix := "$KV." + bucket + "."
	info, err := n.JS.StreamInfo("KV_"+bucket, &nats.StreamInfoRequest{SubjectsFilter: prefix + ">"})
	if err != nil {
		return nil, bus.AsNotReady(err)
	}
	keys := make([]string, 0, len(info.State.Subjects))
	for subject := range info.State.Subjects {
		keys = append(keys, strings.TrimPrefix(subject, prefix))
	}
	sort.Strings(keys)
	return keys, nil
}

// DeleteKVKey deletes the key.
func (n NATS) DeleteKVKey(bucket, key string) error {
	kv, err := n.JS.KeyValue(bucket)
	if err != nil {
		return bus.AsNotReady(err)
	}
	return bus.AsNotReady(kv.Delete(key))
}
