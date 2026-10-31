---
title: "Signals & Protocol"
weight: 30
description: "agree / concern / block / stand_aside and the discussion phases."
---

Agents in a crew never call each other directly. They publish to a message bus
(NATS JetStream), and every contribution carries a **signal** that says how it relates
to the emerging answer. The signals plus a phase model are the whole protocol - there
is no central controller issuing commands.

## Consensus signals

| Signal | Meaning |
|--------|---------|
| `agree` | This contribution supports the emerging answer. |
| `concern` | A reservation that should be weighed before the crew settles. |
| `stand_aside` | No relevant contribution; abstain without blocking. With `metadata.reason` set to `gpu-busy`, `model-too-large`, or `prompt-too-large`, the agent was willing but could not get a GPU (see below). |
| `block` | A strong objection that should stop the answer as it stands. |
| `failure` | The agent tried and could not complete - surfaced as a first-class signal, not hidden behind silence. |

Treating **failure as signal** is deliberate. An agent that fails a tool call or
cannot reach a source says so, so the coordinator can route around it, rather than
standing aside silently and letting the crew mistake "no answer" for "no objection."

A Tooler that posts raw tool output as its contribution never posts errors as data:
when every tool call it made returned an error (a structured error, or a short
plain-text message such as `failed to list pods`), it publishes `failure` with the
failed tools and their error text instead of an `agree`. When at least one call
returned data, the output is posted as before, failed sections included.

Additional protocol signals facilitate the discussion itself - `triaging`,
`evaluating`, `waiting`, `advisory`, `proposal`. An agent publishes `waiting`
(with `metadata.model` and `metadata.reason: gpu-busy`) once when every GPU that can
hold its model is busy, and `evaluating` again when a GPU frees up. Which signals a crew exercises is fixed
by the runtime today; the one archetype that ships is described in
[The Moot](../consensus-model/).

## Which model a signal reports

The scheduler places each inference call when it is made, so a call can run on a
different model or server than the one the agent prefers: a warm alternative within
the agent's quality tolerance, or a re-plan when the plan made at selection only
queued. A signal names where its calls actually ran:

| Field | Meaning |
|---|---|
| `metadata.modelName` | The model the last call behind the signal ran on. On `triaging`, the triage model the agent plans to use. On a signal sent before a call completed (`evaluating`, `waiting`, `failure`), the model the agent is configured with. On the coordinator's `advisory` and `advisory_ready`, the model its advisory call ran on. |
| `metadata.gpuLabel` | The server the last call ran on, or a label derived from the agent's static endpoint when no server was picked. The coordinator's `advisory` and `advisory_ready` name the server of the advisory call. Signals sent before a call is placed (triaging, evaluating, waiting, heartbeat) carry none, because the scheduler picks the server when the call is made. |
| `metadata.modelsUsed` | Each distinct place the calls ran, as `model@server` in call order (the model alone for the static endpoint). |
| `metadata.placementCalls` | The same calls with their placement: the policy, the reason, the options weighed, the predicted completion time and what the call measured. The coordinator copies them to the thread's receipt; see [Reading a placement decision](../../architecture/scheduler/#reading-a-placement-decision). |

## What a signal reports about the turn

A signal sent for an agent running behind a [Moot Mesh sidecar](../../architecture/moot-mesh/)
carries a few more facts, all measured or recorded by the sidecar and all optional:

| Field | Meaning |
|---|---|
| `metadata.harness` | The name of the harness that produced the contribution. |
| `metadata.harnessImage` | The harness image, by digest when known. |
| `metadata.turnMs` | Milliseconds from handing the turn to the harness to its reply. |
| `metadata.evidence` | What the harness says its answer rests on, as it reported it. |
| `metadata.toolsCalled` | The tools the harness called during the turn, as counted at the sidecar's tools door. |

`metadata.toolsUsed` keeps its meaning: the tools the agent has enabled, what it
could call. `toolsCalled` is what it did call.

