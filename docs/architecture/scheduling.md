---
title: "Scheduling - reminders, follow-ups, recurring"
description: "How a crew sets reminders, follow-ups, and recurring tasks: a Tooler calls scheduling-mcp, records land in NATS KV, and the operator fires them when due."
weight: 4
linkTitle: "Reminders and Follow-ups"
---

> **One sentence**: a crew's `scheduler-advisor` Tooler agent calls the `scheduling-mcp` server's tools to write `ScheduleRecord`s into the NATS KV bucket `kubemoot_scheduled`; the operator's scheduler poller fires due records - either inline in the originating thread or as a new thread, depending on `kind` x whether a `sourceThreadId` was attached.

## Why this exists

Some discussions are inherently *temporally* driven:

- **Reminder** - "Remind me in 30 minutes to check the backup job status."
- **Follow-up** - "Re-check disk usage in an hour" / "Run the weekly health summary every Monday."
- **Recurring** - daily/weekly admin checks, configured by a cluster admin rather than via chat.

All three reduce to: *publish a discussion message at a future time, in the right place*. This module provides the primitive.

## Architecture

```
Pilot user chat ──► coordinator ──► discussion (channel: scheduler)
                                       │
                                       ▼
                                 scheduler-advisor (Tooler agent)
                                       │ enabledTools:
                                       │   set_reminder
                                       │   schedule_followup
                                       │   list_scheduled
                                       │   cancel_scheduled
                                       ▼
                                 scheduling-mcp (Go binary, stdio MCP)
                                       │
                                       ▼   NATS KV: kubemoot_scheduled
                                              ▲
                       ┌──────────────────────┘
                       │ also writes recurring records
                  k8s CronJob (admin-side; GitOps)
                       │
                       ▼   reads, fires by Kind × HasSource()
                  operator scheduler poller ──► publishes the right
                  (30s tick, leader-elected)    message on the right
                                                discussion subject
```

The record schema is defined once in the `scheduling-mcp` server (the writer) and mirrored in the operator's scheduler package (the reader). The two are intentionally duplicated rather than shared via a Go module dependency, since per-module container builds can't cross the repository to resolve a shared import. The duplication surface is small (a ~30-line struct) and protected by a header comment in each file pointing at its counterpart.

## End-user UX (Pilot persona)

The user **never sees** scheduleIds, the bucket name, "NATS", or any internal terminology. Every example below assumes the user is chatting with the homelab-pilot crew.

| User says | scheduler-advisor does |
|---|---|
| "Remind me in 30 minutes to check the backup job." | `set_reminder(when="in 30 minutes", message="check the backup job", source_thread_id=<this thread>)`. 30 min later, a reminder line appears inline in this conversation. |
| "Ping me tomorrow at 9 to call mom." | `set_reminder(when="tomorrow at 9am", message="call mom")` - no source thread, so the reminder lands as a self-contained thread on the dashboard. |
| "Re-check disk usage in an hour." | `schedule_followup(when="in 1 hour", query="Re-check disk usage.", source_thread_id=<this thread>)`. 1 hour later, this thread re-opens and the crew re-evaluates with prior context. |
| "What's scheduled?" | `list_scheduled` → summarized in natural language: "You have 2 things scheduled: a reminder to check the backup job in 23 minutes, and a follow-up to re-check disk usage in 1h." |
| "Cancel the backup job one." | `list_scheduled` → match "backup job" by descriptor → confirm with user → `cancel_scheduled(<id>)`. |

The user-facing PromptModule for `scheduler-advisor` enforces these rules - `homelab-pilot/charts/homelab-pilot-crew/templates/promptmodule-specialists.yaml` (search for `scheduler-advisor-system`).

## Two primitives × two firing modes

The firing behavior depends on `kind` (reminder vs followup) and whether the record carries a `sourceThreadId` (i.e., the user was inside a discussion when they created it).

| Kind | sourceThreadId | What the operator publishes |
|---|---|---|
| `reminder` | yes | `messageType=reminder, agentName=scheduler` on the existing thread's subject. The user sees the reminder line inline. Does **not** reopen the thread (agentName=scheduler, not human). |
| `reminder` | no | New thread: `thread_start` (content=message) → `synthesis` (content=message) → `thread_close`, all on a fresh threadId. Self-contained reminder, no agent activity. |
| `followup` | yes | `messageType=reply, agentName=human, content=query` on the existing thread's subject. The agent-runtime's `handleThreadReopen` triggers: thread CLOSED → EVALUATING. The crew re-discusses with prior conversation context (loaded from NATS KV `conv.<conversationId>`). |
| `followup` | no | Fresh `thread_start` on a new threadId. Normal Kubemoot discussion flow runs against the query. |

The `sourceThreadId` is **never typed by the user**. The scheduler-advisor pulls it from its system prompt - the agent-runtime injects a "Discussion context: threadId=&lt;X&gt;" line when chatting inside a discussion, and the PromptModule instructs the LLM to use that.

### Orphan-thread degrade

The user can delete a discussion thread from the dashboard at any time (or it can age out of the `KUBEMOOT_DISCUSS` 24h retention window). If a `set_reminder` or `schedule_followup` record references that thread as its `sourceThreadId` and the thread is gone at fire time, the poller **degrades** the fire from inline to new-thread mode for the same `kind`. The resulting message carries two extra metadata keys for dashboard traceability:

- `originalSourceThreadId` - the deleted/aged-out thread the schedule was originally created against
- `degraded: true` - explicit flag for downstream consumers

