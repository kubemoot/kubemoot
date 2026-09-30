/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package scheduler implements the operator-side poller that fires
// scheduled reminders and follow-ups written into the NATS KV bucket
// `kubemoot_scheduled` by the `scheduling-mcp` server (and any other
// writer, e.g. a Kubernetes CronJob).
//
// The record schema is duplicated in two Go files — one here at
// internal/scheduler/record/, one at kubemoot/scheduling-mcp/pkg/record/.
// The duplication is intentional: per-module Kaniko builds can't share
// source across the monorepo. Keep both files in sync.
//
// Firing behavior (four-cell matrix, by Kind × SourceThreadID present):
//
//   - followup + no source        → new thread (thread_start with query),
//     normal discussion flow.
//   - followup + has source       → publish a synthetic human reply on
//     the original thread's subject. The
//     agent-runtime's DiscussionOrchestrator
//     handleThreadReopen path moves the
//     thread CLOSED → EVALUATING and the
//     crew re-discusses with prior context.
//   - reminder + no source        → new thread: thread_start + immediate
//     synthesis-style notice + thread_close,
//     no agent activity.
//   - reminder + has source       → publish a `reminder` message on the
//     original thread's subject. agentName
//     is "scheduler" (not "human") so it
//     does NOT trigger handleThreadReopen;
//     the user just sees the inline notice.
//
// Orphan-thread degrade: when the record carries a SourceThreadID but the
// thread no longer has any messages in the KUBEMOOT_DISCUSS stream (user
// deleted it via the dashboard purge, or 24h MaxAge aged it out), the
// poller falls through to the corresponding new-thread firing path. The
// degraded fire's metadata carries `originalSourceThreadId` and
// `degraded: true` for dashboard traceability. Without this check the
// synthetic message would resurrect one entry under the dead thread's
// subject (which agents would silently drop because handleThreadReopen
// requires phase CLOSED, and a fresh ThreadState defaults to SUBMITTED).
//
// See kubemoot/docs/scheduling.md for the full design.
//
// NATS is optional throughout — if NATS_URL is unset, the poller logs
// at V(1) and the tick is a no-op, mirroring the rest of operator's
// best-effort NATS integration.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
	kubemootnats "github.com/kubemoot/kubemoot/operator/internal/nats"
	"github.com/kubemoot/kubemoot/operator/internal/scheduler/record"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DefaultInterval is the polling cadence when none is configured.
const DefaultInterval = 30 * time.Second

// Re-export of the bucket name for convenience.
const Bucket = record.Bucket

// DiscussStreamName is the JetStream stream holding discussion messages.
// The scheduler checks for source-thread existence before firing inline.
const DiscussStreamName = "KUBEMOOT_DISCUSS"

// Poller runs the scheduling loop. Construct via New() and add to the
// controller-runtime Manager via mgr.Add().
type Poller struct {
	Publisher *kubemootnats.Publisher
	Interval  time.Duration
	// Now is injectable for tests. Defaults to time.Now.
	Now func() time.Time
	// SubjectHasMessages reports whether the given stream contains any
	// messages on the given subject. Injectable for tests. Default
	// implementation delegates to the Publisher's JetStream check.
	// When nil, the poller assumes source threads exist (optimistic) —
	// matches the no-NATS no-op behavior elsewhere in this package.
	SubjectHasMessages func(stream, subject string) bool
}

// NeedLeaderElection makes controller-runtime defer scheduling to the
// elected leader pod. Avoids duplicate firings when the operator
// scales horizontally.
func (p *Poller) NeedLeaderElection() bool { return true }

// Start runs the polling loop until ctx is cancelled. Satisfies the
// manager.Runnable interface.
func (p *Poller) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("scheduler")
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	if p.Now == nil {
		p.Now = time.Now
	}

	log.Info("starting scheduler poller", "bucket", Bucket, "interval", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("scheduler poller stopped")
			return nil
		case <-ticker.C:
			if err := p.tick(ctx); err != nil {
				log.V(1).Info("scheduler tick error", "err", err)
			}
		}
	}
}

// tick scans the KV bucket and fires due records.
func (p *Poller) tick(ctx context.Context) error {
	if p.Publisher == nil {
		return nil
	}

	keys, err := p.Publisher.ListKVKeys(Bucket)
	if err != nil {
		return fmt.Errorf("list KV keys: %w", err)
	}

	now := p.Now()
	for _, key := range keys {
		p.processKey(ctx, key, now)
	}
	return nil
}

