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

A contribution (`agree`) carries what the agent states followed by the description of each file its
tool calls stored: `[CALL key=... tool=... args=... format=... bytes=... rows=... error=...]` lines
under a `[WORKSPACE ...]` header, or a `[NOT STORED ...]` line for a result that could not be
written. The keys in those lines are relative to the thread (`k8s-workloads/001-pods_list`). A
result small enough for the readers' context window follows its line whole, between `[RESULT]` and
`[END RESULT]` (`[FAILED RESULT]` for the error text of a failed call); a larger result is only in
its file, and a result is never cut. See the
[discussion artifact store](discussion-artifact-store.md#what-an-agent-posts-the-descriptions-of-its-files-and-its-small-results).
Agents that read files add the facts they find to their own contribution; the synthesizer answers
from the discussion only.

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

### No facts, no model

The synthesizer answers from the discussion, so it needs something to answer from. When the
discussion holds **no facts** but tools ran, the coordinator does not call the model: a model with
nothing to answer from invents. It closes the thread with an answer it writes in code:

- the question,
- the tools that ran, per agent, with the number of calls and how many failed (from the file
  descriptions),
- the failures with their text: the error text a failed call posted, and what an agent reported
  when it failed,
- the plain statement that no data was obtained and nothing can be reported as fact.

It is a normal synthesized answer: it is published as the `synthesis` message, the thread closes,
and the asker and the fitness judge read it like any other.

**The discussion holds facts** when a contribution of type `agree`, `contribution`, `proposal`,
`concern` or `block` has either

- a `[RESULT]` block with a result that is not blank, or
- its own text, which is whatever is left after taking out the `[WORKSPACE ...]` header, the
  `[CALL ...]` and `[NOT STORED ...]` lines, the closing "stored under these keys" line, the
  `[N more calls under the prefix]` line, the `[FAILED RESULT]` blocks, and the words
  `NOTHING_TO_ADD` and `NO_DATA`.

An empty `agree`, a `stand_aside`, a `decline`, a `failure`, the coordinator's own messages (the
advisory, the review list) and the user's question hold no facts. A `concern` counts, because its
text is usually a `TOOL_GAP:` that the model path turns into a hint about the missing tool.

**The shortcut applies only when tools ran**: at least one contribution describes files (a
`[CALL ...]` or `[NOT STORED ...]` line). A discussion where every agent stood aside, or only
failed without storing a call, still goes to the model, which answers a general question from its
own knowledge or reports the sources it could not reach.

The decision is made in code on what is in the discussion, never on a timer. The thread's
`synthesis_done` event carries the outcome `no_data` (see the table below), so the case is
countable: the share of threads that end with `no_data` is the share of questions that got no
data from their tools.

## The coordinator log

