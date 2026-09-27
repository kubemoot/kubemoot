// Package record defines the JSON document stored in the NATS KV bucket
// `kubemoot_scheduled`.
//
// IMPORTANT: keep in sync with kubemoot/scheduling-mcp/pkg/record/record.go.
// Both files define the same wire schema; the operator's per-module
// Kaniko build context can't cross monorepo boundaries, so the schema
// is duplicated as a Go-only file (no Java/Go cross-language drift).
// If you change one, change the other. Both files are short by design
// to keep this discipline cheap.
package record

import "time"

// Bucket is the NATS KV bucket name holding scheduled records.
const Bucket = "kubemoot_scheduled"

// Record kinds. The poller branches its publish behavior on this value.
const (
	KindReminder = "reminder"
	KindFollowup = "followup"
)

// Record is the value stored at each KV key. The key is the ScheduleID.
type Record struct {
	ScheduleID     string    `json:"scheduleId"`
	Kind           string    `json:"kind,omitempty"`
	TriggerAt      time.Time `json:"triggerAt"`
	ScheduledBy    string    `json:"scheduledBy,omitempty"`
	Namespace      string    `json:"namespace"`
	Crew           string    `json:"crew"`
	Channel        string    `json:"channel,omitempty"`
	Query          string    `json:"query,omitempty"`
	Message        string    `json:"message,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	SourceThreadID string    `json:"sourceThreadId,omitempty"`
}

// EffectiveKind returns the record's Kind, defaulting to KindFollowup
// when empty (backwards compatibility with older records).
func (r *Record) EffectiveKind() string {
	if r.Kind == "" {
		return KindFollowup
	}
	return r.Kind
}

// HasSource reports whether the record carries a SourceThreadID — i.e.
// whether the poller should fire inline rather than starting a new thread.
func (r *Record) HasSource() bool {
	return r.SourceThreadID != ""
}