// processKey fires the record at key when it is due and deletes it once fired.
// A record that cannot be decoded or cannot be scoped to a crew is dropped; a
// failed publish leaves the record for the next tick to retry.
func (p *Poller) processKey(ctx context.Context, key string, now time.Time) {
	log := logf.FromContext(ctx).WithName("scheduler")
	raw, err := p.Publisher.GetKVValue(Bucket, key)
	if err != nil || raw == nil {
		return
	}
	var rec record.Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		log.Info("dropping malformed schedule record", "key", key, "err", err)
		_ = p.Publisher.DeleteKVKey(Bucket, key)
		return
	}
	if _, err := recordScope(&rec); err != nil {
		log.Info("dropping unscoped schedule record", "key", key, "err", err)
		_ = p.Publisher.DeleteKVKey(Bucket, key)
		return
	}
	if rec.TriggerAt.After(now) {
		return
	}
	if err := p.fire(ctx, &rec); err != nil {
		log.Info("failed to fire schedule", "scheduleId", rec.ScheduleID, "err", err)
		return
	}
	_ = p.Publisher.DeleteKVKey(Bucket, key)
}

// recordScope is the namespace and crew a record fires into.
func recordScope(rec *record.Record) (crewscope.Scope, error) {
	scope, err := crewscope.New(rec.Namespace, rec.Crew)
	if err != nil {
		return crewscope.Scope{}, fmt.Errorf("invalid record: %w", err)
	}
	return scope, nil
}

// fire dispatches to the right publish path based on Kind + SourceThreadID.
// Returns nil iff the publish(es) succeeded. The caller is responsible for
// deleting the KV entry on success.
func (p *Poller) fire(ctx context.Context, rec *record.Record) error {
	if _, err := recordScope(rec); err != nil {
		return err
	}
	channel := rec.Channel
	if channel == "" {
		channel = "general"
	}
	log := logf.FromContext(ctx).WithName("scheduler")

	switch rec.EffectiveKind() {
	case record.KindFollowup:
		return p.fireFollowup(log, rec, channel)
	case record.KindReminder:
		return p.fireReminder(log, rec, channel)
	default:
		return fmt.Errorf("unknown record kind: %q", rec.Kind)
	}
}

// fireFollowup handles the KindFollowup branch of fire: validate, then
// route to inline-reopen, degrade-to-new-thread, or new-thread.
func (p *Poller) fireFollowup(log logr.Logger, rec *record.Record, channel string) error {
	if rec.Query == "" {
		return fmt.Errorf("invalid followup record: query is required")
	}
	if rec.HasSource() {
		if !p.sourceExists(rec, channel, rec.SourceThreadID) {
			log.Info("source thread missing — degrading followup to new-thread",
				"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew,
				"originalSourceThreadId", rec.SourceThreadID)
			return p.fireNewThreadFollowup(rec, channel)
		}
		log.Info("firing inline followup (reopen)",
			"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew,
			"sourceThreadId", rec.SourceThreadID)
		return p.fireInlineFollowup(rec, channel)
	}
	log.Info("firing new-thread followup",
		"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew)
	return p.fireNewThreadFollowup(rec, channel)
}

// fireReminder handles the KindReminder branch of fire: validate, then
// route to inline notice, degrade-to-new-thread, or new-thread.
func (p *Poller) fireReminder(log logr.Logger, rec *record.Record, channel string) error {
	if rec.Message == "" {
		return fmt.Errorf("invalid reminder record: message is required")
	}
	if rec.HasSource() {
		if !p.sourceExists(rec, channel, rec.SourceThreadID) {
			log.Info("source thread missing — degrading reminder to new-thread",
				"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew,
				"originalSourceThreadId", rec.SourceThreadID)
			return p.fireNewThreadReminder(rec, channel)
		}
		log.Info("firing inline reminder",
			"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew,
			"sourceThreadId", rec.SourceThreadID)
		return p.fireInlineReminder(rec, channel)
	}
	log.Info("firing new-thread reminder",
		"scheduleId", rec.ScheduleID, "namespace", rec.Namespace, "crew", rec.Crew)
	return p.fireNewThreadReminder(rec, channel)
}

// fireInlineFollowup reopens an existing thread by publishing a synthetic
// human reply. handleThreadReopen in DiscussionOrchestrator looks for
// agentName=="human" + messageType=="reply" on a CLOSED thread.
func (p *Poller) fireInlineFollowup(rec *record.Record, channel string) error {
	subject := threadSubject(rec, channel, rec.SourceThreadID)
	msg := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    rec.SourceThreadID,
		"agentName":   "human", // load-bearing: matches handleThreadReopen guard
		"messageType": "reply",
		"content":     rec.Query,
		"channel":     channel,
		"timestamp":   p.Now().UTC().Format(time.RFC3339),
		"metadata":    fireMetadata(rec, false),
	}
	return p.publish(subject, msg)
}

// fireNewThreadFollowup is the original Phase 1 behavior: emit thread_start
// for a fresh discussion that re-asks the query.
func (p *Poller) fireNewThreadFollowup(rec *record.Record, channel string) error {
	threadID := uuid.NewString()
	subject := threadSubject(rec, channel, threadID)
	msg := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    threadID,
		"agentName":   "scheduler",
		"messageType": "thread_start",
		"content":     rec.Query,
		"channel":     channel,
		"timestamp":   p.Now().UTC().Format(time.RFC3339),
		"metadata":    fireMetadata(rec, true),
	}
	return p.publish(subject, msg)
}

