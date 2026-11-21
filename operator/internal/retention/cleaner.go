/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package retention

import (
	"errors"
	"fmt"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/kubemoot/kubemoot/operator/pkg/crewscope"
)

// The shared stores a crew's data lives in.
const (
	ConversationsBucket = bus.ConversationsBucket
	MemoryBucket        = crewmemory.Bucket
	DiscussStream       = "KUBEMOOT_DISCUSS"
	CoordinatorStream   = "KUBEMOOT_COORDINATOR"
	RequestStream       = "KUBEMOOT_REQUEST"
)

// KVEntry is one key of a bucket and when it was last written.
type KVEntry struct {
	Key     string
	Written time.Time
}

// Store is the part of NATS the cleanup uses.
type Store interface {
	// KVEntries lists the keys that begin with prefix, with the time each was last written.
	KVEntries(bucket, prefix string) ([]KVEntry, error)
	// DeleteKVKeys removes exact keys.
	DeleteKVKeys(bucket string, keys []string) error
	// PurgeStreamBefore removes the messages on a subject filter written before the time,
	// and returns how many it removed.
	PurgeStreamBefore(stream, subject string, before time.Time) (int, error)
}

// Removed counts what one run removed.
type Removed struct {
	Conversations int
	Memory        int
	Other         int
}

// Cleaner removes what a crew's policy says has expired.
type Cleaner struct {
	Store Store
	Now   func() time.Time
}

// Run removes the crew's expired conversations, facts and messages. Each kind is tried
// even when another fails; every failure is returned together with what was removed.
// Only keys under the crew's own prefix and subjects are touched.
func (c *Cleaner) Run(scope crewscope.Scope, p Policy) (Removed, error) {
	var out Removed
	var errs []error
	var err error
	now := c.Now()

	if p.Conversations > 0 {
		cutoff := now.Add(-p.Conversations)
		out.Conversations, err = c.expireKV(ConversationsBucket, scope.ConversationPrefix(), cutoff)
		errs = appendErr(errs, err)
		// A conversation's owner record expires with its history; its removal is not counted
		// as a conversation, since it is the same one.
		_, err = c.expireKV(ConversationsBucket, bus.ConversationOwnerPrefix(scope.Namespace, scope.Crew), cutoff)
		errs = appendErr(errs, err)
	}
	if !p.Memory.IsNever() && p.Memory.IsSet() {
		out.Memory, err = c.expireKV(MemoryBucket, scope.MemoryPrefix(), now.Add(-p.Memory.Value()))
		errs = appendErr(errs, err)
	}
	if p.Other > 0 {
		out.Other, err = c.expireStreams(scope, now.Add(-p.Other))
		errs = appendErr(errs, err)
	}
	return out, errors.Join(errs...)
}

func appendErr(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}

// expireKV deletes every key under the prefix last written before the cutoff.
func (c *Cleaner) expireKV(bucket, prefix string, cutoff time.Time) (int, error) {
	entries, err := c.Store.KVEntries(bucket, prefix)
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", bucket, err)
	}
	var expired []string
	for _, e := range entries {
		if e.Written.Before(cutoff) {
			expired = append(expired, e.Key)
		}
	}
	if len(expired) == 0 {
		return 0, nil
	}
	if err := c.Store.DeleteKVKeys(bucket, expired); err != nil {
		return 0, fmt.Errorf("delete from %s: %w", bucket, err)
	}
	return len(expired), nil
}

// expireStreams removes the crew's messages written before the cutoff from each stream
// that carries them.
func (c *Cleaner) expireStreams(scope crewscope.Scope, cutoff time.Time) (int, error) {
	targets := []struct{ stream, subject string }{
		{DiscussStream, scope.DiscussFilter()},
		{CoordinatorStream, scope.CoordinatorFilter()},
		{RequestStream, scope.RequestSubject()},
	}
	total := 0
	var errs []error
	for _, t := range targets {
		n, err := c.Store.PurgeStreamBefore(t.stream, t.subject, cutoff)
		if err != nil {
			errs = append(errs, fmt.Errorf("purge %s: %w", t.stream, err))
			continue
		}
		total += n
	}
	return total, errors.Join(errs...)
}