The coordinator decides things no agent message records: which agents it convened, which
agents a phase waits for and by when, why a phase ended, what the answer is. Those
decisions can be written to a log, one per thread, so that what the coordinator knew about
a thread can be read back, and so that a later version can rebuild a thread's state from
the log. The log is on by default. Turn it off for every coordinator with the chart value
`coordinator.eventLog=false`, together with `coordinator.resume=false` (it reaches the [KubemootConfig](../../reference/kubemootconfig-guide/#coordinator)
and from there the `KUBEMOOT_COORDINATOR_EVENT_LOG` variable of each coordinator Agent), or
for one coordinator by setting the variable in the Agent's `spec.deployment.env`, which
wins over the chart. With the log on, a coordinator [writes ahead](#write-ahead), keeps
other coordinators off its threads, and [checks the log](#the-check-at-close) when a thread
closes; with it off nothing about a discussion changes. The stream is created either way.

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
| `synthesis_done` | the answer exists, before it is published | `outcome` (`synthesized`, `capacity`, `no_data`, `direct`, `fallback` or `ceiling`), `response`, `tokens`, `promptTooLarge`. A log written by an earlier release may also carry `rounds`, `filledBytes` and `delivery`; they are ignored on replay |
| `thread_reopened` | a human reply reopens a closed thread | `phaseStartedAt` |
| `thread_closed` | the thread ends | `reason` (an outcome of `synthesis_done`, `zombie`, `no_signals`, `unlogged`, or `expired`), `channel` |
| `coordinator_resumed` | a restarted coordinator takes the thread over | `fromSeq` (the last event it read), `resumedAt` |

`thread_opened` also carries `requestSeq`, the stream sequence of the queued request that
started the thread, when a request did.

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
  set for each (a heartbeat can have pushed the live deadline later), and the answer. A
  coordinator that starts with [resume](#resume) on rebuilds its threads this way.

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
it never meets a refusal it could mistake for another writer. Without
[leases](#leases-and-more-than-one-replica) nothing decides which coordinator takes a thread,
so the fence is the guard against two coordinators running one thread; with leases on, the fence
is still the arbiter and the lease keeps replicas from trying.

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

### Resume

Resume is on by default (`coordinator.resume`; it needs `coordinator.eventLog`, so a release that
turns the log off must turn resume off too, and the chart refuses one without the other). A coordinator that starts picks up the threads the last one left in flight. Run one
coordinator replica unless you turn on [leases](#leases-and-more-than-one-replica): without them
nothing decides which of two replicas owns a thread, only the
[fence](#one-coordinator-per-thread) keeps them apart. With resume off
nothing about a discussion changes.

**At start** the coordinator reads its crew's coordinator log, reduces each thread's events to
the state they describe, and sorts the threads. A thread that closed is only remembered, so a
request handed again can be answered from its logged answer. A thread still open is a
candidate, and live messages for it wait behind a gate until the candidate is rebuilt.

**For each open thread** it then:

1. Checks the thread's age. A thread whose last logged event is older than the **resume age
   limit** (`coordinator.resumeMaxAge`, default `30m`) is not resumed: its asker stopped
   waiting long ago, and resuming it would spend accelerator time for nobody. It is closed
   the way an incomplete one is (below): its agents get `thread_close`, `thread_closed` is
   written with reason `expired`, and the request that started it, handed again, starts a
   new thread. The limit is a Go duration such as `30m` or `1h30m`; `0` or empty turns it
   off, and then a thread of any age is resumed (the log holds a day at most). The chart
   refuses a value that is not a duration.
2. Reads the thread's messages back from `KUBEMOOT_DISCUSS` with an ordered consumer.
3. Checks the log is complete. If the coordinator published one of its own messages with a
   random id (what a thread whose append failed leaves behind, see [Write-ahead](#write-ahead)),
   the log ends before the thread did. Such a thread is **not resumed**. It is closed: its
   agents get `thread_close` so they stop working for nobody, a `thread_closed` event with
   reason `unlogged` is written so the next start does not find it open again, and the request
   that started it, handed again, starts a new thread. Leaving it to its hard ceiling would
   not work, because the ceiling lives in the memory of the process that died.
4. Rebuilds the state from the log and writes `coordinator_resumed`. If another coordinator has
   appended since, the append is refused and this one leaves the thread alone.
5. Gives every agent still pending a fresh deadline: the window a heartbeat gives (the agent's
   expected time, at least one heartbeat interval, plus grace), or what the logged deadline
   still had left at the last event if that is longer. The hard ceiling, the zombie sweep and
   the phase clock count from the takeover. The time the thread had no coordinator is not
   charged to the agents or to the thread.
6. Applies the messages the log has not seen, in stream order, through the handlers the live
   path uses. A signal whose `messageId` the log holds is skipped. A heartbeat is not replayed.
   A stop, pause or resume is applied only if it came after the thread's last logged
   decision: by stream sequence when the log states one (with [leases](#leases-and-more-than-one-replica)
   on), else by the message's timestamp. Each applied signal is logged as it is live.
7. Puts the thread in the phase sweep, carries out what the last decision left undone, and
   opens the gate; the live messages that waited are applied in the order they arrived, and
   a message that was both read from the stream and delivered live is applied once.

What step 7 carries out depends on the last logged decision. A decision's event is written
before its effect, so the effect may not have happened. Every effect is repeatable because the
messages carry [ids made from the event](#write-ahead):

| Last logged decision | What the new coordinator does |
|---|---|
| Opened, before ADVISORY | Nothing. The request handed again starts the thread (an external thread waits for its `thread_start`) |
| ADVISORY entered | Nothing. The phase sweep runs the advisory step |
| EVALUATING entered, no subcommittee logged | Runs the advisory step again |
| Subcommittee convened, evaluation not started | Publishes the advisory, the subcommittee line and `advisory_ready` for the members already chosen. No second model call |
| Evaluation started, no agent has signalled | Publishes `advisory_ready` again |
| Evaluation started, signals in | Nothing. The sweep settles from the rebuilt roster and deadlines |
| Review roster written, REVIEW not entered | Enters REVIEW and publishes `review_ready` |
| REVIEW entered, no analyst has signalled | Publishes `review_ready` again |
| REVIEW entered, analysts have signalled | Nothing. The sweep settles |
| PAUSED | Stays paused. A `thread_resume` message resumes it |
| SYNTHESIZING, no answer logged | Runs the synthesis again |
| SYNTHESIZING by a direct answer, answer not logged | Writes the answer again from the logged brief |
| Answer logged (`synthesis_done`), no close | Publishes the answer and `thread_close`, then writes `thread_closed` |
| Closed | Nothing |

**The request.** The queue consumer passes each request's stream sequence to the coordinator,
and `thread_opened.requestSeq` ties a thread to it. A request handed again after a restart
finds its thread. If the thread is in flight, the request waits for its answer and the
question is not published again. If the thread already closed, the request returns the logged
answer, records the conversation turn once, and is acknowledged. A request whose thread was
closed unresumed starts a new thread. The queue consumer waits for the start-up read of the log
before it takes a request, up to a minute.

**What is not carried over.** A resumed thread's receipt, published on `thread_close`, lacks the
calls the coordinator itself placed before the restart. An agent that was working when the
coordinator died is not resumed; it finishes and signals, or its deadline passes and the
coordinator records a failure, as always.

**Limits to know.**

- Without leases a stop, pause or resume has no id in the log, so it is compared by its
  timestamp with the last logged event. A clock that differs between the dashboard and the
  coordinator can drop one or apply a stale one. With leases on it is compared by stream
  sequence and no clock is read.
- Without leases, a takeover refused because another coordinator wrote to the thread first
  leaves the thread alone, and a request for it, handed again, is not matched and starts a
  second thread while the other coordinator owns the first. With leases on, that request
  finds the thread in the log, whichever replica owns it.
- A thread opened before the log or resume was turned on has no derived ids on its first
  messages. After a restart it looks like a thread with an incomplete log and is closed.
  Turn the settings on while no thread is open.
- A message lost to a brief loss of the NATS connection while the coordinator runs is not
  read back; the stream is read only at start.

Without leases the live subscription stays a plain NATS subscription. JetStream is used to read
the log and the messages missed, once per thread, at start. With leases on the live feed is an
ordered consumer ([below](#leases-and-more-than-one-replica)).

Resume is counted in `kubemoot_coordinator_resumes_total{action}`, where the action is one
of the rows above (`sweep`, `rerun_advisory`, `announce_subcommittee`, `republish_advisory_ready`,
`enter_review`, `republish_review_ready`, `stay_paused`, `restart_synthesis`, `answer_directly`,
`finish_close`, `nothing`), `abandoned` for an incomplete log, `expired` for a thread past the age limit, `moved_by_replay`, `fenced`, or `failed`.

### Leases and more than one replica

With `coordinator.lease=true` (it needs `coordinator.eventLog` and `coordinator.resume`, both on by
default; the chart refuses it when you turned either off) more than one coordinator replica can run. The coordinator
Agent may set `spec.deployment.replicas` above 1, and it rolls with `RollingUpdate` instead of
`Recreate`: the new pod starts, the old pod gives its threads up as it stops, and the new pod
resumes them. Leases are the one coordinator setting that stays off by default, because they pay off only with more
than one coordinator replica; then a coordinator is one replica that rolls with
`Recreate`, as before, and nothing below applies.

The log decides and the lease avoids wasted work. The [fenced append](#one-coordinator-per-thread)
is still what keeps two replicas from both acting on a thread; the lease keeps them from trying.

**The lease.** A lease is a key in the `kubemoot_coordinator_leases` KV bucket, which the chart
creates when `coordinator.lease` is on, with a TTL of 15 seconds and history 1. The key is
`<namespace>.<crew>.<threadId>` and the value is the pod name.

- A replica takes a thread's lease with a create, which fails while another replica holds it,
  before it opens the thread or takes it up.
- It renews the lease every 5 seconds with a compare-and-set on the revision it last wrote, as
  long as the thread is open. A replica that has not renewed for 10 seconds (two thirds of the
  lease) treats the lease as lost, before the bucket can hand the key to anyone else.
- It gives the lease up when the thread closes and when the replica stops, so a rolling update
  hands threads over at once instead of after the TTL.
- A replica that learns it lost a lease, at a renewal or from a refused append, stops acting on
  the thread and forgets it. The call waiting for the thread's answer follows the thread to
  the replica that owns it (below).

**Who owns a thread.** Every replica hears every discussion message of its crew. It acts only on
the threads it holds a lease for and drops the rest, so no replica makes a blank thread of a
message it was not meant to handle.

- A request is pulled by one replica, which takes the thread's lease and owns the new thread.
- A `thread_start` that another publisher put on the bus (the dashboard's New Discussion) is
  heard by every replica; the one that wins the create owns the thread.
- A thread whose owner stopped renewing is taken over. Every 10 seconds, and at start, each
  replica reads the last hour of the coordinator log (the longer of an hour and the resume age
  limit), finds the open threads it does not hold, and tries to take each lease. The replica
  that wins resumes the thread through [the steps above](#resume): the lease is won before the
  thread's gate is shut and its messages are read, so none that arrives in between is missed.
  A thread is taken within the lease plus one scan, about 25 seconds, of its owner stopping.

**The request pump.** The queue consumer of a crew is one durable consumer, and one replica
at a time pulls from it: the one that holds the crew's `request-pump` lease. Only that replica
resets the consumer when it takes the lease, so replicas never delete it out from under each
other and the 409 churn does not return. The consumer still has one request in flight, so a
crew's requests are served one at a time; replicas add availability, not throughput.

**A request handed again.** A request redelivered after its replica stopped, or handed to the
pump of a new replica, carries the same stream sequence, and `thread_opened.requestSeq` ties
it to its thread in the log. With leases on the replica that is handed the request asks the log,
not only its own memory:

- No thread started by this request: the request is new.
- The thread closed: the request returns the logged answer, records the conversation turn once
  and is acknowledged.
- The thread is open and no replica holds its lease: this replica takes it over and the request
  waits for its answer.
- The thread is open and another replica holds the lease: the request waits, looking every
  2 seconds, until the thread closes (it then returns the logged answer) or the lease lapses (it
  then takes the thread over). A wait that outlasts the hard ceiling by two minutes fails the
  request, so it is handed again; it never starts a second thread for a request that has one.
- A request whose own thread was lost to another replica (its lease lapsed or its append was
  refused) is followed the same way, up to three times, instead of being acknowledged with no
  answer.

**Stop, pause and resume.** These messages have no id in the log, so a replica that takes a
thread over needs to know which of them the owner's decisions already account for. With leases
on, the coordinator follows the discussion subjects with an ordered JetStream consumer, so each
message arrives with its sequence in `KUBEMOOT_DISCUSS`, and every event it writes carries
`discussSeq`, the highest sequence it had applied for the thread. A replica taking a thread
over applies a stop, pause or resume whose sequence is greater than the thread's logged
`discussSeq` and skips the rest. No clock is read, so a dashboard whose clock is an hour off
cannot drop a fresh control or apply a stale one. A log written without sequences (leases were
off) falls back to the message's timestamp.

**Why the live feed is a consumer, and a plain subscription would not do.** A plain subscription
delivers every message to every replica with no sequence. A queue group would give each message
to one replica, which is not necessarily the owner. An ordered consumer per replica delivers every
message with its sequence, and recreates itself after a gap, so a brief loss of the NATS
connection loses nothing. Every replica still parses every message of its crew and drops the ones
for threads it does not hold; that cost is small for the few replicas this is meant for. If the
consumer cannot be created the feed falls back to a plain subscription, says so at WARN, and
controls are ordered by time as before.

**Limits to know.**

- The lease TTL is a bucket setting (15 seconds) that the runtime assumes; do not change the
  bucket's TTL.
- A thread started through the HTTP chat API (the synchronous bridge) has no request to follow.
  If its replica stops, another replica resumes the thread and the answer reaches the bus, but
  the HTTP call that asked is gone with the replica.
- A model call in flight on the replica that lost a thread is wasted: the replica that takes the
  thread runs the step again from the last logged decision. The answer is still published once.
- Run as many replicas as you need for availability, usually two. Every replica reads every
  message of its crew.

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

### Testing the coordinator

The [coordinator chaos test](../../operating/coordinator-chaos-test/) kills or freezes a
coordinator at chosen points of a real discussion (after the thread opened, the subcommittee
convened, evaluation or synthesis started, the answer was logged, the close was published) and
checks from the log and the streams that the question was answered once. It refuses to run
unless the log and resume (and, for its freeze and lease-holder cases, leases) are on.

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
| Artifact notices | `kubemoot.artifacts.<namespace>.<crew>.<threadId>` | `ArtifactNotice`: a tool result stored as a file in the artifact store |
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