// fireInlineReminder posts a notice on the original thread without
// reopening it. agentName="scheduler" intentionally does not match
// handleThreadReopen's "human" guard.
func (p *Poller) fireInlineReminder(rec *record.Record, channel string) error {
	subject := threadSubject(rec, channel, rec.SourceThreadID)
	msg := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    rec.SourceThreadID,
		"agentName":   "scheduler",
		"messageType": "reminder",
		"content":     rec.Message,
		"channel":     channel,
		"timestamp":   p.Now().UTC().Format(time.RFC3339),
		"metadata":    fireMetadata(rec, false),
	}
	return p.publish(subject, msg)
}

// fireNewThreadReminder creates a self-contained reminder thread:
// thread_start → synthesis (the reminder text) → thread_close. No agent
// activity expected on this thread.
func (p *Poller) fireNewThreadReminder(rec *record.Record, channel string) error {
	threadID := uuid.NewString()
	subject := threadSubject(rec, channel, threadID)
	now := p.Now().UTC().Format(time.RFC3339)
	meta := fireMetadata(rec, true)

	start := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    threadID,
		"agentName":   "scheduler",
		"messageType": "thread_start",
		"content":     rec.Message,
		"channel":     channel,
		"timestamp":   now,
		"metadata":    meta,
	}
	if err := p.publish(subject, start); err != nil {
		return err
	}
	synth := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    threadID,
		"agentName":   "scheduler",
		"messageType": "synthesis",
		"content":     rec.Message,
		"channel":     channel,
		"timestamp":   now,
		"metadata":    meta,
	}
	if err := p.publish(subject, synth); err != nil {
		return err
	}
	close := map[string]any{
		"messageId":   uuid.NewString(),
		"threadId":    threadID,
		"agentName":   "scheduler",
		"messageType": "thread_close",
		"content":     "",
		"channel":     channel,
		"timestamp":   now,
		"metadata":    meta,
	}
	return p.publish(subject, close)
}

// threadSubject is the record's discussion subject,
// kubemoot.discuss.<ns>.<crew>.<channel>.<thread>. fire validates the scope first.
func threadSubject(rec *record.Record, channel, threadID string) string {
	return crewscope.Scope{Namespace: rec.Namespace, Crew: rec.Crew}.DiscussSubject(channel, threadID)
}

// publish wraps Publisher.Publish with a nil-Publisher guard so tests
// without an injected Publisher (or operators with NATS_URL unset) treat
// the publish as a no-op, matching the rest of this package's behavior.
func (p *Poller) publish(subject string, msg map[string]any) error {
	if p.Publisher == nil {
		return nil
	}
	return p.Publisher.Publish(subject, msg)
}

// fireMetadata builds the metadata map attached to scheduled-fire messages.
// newThread is true for new-thread fires (including degrade-from-inline);
// false for inline fires that reuse the source thread's subject.
//
// When newThread=true and the record carried a SourceThreadID, the fire
// has degraded — surface the original ID under `originalSourceThreadId`
// (and a `degraded: true` flag) so the dashboard can show "scheduled from
// a now-deleted thread" context.
func fireMetadata(rec *record.Record, newThread bool) map[string]any {
	m := map[string]any{
		"scheduleId":  rec.ScheduleID,
		"scheduledBy": rec.ScheduledBy,
		"kind":        rec.EffectiveKind(),
	}
	if rec.Reason != "" {
		m["reason"] = rec.Reason
	}
	if rec.SourceThreadID != "" {
		if newThread {
			m["originalSourceThreadId"] = rec.SourceThreadID
			m["degraded"] = true
		} else {
			m["sourceThreadId"] = rec.SourceThreadID
		}
	}
	if newThread && rec.Query != "" {
		m["userQuery"] = rec.Query
	}
	return m
}

// New constructs a Poller with sane defaults. The default
// SubjectHasMessages closes over the supplied Publisher so the scheduler
// can detect deleted source threads at fire time.
func New(pub *kubemootnats.Publisher) *Poller {
	p := &Poller{Publisher: pub, Interval: DefaultInterval, Now: time.Now}
	if pub != nil {
		p.SubjectHasMessages = func(stream, subject string) bool {
			return pub.SubjectHasMessages(stream, subject)
		}
	}
	return p
}

// sourceExists reports whether the source thread for the given record
// still has messages in the discuss stream. When no checker is configured
// (e.g. NATS unset, or in tests without injection), the scheduler is
// optimistic and treats the source as present.
func (p *Poller) sourceExists(rec *record.Record, channel, threadID string) bool {
	if p.SubjectHasMessages == nil {
		return true
	}
	return p.SubjectHasMessages(DiscussStreamName, threadSubject(rec, channel, threadID))
}
