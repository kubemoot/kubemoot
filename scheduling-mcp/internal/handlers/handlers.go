// Package handlers implements the four scheduling tools surfaced over
// MCP: set_reminder, schedule_followup, list_scheduled, cancel_scheduled.
//
// All four operate on the NATS KV bucket named in pkg/record.Bucket.
// Reads and deletes are filtered by the crew configured on the Set —
// each crew sees only its own records.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kubemoot/kubemoot/scheduling-mcp/internal/timeparse"
	"github.com/kubemoot/kubemoot/scheduling-mcp/pkg/record"
)

// KV abstracts the slice of nats.KeyValue we actually use, so handler
// logic is testable without a live NATS connection.
type KV interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Keys(ctx context.Context) ([]string, error)
}

// Set is the registry of tool specifications and their dispatch.
type Set struct {
	KV        KV
	Namespace string // the namespace of the crew this MCP serves; carried on every record
	Crew      string // the crew this MCP serves; carried on every record
	Now       func() time.Time
}

// New constructs a Set with sane defaults.
func New(kv KV, namespace, crew string) *Set {
	return &Set{KV: kv, Namespace: namespace, Crew: crew, Now: time.Now}
}

// owns reports whether the record belongs to this Set's namespace and crew.
func (s *Set) owns(r *record.Record) bool {
	return r.Namespace == s.Namespace && r.Crew == s.Crew
}

// ─── Tool surfaces (MCP-shaped specifications) ──────────────────────────