A message can also carry `traceparent` and `tracestate`, the W3C Trace Context of the
span that published it, so one trace follows a question across the crew. The same
value travels in the `traceparent` header of the NATS message. Both fields are
optional, and a writer that has no trace context omits them.

## Stand-asides for GPU capacity

A stand-aside usually means the agent had nothing to add. Three reasons mark a different
case, where the agent was selected and willing but the cluster could not run it:

| `metadata.reason` | Meaning |
|---|---|
| `gpu-busy` | Every GPU that can hold the model stayed busy until the discussion ended or the agent's capacity wait reached its safety limit. |
| `model-too-large` | No GPU in the cluster can ever hold the model (`metadata.model`). |
| `prompt-too-large` | No server gives an acceptable model a context window that holds the prompt. The engine would drop the oldest messages, so the agent stands aside instead. |

When no agent contributed and at least one stood aside for one of these reasons (or was
still waiting), the crew's answer says the GPUs were the limit, not the crew's design.
See [Models & Scheduling](../models-and-scheduling/#when-every-accelerator-is-busy).

## Discussion phases

A discussion advances by **state**, not by a fixed timer. The coordinator runs a
per-thread state machine with the following phases:

`SUBMITTED` → `ADVISORY` → `EVALUATING` → `REVIEW` → `SYNTHESIZING` → `CLOSED`

A `PAUSED` state can interrupt any phase except `SYNTHESIZING` and `CLOSED`; the
machine resumes to the same phase when unpaused. A dashboard Stop forces an
immediate transition to `SYNTHESIZING` on whatever signals exist. A human reply on a
closed thread reopens it to `EVALUATING`.

In plain terms, the flow is:

1. **ADVISORY** - the coordinator generates the framing advisory and selects the
   Tooler subcommittee; Toolers acknowledge with `triaging`.
2. **EVALUATING** - selected Toolers call their domain MCP tools and publish findings,
   each carrying a signal.
3. **REVIEW** - Analysts (if the crew has them) reason over the Toolers' gathered data
   and contribute interpretive findings before synthesis. The coordinator waits for
   every Analyst it woke to report; there is no fast path in this phase.
4. **SYNTHESIZING** - the coordinator composes the answer from all contributions.
5. **CLOSED** - the answer has been delivered and the thread is clean.

Timeouts exist only as safety nets. The normal path is driven by signal state: each
phase has a roster (the selected Toolers in EVALUATING, the woken Analysts in REVIEW),
and when every roster member has published a terminal signal in that phase, the phase
advances at once. The minimum phase times and the quiet period apply only when the
roster is unknown or a member is still pending. GPU
inference and model loading have unpredictable latency, so the protocol waits on
state transitions, not wall-clock deadlines.

The full state machine (all states, both automatic and event-driven transitions, and
the key design principles) is documented in
[The Moot - Discussion phase lifecycle](../consensus-model/#discussion-phase-lifecycle).

## The coordinator log

The coordinator decides things no agent message records: which agents it convened, which
agents a phase waits for and by when, why a phase ended, what the answer is. Those
decisions can be written to a log, one per thread, so that what the coordinator knew about
a thread can be read back, and so that a later version can rebuild a thread's state from
the log. The log is off by default. Turn it on for every coordinator with the chart value
`coordinator.eventLog=true` (it reaches the [KubemootConfig](../../reference/kubemootconfig-guide/#coordinator)
and from there the `KUBEMOOT_COORDINATOR_EVENT_LOG` variable of each coordinator Agent), or
for one coordinator by setting the variable in the Agent's `spec.deployment.env`, which
wins over the chart. With the log on, a coordinator [writes ahead](#write-ahead), keeps
other coordinators off its threads, and [checks the log](#the-check-at-close) when a thread
closes; with it off nothing about a discussion changes.

Each thread has its own subject, `kubemoot.coordinator.<namespace>.<crew>.<threadId>`,
held by the JetStream stream `KUBEMOOT_COORDINATOR` for a day. Agents do not read it.
Every message is an event:

| Field | Meaning |
|---|---|
| `v` | The envelope version, 1 |
| `eventId` | A key that names the decision. Writing the same decision twice gives the same id, and a reader ignores an id it already applied |
| `type` | What was decided (the table below). A reader skips a type it does not know |
| `threadId`, `round` | The thread, and which round of it: 0 until a human reply reopens the thread, then 1, 2 and so on |
| `at`, `coordinator` | When, and which coordinator agent wrote it |
| `data` | The fields of the type |

| `type` | Written when | Main `data` fields |
|---|---|---|
| `thread_opened` | the coordinator takes the thread up | `userQuery`, `conversationId`, `crew`, `source` (`request` or `external`), `openedAt` |
| `phase_entered` | the thread enters ADVISORY, EVALUATING, REVIEW, SYNTHESIZING or PAUSED, or resumes | `phase`, `phaseStartedAt`, `phaseBeforePause`, `advisoryPending`, `reason` |
| `subcommittee_convened` | the advisory step ends | `path` (`reasoning`, `fallback`, `broadcast` or `direct`), `members`, `brief`, `skills`, `confidence`, `tokens` |
| `evaluation_started` | the evaluation clock resets after `advisory_ready` | `roster` (who EVALUATING waits for) |
| `review_started` | `review_ready` is sent | `roster` (the analysts woken), `pending` (each with its deadline) |
| `signal_applied` | a signal changes the thread: `triaging`, `evaluating`, `waiting`, `waking`, `ready`, `agree`, `concern`, `block`, `stand_aside` | `agent`, `signal`, `messageId`, `deadlineAt`, and for a stand-aside for want of an accelerator `capacityReason` |
| `failure_recorded` | an agent reports `failure`, or its deadline passes and the coordinator records one | `agent`, `cause` (`reported` or `evaluation_timeout`), `synthesized`, `failedSources` |
| `settled` | a phase ends | `phase`, `basis` (`roster_signalled`, `fast_path`, `quiet` or `no_pending`), `counts` |
| `synthesis_started` | the synthesis call begins | the signal counts |
| `synthesis_done` | the answer exists, before it is published | `outcome` (`synthesized`, `capacity`, `direct`, `fallback` or `ceiling`), `response`, `tokens` |
| `thread_reopened` | a human reply reopens a closed thread | `phaseStartedAt` |
| `thread_closed` | the thread ends | `reason`, `channel` |

A few rules hold for every event:

- **Deadlines are absolute.** `deadlineAt` is a time in milliseconds since the epoch, not a
  number of seconds to wait, so it means the same whenever it is read.
- **A signal event points at the bus message, it does not copy it.** `messageId` names the
  message on the discussion stream that carried the signal, and the text stays there.
  Failures are recorded as failures, never as a stand-aside.
- **Heartbeats are not recorded.** A heartbeat only pushes an agent's deadline out; the
  deadline in the log is the last one an event set.
- **A log that is down never silences the crew.** An event that cannot be written leaves
  the thread unlogged and does not hold back its effects. See [Write-ahead](#write-ahead).
- **The state rebuilds from the events.** Applying a thread's events in order, and skipping
  any event whose `eventId` was already applied, gives the state the coordinator held at
  the last event: the phase and its clock, the convened agents, the roster and who has
  signalled, the signals by agent, the agents still pending and the deadline an event last
  set for each (a heartbeat can have pushed the live deadline later), and the answer. The coordinator
  writes the log and can rebuild a thread's state from it; a coordinator that starts does
  not yet resume threads from it.

### Write-ahead

A decision's event is written to the log, and acknowledged by the stream, before the
coordinator carries the decision out:

| Decision (event) | Carried out only after it is in the log |
|---|---|
| `subcommittee_convened` | the `advisory` brief and the `subcommittee` line |
| `evaluation_started` | the `advisory_ready` signal that tells the toolers to evaluate |
| `review_started` | the `review_ready` signal that wakes the analysts |
| `synthesis_done` | the `synthesis` answer, then `thread_close` |

So while the log is up it never lacks a decision that agents have already acted on, and a
coordinator that stopped between the two can repeat the effect.

If an append fails (the stream cannot be reached, or it refuses the event for a reason
other than another writer, see below), a log outage must not silence the crew: the
coordinator **publishes the effect anyway** and marks the thread *unlogged*. The thread's
log is incomplete from the failed event on, so a coordinator that took the thread over
could not rebuild it, and the thread is not resumable. For that thread the coordinator
appends no further events (later events would make the incomplete log look complete), logs
one WARN naming the event that failed, and skips the [check at close](#the-check-at-close).
The messages it publishes for an unlogged thread from then on carry random ids and no
`Nats-Msg-Id`, as with the log off, so a repeat of one is not dropped by the stream. The loss is counted in `kubemoot_coordinator_log_append_failures_total`
(each failed append) and `kubemoot_coordinator_threads_unlogged_total` (each thread).

The bus messages that carry out a logged decision have ids made from the event's id (the
answer is `<threadId>/0/synthesis/done/1/msg/synthesis`, the close
`<threadId>/0/closed/msg/thread_close`). The same decision carried out twice has the same
`messageId`, which every consumer already drops, and the same `Nats-Msg-Id`
(`<messageId>@<subject>`, one per subject the message goes to), which the
`KUBEMOOT_DISCUSS` stream drops inside its duplicate window (two minutes, the NATS
default; a repeat after a longer gap is still dropped by the consumers' `messageId` check). With the log
off these messages keep a random `messageId` and carry no `Nats-Msg-Id`.

### One coordinator per thread

Every append states the sequence of the last event this coordinator wrote on the thread's
subject (JetStream's expected-last-subject-sequence). If another writer appended to the
thread in between, the stream refuses the append: this coordinator no longer owns the
thread. It logs the refusal at WARN, counts it in
`kubemoot_coordinator_log_fenced_total`, and stops acting on the thread:
it publishes nothing more for it, reads none of its messages and runs none of its phase
checks. Because decisions are written before they are acted on, a coordinator that was
outrun cannot publish an answer the other one did not see. A fenced thread's asker is
released without an answer (the other coordinator owns it), and a fenced thread past the
zombie age is dropped without publishing. A failed append is not a fence: an append that
timed out may have been stored, but the coordinator stops appending to that thread, so
it never meets a refusal it could mistake for another writer. There is no lease yet to
decide which coordinator takes a thread, so the fence is the guard against two
coordinators running one thread.

### The check at close

When a thread closes, the coordinator rebuilds the thread's state from the events the
stream acknowledged for it and compares it with the state it holds. They agree on every
field except an agent's pending deadline, which a heartbeat moves without an event. On a
difference the coordinator logs a WARN naming each differing field with both values
(`signals.agree: log=[] live=[metrics]`) and counts the check in
`kubemoot_coordinator_log_checks_total{result="mismatch"}`; a match is counted as
`match`, and a close of an unlogged thread, whose log is incomplete and is not compared, as `unlogged`
(one per close; a thread reopened by a human reply closes more than once).
A difference never fails the thread. A thread with more than 2,048 events is not checked.

### Stream limits

The coordinator log is held by `KUBEMOOT_COORDINATOR` for 24 hours: 100,000 events, 512 per
thread, 256 MiB. The discussion messages the log points at (`messageId`) stay on the
`KUBEMOOT_DISCUSS` stream, which every crew in the cluster shares. Its limits are chart
values, because a thread can be read back only while its messages are still on the stream:

| Chart value | Default | Meaning |
|---|---|---|
| `nats.streams.discuss.maxMessages` | `5000` | Messages kept across all crews; the oldest go first. `-1` for no limit |
| `nats.streams.discuss.maxAge` | `24h` | How long a message is kept: a number and `s`, `m`, `h`, `d`, `w` or `y`. `-1` for no limit |
| `nats.streams.discuss.maxBytes` | `-1` | Bytes kept, such as `256MiB`. `-1` for no limit |

A thread lives minutes (the hard ceiling is about seven) and sends 6 to about 50 messages, 15 at
the median. Size `maxMessages` for the busiest traffic of all crews multiplied by the time
between a coordinator stopping and its replacement running, plus a thread's life, with
headroom. On a cluster with two crews the busiest stretch measured about 1,200 messages an
hour, so the default holds about four hours of it, many times what a restart needs; a
cluster that runs many crews at once, or fitness suites alongside live traffic, raises the
value. The job that creates the streams runs on every install and upgrade and updates an
existing stream to the values.

## The bus contract

The messages on the bus, the key-value entries agents and Kubemoot share, and the
discussion gateway's event stream are written down as one versioned contract: an
[AsyncAPI](https://www.asyncapi.com/) document with a JSON Schema file per payload, in
[`operator/pkg/bus/schema`](https://github.com/kubemoot/kubemoot/tree/main/operator/pkg/bus/schema).
It is the reference for every field named on this page. The Go types the operator and
the discussion gateway use, and the TypeScript types the dashboard uses, are generated
from it.

| Channel | Address | Payload |
|---|---|---|
| Discussion thread | `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>` | `DiscussionMessage`: `messageId`, `threadId`, `agentName`, `messageType`, `content`, `channel`, `timestamp`, `traceparent`, `tracestate`, `metadata` |
| Agent lifecycle | `kubemoot.lifecycle.<namespace>.<crew>.<signal>.<agent>` | `DiscussionMessage` with `waking` or `ready` |
| Coordinator log | `kubemoot.coordinator.<namespace>.<crew>.<threadId>` | `CoordinatorEvent`: `eventId`, `type`, `threadId`, `round`, `at`, `coordinator`, `data` (see [The coordinator log](#the-coordinator-log)) |
| Request queue | `kubemoot.request.<namespace>.<crew>` | `DiscussionRequest`: the question for the crew's coordinator |
| Model lifecycle | `kubemoot.server.model.<server>` | `ModelLifecycleRequest`: an agent asks the operator to wake (`ready`) or sleep (`release`) a model on a vLLM server; the reply is a `ModelLifecycleReply` sent once the server reports the state |
| Artifact notices | `kubemoot.artifacts.<namespace>.<crew>.<threadId>` | `ArtifactNotice`: a contribution spilled to the artifact store |
| Direct chat | `kubemoot.chat.<namespace>.<agent>` | `ChatEvent` |
| Onboarding | `kubemoot.operator.onboarding.deployed`, `.progress`, `.docs-indexed` | `OnboardingDeployed`, `OnboardingProgress`, `OnboardingDocsIndexed` |
| Discussion stream | `GET /api/v1/discussions/{crew}/{conversationId}/stream` on the gateway | `StreamEvent`, as server-sent events |

The key-value buckets are channels too, addressed as `$KV.<bucket>.<key>`:
`kubemoot_agent_state` (heartbeats), `kubemoot_server_state` and
`kubemoot_server_tickets` (accelerator capacity and claims), `kubemoot_model_demand`,
`kubemoot_scheduler_performance` (what the scheduler observed), `kubemoot_latency`,
`kubemoot_conversations`, `kubemoot_crew_resumes`, `kubemoot_crew_memory`,
`kubemoot_scheduled` and `kubemoot_pinned_threads`.

A field is required only where its writer always sends it; every other field is
optional, and readers ignore fields they do not know. The contract's version is the
AsyncAPI document's `info.version`. Removing a field, changing its type, or changing
whether it is required is a breaking change and needs a new major version; the
operator's tests compare the schema with the surface recorded for the current version
and fail on a breaking change without one. Recorded messages from a running crew, and
what agent-runtime writes, are validated against the schema in the tests too.

## Why a protocol instead of direct calls

Routing every contribution through the bus with an explicit signal makes disagreement
and abstention **observable**. The coordinator settles on what the crew actually
reported - agreements net of concerns and blocks - instead of taking the first or
loudest answer. It also means an agent crashing or timing out degrades gracefully:
its absence (or its `failure` signal) is visible to the coordinator rather than
silently corrupting the result.
