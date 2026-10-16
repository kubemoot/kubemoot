/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package store reads and writes the scheduler's NATS KV buckets in the formats
// the bus contract defines (operator/pkg/bus): call tickets on a provider
// (ProviderTicket) and the performance history (SchedulerPerformance). The agent
// runtime writes the same formats (TicketManager, PerformanceHistory).
package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// KV is the part of the NATS KV client the store uses.
type KV interface {
	GetKVValue(bucket, key string) ([]byte, error)
	PutKVValue(bucket, key string, value []byte) error
	ListKVKeys(bucket string) ([]string, error)
	DeleteKVKey(bucket, key string) error
}

// Buckets the agent runtime reads; the operator's streams job creates them.
const (
	TicketsBucket     = "kubemoot_provider_tickets"
	PerformanceBucket = "kubemoot_scheduler_performance"
)

// Tickets claims and releases call tickets on a provider (key <provider>__<ticketId>),
// so agents count these calls against the provider's slots and memory.
type Tickets struct {
	KV     KV
	Holder string
	Now    func() time.Time
}

// othersOn lists the tickets on the provider held by anyone but this holder. A
// ticket that cannot be read is an error; one that cannot be parsed counts as
// someone else's, so an odd entry never makes a busy provider look idle.
func (t Tickets) othersOn(provider string) ([]bus.ProviderTicket, error) {
	keys, err := t.KV.ListKVKeys(TicketsBucket)
	if err != nil {
		return nil, err
	}
	var out []bus.ProviderTicket
	for _, key := range keys {
		if !strings.HasPrefix(key, provider+"__") {
			continue
		}
		raw, err := t.KV.GetKVValue(TicketsBucket, key)
		if err != nil {
			return nil, fmt.Errorf("reading ticket %s: %w", key, err)
		}
		if raw == nil {
			continue // released since the listing
		}
		var ticket bus.ProviderTicket
		if json.Unmarshal(raw, &ticket) != nil {
			out = append(out, bus.ProviderTicket{Provider: provider})
			continue
		}
		if ticket.HolderID != t.Holder {
			out = append(out, ticket)
		}
	}
	return out, nil
}

// Others counts the tickets on the provider held by anyone but this holder.
func (t Tickets) Others(provider string) (int, error) {
	others, err := t.othersOn(provider)
	return len(others), err
}

// ModelsInUse is the set of models other holders' tickets on the provider name.
func (t Tickets) ModelsInUse(provider string) (map[string]bool, error) {
	others, err := t.othersOn(provider)
	if err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	for _, o := range others {
		if o.ModelName != "" {
			inUse[o.ModelName] = true
		}
	}
	return inUse, nil
}

// Claim writes a ticket for one request of model on provider and returns its key.
func (t Tickets) Claim(provider, model string, footprintMiB int64) (string, error) {
	ticket := bus.ProviderTicket{
		TicketID:          uuid.NewString(),
		Provider:          provider,
		ModelName:         model,
		ModelFootprintMiB: footprintMiB,
		HolderID:          t.Holder,
		ClaimedAt:         t.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(ticket)
	if err != nil {
		return "", err
	}
	key := provider + "__" + ticket.TicketID
	return key, t.KV.PutKVValue(TicketsBucket, key, raw)
}

// Release deletes a ticket; the bucket TTL removes it if the delete fails.
func (t Tickets) Release(key string) {
	_ = t.KV.DeleteKVKey(TicketsBucket, key)
}

// MaxSamples matches the agent runtime's bound per performance-history entry.
const MaxSamples = 32

// Unknown is an engine timing that was not reported, as the agent runtime writes it.
const Unknown int64 = -1

// KindCalibration marks a sample as a calibration request, not a crew call.
const KindCalibration = bus.SchedulerPerformanceSamplesElemKindCalibration

// Sample is one request in the performance history.
type Sample = bus.SchedulerPerformanceSamplesElem

// History appends samples to the scheduler's performance history under this
// holder's own key, <provider>.<model>.<agent>; one writer per key, as in the agent
// runtime, so the read-modify-write needs no compare-and-set.
type History struct {
	KV    KV
	Agent string
	Now   func() time.Time
}

// Record adds samples to the entry for provider and model, keeping the latest
// MaxSamples. A stored entry that cannot be read is an error, so a stored history
// is never replaced by an empty one; an entry that cannot be parsed is rebuilt.
func (h History) Record(provider, model string, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	key := Token(provider) + "." + Token(model) + "." + Token(h.Agent)
	raw, err := h.KV.GetKVValue(PerformanceBucket, key)
	if err != nil {
		return fmt.Errorf("reading %s: %w", key, err)
	}
	entry := bus.SchedulerPerformance{}
	if raw == nil || json.Unmarshal(raw, &entry) != nil {
		entry = bus.SchedulerPerformance{}
	}
	entry.Provider, entry.Model, entry.Agent = provider, model, h.Agent
	for _, s := range samples {
		entry.Requests++
		if !s.Ok {
			entry.Failures++
		}
	}
	entry.Samples = append(entry.Samples, samples...)
	if len(entry.Samples) > MaxSamples {
		entry.Samples = entry.Samples[len(entry.Samples)-MaxSamples:]
	}
	if entry.Waits == nil {
		entry.Waits = []bus.SchedulerPerformanceWaitsElem{}
	}
	entry.UpdatedAt = h.Now().UnixMilli()
	out, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return h.KV.PutKVValue(PerformanceBucket, key, out)
}

// Forget deletes every agent's performance history for the provider.
func (h History) Forget(provider string) error {
	keys, err := h.KV.ListKVKeys(PerformanceBucket)
	if err != nil {
		return err
	}
	prefix := Token(provider) + "."
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			if err := h.KV.DeleteKVKey(PerformanceBucket, key); err != nil {
				return err
			}
		}
	}
	return nil
}