// Specifications returns the tool-list payload MCP clients consume.
func (s *Set) Specifications() []map[string]any {
	return []map[string]any{
		{
			"name":        "set_reminder",
			"description": setReminderDesc,
			"inputSchema": mustSchema(map[string]any{
				"type":     "object",
				"required": []string{"when", "message"},
				"properties": map[string]any{
					"when": map[string]any{
						"type":        "string",
						"description": `Natural-language time. "in 30 minutes", "tomorrow at 9am", "Monday at 14:30", or an RFC 3339 timestamp.`,
					},
					"message": map[string]any{
						"type":        "string",
						"description": "The text to deliver as a reminder. Be specific — the future-you reading it lacks today's chat context.",
					},
					"source_thread_id": map[string]any{
						"type":        "string",
						"description": "Optional. The discussion threadId this reminder was created in, so it fires inline. Pull this from 'Discussion context: threadId=...' in your system prompt; omit it if you are not inside a discussion.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional free-text note for the dashboard timeline.",
					},
				},
			}),
		},
		{
			"name":        "schedule_followup",
			"description": scheduleFollowupDesc,
			"inputSchema": mustSchema(map[string]any{
				"type":     "object",
				"required": []string{"when", "query"},
				"properties": map[string]any{
					"when": map[string]any{
						"type":        "string",
						"description": `Natural-language time. Same syntax as set_reminder.`,
					},
					"query": map[string]any{
						"type":        "string",
						"description": "The question to re-ask at the scheduled time. Phrase it fully — the follow-up discussion has no memory of the current one unless source_thread_id is set.",
					},
					"source_thread_id": map[string]any{
						"type":        "string",
						"description": "Optional. The discussion threadId this follow-up was created in. When present, the original thread is reopened (synthetic human reply) so the prior conversation context is preserved.",
					},
					"reason": map[string]any{
						"type":        "string",
						"description": "Optional free-text note.",
					},
				},
			}),
		},
		{
			"name":        "list_scheduled",
			"description": listScheduledDesc,
			"inputSchema": mustSchema(map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
		},
		{
			"name":        "cancel_scheduled",
			"description": cancelScheduledDesc,
			"inputSchema": mustSchema(map[string]any{
				"type":     "object",
				"required": []string{"scheduleId"},
				"properties": map[string]any{
					"scheduleId": map[string]any{
						"type":        "string",
						"description": "The exact scheduleId (UUID) returned by list_scheduled. The end user never sees these — call list_scheduled first to look up the right id by descriptor.",
					},
				},
			}),
		},
	}
}

const setReminderDesc = `Schedule a one-shot reminder. The operator will publish a notice at the scheduled time — agents do NOT respond, the user just sees the reminder appear.

Use this when the user says "remind me…", "ping me…", or "tell me at…". Do NOT use this for questions; use schedule_followup if they want a discussion to run later.

If you are inside a discussion thread (look for 'Discussion context: threadId=...' in your system prompt), pass that as source_thread_id so the reminder fires inline in this conversation. If you are not, omit it and the reminder lands as a new closed thread.`

const scheduleFollowupDesc = `Schedule a deferred discussion. The operator will re-pose the query at the scheduled time and let the crew discuss it.

Use this when the user says "re-check…", "ask again later…", "follow up on…", or "run the weekly health summary every Monday".

If you are inside a discussion thread (look for 'Discussion context: threadId=...' in your system prompt), pass that as source_thread_id so the follow-up reopens this conversation (preserves context). If you are not, omit it and a fresh discussion will run.`

const listScheduledDesc = `Return the pending scheduled reminders and follow-ups for this crew. Use this when the user asks "what's scheduled?", "any reminders?", or before cancelling so you can identify the right entry. Returns a JSON array; describe items to the user by their content + relative time, NEVER by scheduleId.`

const cancelScheduledDesc = `Cancel a pending schedule by id. ALWAYS call list_scheduled first to find the right id from the user's description; the user does not know ids. Confirm with the user before calling cancel_scheduled ("That's the 9am disk check — cancel?"). On success, the schedule will not fire.`

// ─── Dispatch ───────────────────────────────────────────────────────────

// Call routes a tool-call to the right handler. Returns the text result
// (sent back to the LLM verbatim) or an error.
func (s *Set) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	switch name {
	case "set_reminder":
		return s.setReminder(ctx, args)
	case "schedule_followup":
		return s.scheduleFollowup(ctx, args)
	case "list_scheduled":
		return s.listScheduled(ctx)
	case "cancel_scheduled":
		return s.cancelScheduled(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// ─── set_reminder ───────────────────────────────────────────────────────

type setReminderArgs struct {
	When           string `json:"when"`
	Message        string `json:"message"`
	SourceThreadID string `json:"source_thread_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (s *Set) setReminder(ctx context.Context, raw json.RawMessage) (string, error) {
	var a setReminderArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.When) == "" || strings.TrimSpace(a.Message) == "" {
		return "", errors.New("`when` and `message` are required")
	}
	trigger, err := timeparse.Parse(a.When, s.Now())
	if err != nil {
		return "", err
	}
	rec := record.Record{
		ScheduleID:     uuid.NewString(),
		Kind:           record.KindReminder,
		TriggerAt:      trigger,
		ScheduledBy:    "scheduler-advisor",
		Namespace:      s.Namespace,
		Crew:           s.Crew,
		Channel:        "general",
		Message:        strings.TrimSpace(a.Message),
		Reason:         strings.TrimSpace(a.Reason),
		SourceThreadID: strings.TrimSpace(a.SourceThreadID),
	}
	if err := s.write(ctx, &rec); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Reminder scheduled for %s — message: %q (id=%s).",
		humanWhen(rec.TriggerAt, s.Now()), rec.Message, rec.ScheduleID), nil
}

// ─── schedule_followup ──────────────────────────────────────────────────

type scheduleFollowupArgs struct {
	When           string `json:"when"`
	Query          string `json:"query"`
	SourceThreadID string `json:"source_thread_id,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (s *Set) scheduleFollowup(ctx context.Context, raw json.RawMessage) (string, error) {
	var a scheduleFollowupArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.When) == "" || strings.TrimSpace(a.Query) == "" {
		return "", errors.New("`when` and `query` are required")
	}
	trigger, err := timeparse.Parse(a.When, s.Now())
	if err != nil {
		return "", err
	}
	rec := record.Record{
		ScheduleID:     uuid.NewString(),
		Kind:           record.KindFollowup,
		TriggerAt:      trigger,
		ScheduledBy:    "scheduler-advisor",
		Namespace:      s.Namespace,
		Crew:           s.Crew,
		Channel:        "general",
		Query:          strings.TrimSpace(a.Query),
		Reason:         strings.TrimSpace(a.Reason),
		SourceThreadID: strings.TrimSpace(a.SourceThreadID),
	}
	if err := s.write(ctx, &rec); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Follow-up scheduled for %s — will re-ask: %q (id=%s).",
		humanWhen(rec.TriggerAt, s.Now()), rec.Query, rec.ScheduleID), nil
}

// ─── list_scheduled ─────────────────────────────────────────────────────

type listedItem struct {
	ScheduleID     string `json:"scheduleId"`
	Kind           string `json:"kind"`
	When           string `json:"when"`       // RFC 3339, model can compare to current time
	Descriptor     string `json:"descriptor"` // the message or query, for natural-language matching
	SourceThreadID string `json:"sourceThreadId,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (s *Set) listScheduled(ctx context.Context) (string, error) {
	keys, err := s.KV.Keys(ctx)
	if err != nil {
		return "", fmt.Errorf("list keys: %w", err)
	}
	items := make([]listedItem, 0, len(keys))
	for _, k := range keys {
		raw, err := s.KV.Get(ctx, k)
		if err != nil || raw == nil {
			continue
		}
		var r record.Record
		if json.Unmarshal(raw, &r) != nil {
			continue
		}
		if !s.owns(&r) {
			continue
		}
		items = append(items, listedItem{
			ScheduleID:     r.ScheduleID,
			Kind:           r.EffectiveKind(),
			When:           r.TriggerAt.UTC().Format(time.RFC3339),
			Descriptor:     descriptorOf(&r),
			SourceThreadID: r.SourceThreadID,
			Reason:         r.Reason,
		})
	}
	out, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func descriptorOf(r *record.Record) string {
	if r.EffectiveKind() == record.KindReminder {
		return r.Message
	}
	return r.Query
}

// ─── cancel_scheduled ───────────────────────────────────────────────────

type cancelArgs struct {
	ScheduleID string `json:"scheduleId"`
}

func (s *Set) cancelScheduled(ctx context.Context, raw json.RawMessage) (string, error) {
	var a cancelArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	a.ScheduleID = strings.TrimSpace(a.ScheduleID)
	if a.ScheduleID == "" {
		return "", errors.New("`scheduleId` is required")
	}

	// Read first so cross-crew cancels can be reported as "not found"
	// without revealing the record exists.
	existing, err := s.KV.Get(ctx, a.ScheduleID)
	if err != nil {
		return "", fmt.Errorf("read: %w", err)
	}
	if existing == nil {
		return "", fmt.Errorf("no schedule with id %s found for this crew", a.ScheduleID)
	}
	var r record.Record
	if err := json.Unmarshal(existing, &r); err != nil {
		return "", fmt.Errorf("read decode: %w", err)
	}
	if !s.owns(&r) {
		return "", fmt.Errorf("no schedule with id %s found for this crew", a.ScheduleID)
	}
	if err := s.KV.Delete(ctx, a.ScheduleID); err != nil {
		return "", fmt.Errorf("delete: %w", err)
	}
	return fmt.Sprintf("Cancelled — was: %q (kind=%s).", descriptorOf(&r), r.EffectiveKind()), nil
}

// ─── helpers ────────────────────────────────────────────────────────────

func (s *Set) write(ctx context.Context, r *record.Record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.KV.Put(ctx, r.ScheduleID, raw)
}

// humanWhen returns a short user-friendly relative string like "in 30 min"
// or "in 2 days" — used in the tool's confirmation reply, NOT in user-facing
// prose (the LLM is instructed to summarize naturally for the user).
func humanWhen(t, now time.Time) string {
	d := t.Sub(now)
	if d < time.Minute {
		return fmt.Sprintf("in %d sec", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("in %d min", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("in %d hr %d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("in %d day(s) at %s UTC", int(d.Hours())/24, t.Format("15:04"))
}

func mustSchema(m map[string]any) map[string]any { return m }
