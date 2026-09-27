// Package record is the single source of truth for the JSON document
// stored in the NATS KV bucket `kubemoot_scheduled`. Both the
// scheduling-mcp server (writer) and the kubemoot operator's scheduler
// poller (reader/firer) depend on this package — keeping the schema in
// one place prevents the Java/Go drift the earlier built-in design risked.
package record

import "time"

// Bucket is the NATS KV bucket name holding scheduled records.
const Bucket = "kubemoot_scheduled"

// Record kinds. The poller branches its publish behavior on this value.
const (
	// KindReminder is a notification: at TriggerAt, the operator
	// publishes a single user-visible message and does nothing else.
	// If SourceThreadID is set, the message is delivered inline in
	// that thread; if not, a self-contained closed thread is created.
	KindReminder = "reminder"

	// KindFollowup is a deferred query: at TriggerAt, the operator
	// causes the original query to be re-asked. If SourceThreadID is
	// set, the thread is reopened (synthetic human reply) so context
	// is preserved; if not, a fresh discussion is started.
	KindFollowup = "followup"
)

// Record is the value stored at each KV key. The key is the ScheduleID.
type Record struct {
	// ScheduleID matches the KV key. Carried in metadata of every message
	// the poller publishes so the resulting thread can be cross-referenced
	// back to its origin.
	ScheduleID string `json:"scheduleId"`

	// Kind is "reminder" or "followup". See constants above. An empty
	// Kind is treated as "followup" for backwards compatibility with
	// records written before this field existed.
	Kind string `json:"kind,omitempty"`

	// TriggerAt is when the operator should fire the record.
	TriggerAt time.Time `json:"triggerAt"`

	// ScheduledBy identifies the agent name (or "cron-...") that created
	// the record. Carried into the fired message's metadata for trace.
	ScheduledBy string `json:"scheduledBy,omitempty"`

	// Namespace is the target crew's namespace. Together with Crew it
	// derives the NATS subject kubemoot.discuss.<ns>.<crew>.<channel>.<thread>
	// for the published message, and scopes scheduling-mcp's list and
	// cancel to the calling crew's records. The poller drops a record
	// without one.
	Namespace string `json:"namespace"`

	// Crew is the target crew name. Used with Namespace to derive the
	// NATS subject for the published message and to scope list and
	// cancel to the calling crew's records.
	Crew string `json:"crew"`

	// Channel is the discussion channel ("general" by default). For
	// new-thread firing it's the channel segment of the subject; for
	// inline firing it's preserved on the message.
	Channel string `json:"channel,omitempty"`

	// Query is the content for follow-up records — the user-visible
	// question to re-ask. Required when Kind is followup.
	Query string `json:"query,omitempty"`

	// Message is the content for reminder records — the user-visible
	// notification text. Required when Kind is reminder.
	Message string `json:"message,omitempty"`

	// Reason is a free-text note explaining why the record was created.
	// Optional; surfaced in the dashboard timeline.
	Reason string `json:"reason,omitempty"`

	// SourceThreadID, when non-empty, signals that the record was
	// created inside a discussion. The poller fires inline on the
	// existing thread's subject rather than starting a new thread.
	SourceThreadID string `json:"sourceThreadId,omitempty"`
}

// EffectiveKind returns the record's Kind, defaulting to KindFollowup
// when empty. This matches the backwards-compat rule documented above.
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