This converts a silent failure (the synthetic reply landing on a dead subject and being ignored by every agent's `handleThreadReopen` guard) into a visible, self-contained event. The trade-off is loss of conversational context - the user gets the reminder or the re-asked question, but not the prior discussion that motivated it. That's an acceptable graceful degradation; the alternative is the schedule never firing.

## Record schema

```go
// kubemoot/scheduling-mcp/pkg/record/record.go

const Bucket = "kubemoot_scheduled"

const (
    KindReminder = "reminder"
    KindFollowup = "followup"
)

type Record struct {
    ScheduleID     string    `json:"scheduleId"`                // UUID; also the KV key
    Kind           string    `json:"kind,omitempty"`            // "reminder" or "followup"; empty defaults to followup
    TriggerAt      time.Time `json:"triggerAt"`                 // when the poller should fire
    ScheduledBy    string    `json:"scheduledBy,omitempty"`     // agent name or "cron-..." - trace only
    Namespace      string    `json:"namespace"`                 // crew's namespace; with Crew, derives the discuss subject and scopes reads/deletes
    Crew           string    `json:"crew"`                      // crew name; scopes reads/deletes with Namespace
    Channel        string    `json:"channel,omitempty"`         // "general" if empty
    Query          string    `json:"query,omitempty"`           // required for kind=followup
    Message        string    `json:"message,omitempty"`         // required for kind=reminder
    Reason         string    `json:"reason,omitempty"`          // free-text note for dashboard
    SourceThreadID string    `json:"sourceThreadId,omitempty"`  // if set, fire inline; else new-thread
}
```

## Recurring schedules - Kubernetes CronJob

Recurring schedules are configured by the cluster admin via GitOps; the user does not set them through chat. Any `CronJob` that writes a valid `Record` to the bucket gets fired by the operator on the next tick - same path as MCP-written records. Example (Monday 09:00 weekly health summary):

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: kubemoot-weekly-health
spec:
  schedule: "0 9 * * 1"
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: writer
              image: natsio/nats-box:latest
              command:
                - sh
                - -c
                - |
                  ID=$(uuidgen)
                  nats --server=nats://nats.nats.svc.cluster.local:4222 \
                    kv put kubemoot_scheduled "$ID" - <<EOF
                  {
                    "scheduleId":  "$ID",
                    "kind":        "followup",
                    "triggerAt":   "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
                    "scheduledBy": "cron-weekly-health",
                    "namespace":   "crew-homelab-pilot",
                    "crew":        "homelab-pilot",
                    "channel":     "general",
                    "query":       "Run the weekly infrastructure health summary."
                  }
                  EOF
```

Notes:
- `triggerAt` is "now" because the CronJob itself enforces the schedule; the operator fires on the next 30s tick.
- No `sourceThreadId` - CronJob-driven fires are always new-thread (there's no parent conversation).
- For a recurring **reminder** (one-shot notice instead of a discussion), set `"kind": "reminder"` and replace `"query"` with `"message"`.

## Persona rules (PromptModule enforced)

Codified in the `scheduler-advisor-system` PromptModule:

- **No scheduleIds in user-facing prose.** Ever. Cancellation resolves by description: list → match → confirm → cancel.
- **Disambiguate, don't guess.** If "cancel the backup job one" matches two pending entries, ask which.
- **Pick the right primitive.** "Remind me" → set_reminder. "Re-ask" / "follow up" → schedule_followup. When ambiguous, ask: "One-shot reminder, or a full re-discussion?"
- **Recurring through chat is admin work.** Tell the user to ask the cluster admin to add a CronJob.
- **No internal terminology surfaces.** Don't say "NATS", "bucket", "Kind", or "Record" to the user.

## Failure modes & guarantees

- **NATS down**: MCP tools return an error string the LLM can react to; operator poller logs at V(1) and re-ticks. The KV bucket persists across NATS restarts (JetStream durable).
- **Operator down at fire time**: record waits in the bucket; the next operator pod's poller fires it (late, but fires). Bounded by NATS JetStream retention.
- **Operator dies mid-fire**: at-least-once semantics - if publish succeeded but KV delete didn't, the record fires again on the next tick. Acceptable for this use case; the discussion system tolerates duplicates because the resulting discussion converges on the same answer.
- **Schedule never fires**: only if leader election fails OR the bucket gets out of sync. Inspect: `nats kv get kubemoot_scheduled <id>`. Manual fire is a `nats kv del <id>` (cancel) + re-create with current timestamp.
- **Cross-crew safety**: every read/delete via the MCP is filtered by the calling agent's namespace and crew together. A scheduler-advisor in one namespace's crew cannot see or cancel records owned by the same-named crew in another namespace, or by a different crew; cross-crew cancel attempts return "not found" without revealing existence.

## Ops commands

```bash
# What's currently scheduled cluster-wide (admin view)
nats --server=nats://nats:4222 kv ls kubemoot_scheduled
nats --server=nats://nats:4222 kv get kubemoot_scheduled <scheduleId>

# Cancel as an admin (bypasses the per-crew scoping the MCP enforces)
nats --server=nats://nats:4222 kv del kubemoot_scheduled <scheduleId>

# Watch the operator's scheduler logs
kubectl logs -n kubemoot deploy/kubemoot-operator -f | grep scheduler

# Force the poller to re-tick (just restart the leader pod)
kubectl rollout restart -n kubemoot deploy/kubemoot-operator
```

