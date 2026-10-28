---
title: "Conversations, Turns & Threads"
weight: 15
description: "The units of interaction: a durable conversation, the turns within it, and the thread each turn's discussion runs on."
---

When you ask a crew a question, four words describe what happens, and they nest.
The docs and the dashboard use them with specific meanings, so this page defines
each one and how it relates to the others.

## The layers

```
  Conversation            durable session with a crew (conversationId)
    |
    +-- Turn              one question and the answer settled on it
    |     |
    |     +-- Thread      where that turn's discussion runs (threadId)
    |           |
    |           +-- Discussion   the deliberation on the thread (phases + signals)
    |
    +-- Turn ...          later questions, each on its own thread
```

A conversation holds many turns; each turn runs on its own thread; the discussion
is what happens on that thread. The two innermost words name the same layer from
different angles, which is explained below.

## Conversation

A **conversation** is a durable session between you and a crew, identified by a
`conversationId` (a UUID). Supply one to continue an existing conversation; omit it
and the crew generates a fresh one and returns it. The crew keeps a bounded history
of the conversation's recent turns as working context for follow-up questions, and
persists it so the session survives a coordinator restart. Every answer carries the
`conversationId` back to you.

## Turn

A **turn** is one exchange within a conversation: your question and the answer the
crew settled on. The conversation records each turn together with the `threadId` that
produced it and a timestamp, and keeps a bounded window of the most recent turns
(older ones age out).

A turn is the conversation-level unit. It is not the same as the iterations a single
agent makes while calling tools and mulling on its way to a contribution - those
happen *inside* one turn's discussion and are not turns themselves.

## Thread

Each turn runs on its own **thread**, identified by a fresh `threadId` (a new UUID per
question). The thread is the channel the deliberation runs on: agents exchange
messages on NATS subjects of the form `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>`,
bracketed by a `thread_start` and a `thread_close`.

Because a thread is created per question, `conversationId` and `threadId` are always
different values: one conversation spans many threads, one per turn. A concluded
thread can be reopened for a further round of deliberation rather than being treated
as immutable.

### The thread's receipt

When a thread settles, the coordinator puts a **receipt** on its `thread_close` message,
as `metadata.receipt`: what the thread cost and how it went, from the coordinator's view.

- **Time:** start and settle times, the total, time to the first agent signal, and the
  time in each phase (advisory, selection, evaluation, synthesis).
- **Consensus:** how many agents were convened (the coordinator's selection, or the agents
  that signalled when more did) and how many agreed, raised a concern, blocked, stood
  aside, or failed.
- **Tokens:** the agents' calls, the coordinator's own calls, and the totals.
- **Agents:** one line per agent with its last signal, when it came, where its calls ran
  (`model@server`), its tokens and time in model calls, how long it waited for an
  accelerator, and the reason it gave for standing aside or the error it failed with.
- **Placements:** how many agents' calls ran at each `model@server`.
- **Calls:** every model call the scheduler placed, the coordinator's and the agents',
  each with the policy that placed it, the reason, the other servers it weighed, the
  completion time it predicted and what the call measured. See
  [Reading a placement decision](../../architecture/scheduler/#reading-a-placement-decision).

Times are from when the coordinator received each message, and the receipt is a snapshot
at the close: a message that arrives after it is not counted. Agents may signal before
the triage result reaches the coordinator, so evaluation starts at whichever comes first.
An agent that signals more than once in a round has its tokens and calls added up, with
its last signal shown. A reply that reopens the thread starts a new round, and the next
`thread_close` carries that round's receipt. The schema is `DiscussionReceipt` in the bus
contract.

## Discussion

**Discussion** and **thread** name the same layer from two angles. The *thread* is the
channel and the identifier you see in streaming events, traces, and the dashboard; the
*discussion* is the deliberation that runs on it - the coordinator convening the crew,
agents contributing findings and consensus signals, and the answer being synthesized.
There is no separate discussion object; a thread advancing through its phases *is* the
discussion.

How that deliberation is organized - its phases, its signal vocabulary, how it
settles, and the fact that the structure is a pluggable archetype - is covered in
[The Moot](../consensus-model/).

## In the API and the dashboard

The crew's discussion endpoint returns a `conversationId` with every answer; echo it
back on your next message to continue the same conversation. Stream the deliberation
with `GET /api/v1/discussions/{crew}/{conversationId}/stream` and the thread is surfaced directly (a `thread_found` event,
followed by per-phase events) as it unfolds. See [Quickstart](../../introduction/quickstart/)
for the request and response shapes, and [Dashboard](../../operating/dashboard/) for
the live `/discussions` view that renders each thread. Every event of the stream, and
the request and response bodies of these endpoints, are defined in the
[bus contract](../signals-and-protocol/#the-bus-contract): `StreamEvent`,
`PostDiscussionRequest`, `PostDiscussionResponse`, `ConversationList` and
`ErrorResponse`.

The stream survives restarts. The POST reply also carries `requestedAt`; pass it as
`?since=` on the stream so any gateway replica can tell this turn's thread from an
earlier turn's. Each thread event carries an SSE `id`; a client whose connection drops
reconnects with that id as the `Last-Event-ID` header (or the `lastEventId` query
parameter) and receives only what it missed. If the coordinator is replaced while it
works on a question, its successor starts the question again on a new thread, and the
stream announces it with a second `thread_found`; the earlier thread will not close.

### The requester's date and time zone

A server can run in another time zone than the person asking, so a turn carries where the person is. A client sends the person's IANA time zone and local date and time with the question:

| Where | Field | Example |
|-------|-------|---------|
| `POST /api/v1/discussions/{crew}` body | `timeZone`, `localDateTime` (both optional) | `America/Los_Angeles`, `2026-10-24T09:15:00` |
| `thread_start` metadata (a client that publishes the thread itself) | `requester.timeZone`, `requester.localDateTime` | the same values |

`localDateTime` is ISO 8601 local time without an offset; its date part is the date that matters most. The dashboard sends the browser's zone and clock, and `kmctl conversation ask` sends the machine's. The gateway queues the two values with the request, the coordinator puts them in the `requester` object of the `thread_start` metadata, and every agent keeps them for the life of the thread. A value that is not a known IANA zone or not an ISO local date-time is dropped, so a bad value behaves like an absent one, and a client that sends neither behaves as before. The fields are additive in the [bus contract](../signals-and-protocol/#the-bus-contract) (`PostDiscussionRequest`, `DiscussionRequest`, and `Requester` in `DiscussionMetadata`).

Agents pass the requester to every tool call of the thread as the MCP request's `_meta`; see [Request metadata](../../reference/mcpgateway-guide/#request-metadata-_meta) and [how a tool reads the requester's date](../../user-guides/onboard-mcp-tools/#read-the-requesters-date-in-a-tool).

### Activity on the stream

Some steps run for minutes without a new phase event: a model loading onto a GPU, an
agent waiting in the GPU queue, a long tool-calling evaluation, the coordinator
synthesizing the answer. So a client can tell a crew that is working from one that has
stopped, the stream sends an `activity` event every 15 seconds while anyone is busy on
the thread, and sends none while no one is:

```json
{"type":"activity","at":"2026-10-15T12:04:37Z","agents":[
  {"agent":"compute","doing":"evaluating","gpu":"ollama-rig0","since":"2026-10-15T12:02:36Z"},
  {"agent":"rules","doing":"waiting_for_accelerator","model":"qwen3:14b","since":"2026-10-15T12:03:10Z"}]}
```

| `doing` | Meaning |
| --- | --- |
| `waking` | The agent is starting up for the thread. |
| `triaging` | The agent is deciding whether it has something to add. |
| `evaluating` | The agent is running its tool-calling evaluation (model calls and tool calls), on `gpu`. |
| `waiting_for_accelerator` | The agent is queued for a GPU with room for `model`. |
| `reasoning` | The coordinator is choosing which agents to convene. |
| `waiting_for_agents` | The coordinator has convened agents that have not reported yet. |
| `synthesizing` | Every agent is done and the coordinator is writing the answer. |

The gateway derives activity from the thread's own messages (each agent's phase, and
the discussion heartbeat an evaluating agent publishes every 30 seconds) and counts an
agent only while it is alive: its liveness heartbeat in the `kubemoot_agent_state`
bucket is no older than two of its intervals, or it published on the thread within the
last 75 seconds. The coordinator is listed only when no agent is busy. Activity events
carry no SSE `id`; they describe the present, so a reconnecting client is not sent the
ones it missed. The `heartbeat` event, every 30 seconds, only keeps the connection open
and says nothing about the crew.

### Timestamps and signal detail

Every event the gateway makes from a discussion message carries `timestamp`, when the
message was published (RFC 3339, UTC): the message's own timestamp, or the time NATS
stored it when the message has none. `thread_found` carries the thread start's. Step
durations computed from these are the crew's own, not the time each event reached the
client. The gateway's own events (`connected`, `heartbeat`, `activity`, `error`) carry
none; `activity` has `at` instead.

`finding` events and the stand-aside `phase` event also carry the signal's metadata,
each field only when the agent sent it:

| Field | Meaning |
| --- | --- |
| `modelName` | The model the agent called. |
| `inputTokens`, `outputTokens`, `totalTokens` | Token counts of the agent's model calls for the signal. |
| `inferenceMs` | Time spent in model calls. |
| `gpu` | The accelerator the agent ran on (findings; phase events already had it). |
| `toolsUsed` | The tools the agent has enabled, sent with `agree`. It lists what the agent could call, not the calls it made. |
| `artifact` | `{"key", "bytes"}` of a contribution spilled to the artifact store. |
| `failureType`, `failedTool`, `failureCount`, `lastError` | For a `failure`: the kind of failure, the tool that failed, how many tool calls failed, and the last error. |

```json
{"type":"finding","agent":"pilot-proxmox","signal":"agree","gpu":"rig1-gpu0",
 "summary":"The cluster has three Proxmox nodes, all online.","content":"...",
 "timestamp":"2026-10-15T09:00:16.4Z","modelName":"qwen3:14b",
 "inputTokens":5210,"outputTokens":640,"totalTokens":5850,"inferenceMs":9800,
 "toolsUsed":["proxmox_nodes","proxmox_vms"]}
```

### The subcommittee

When the coordinator decides which agents to convene, the stream sends a
`subcommittee` event with the convened agents in `members`. The coordinator's
advisory produces one with `source` `advisory`, which also carries the brief the
coordinator wrote for the agents, its confidence, and the technologies it named.
When the coordinator chooses by triage, its triage result produces one more, before
the advisory's, with `source` `triage` and each agent's `reason` and `confidence`.
When the coordinator convened no subcommittee, `allAgents` is true and every agent
may take part. The coordinator does not publish the agents it left out, so the
event does not name them.

```json
{"type":"subcommittee","agent":"homelab-pilot-coordinator","source":"advisory",
 "members":[{"agent":"pilot-metrics"},{"agent":"pilot-proxmox"}],
 "brief":"List each Proxmox node with its role, then flag capacity problems.",
 "confidence":0.9,"technologies":["proxmox"],"timestamp":"2026-10-15T09:00:04Z"}
```

### Listing conversations

`GET /api/v1/discussions/{crew}` lists the crew's recent conversations, newest first by
their latest turn, at most `limit` (default 20, at most 100):

```json
{"conversations":[{"conversationId":"6733c361-aa0c-4bde-85c6-e143af23b251",
  "question":"Describe the proxmox nodes.","startedAt":"2026-10-15T09:00:00Z",
  "lastTurnAt":"2026-10-15T09:12:40Z","turns":2}]}
```

The gateway reads the crew's `thread_start` messages from the discussion stream, so
the list covers what the stream still holds (about a day). `question` is the first
question the stream holds for the conversation, and `turns` counts its threads; a
question the coordinator started again counts twice.

### Fetching an artifact

`GET /api/v1/discussions/{crew}/artifact?key=<key>` returns an artifact's bytes as text,
for the `artifact.key` a finding names. The key must belong to the crew in the
gateway's namespace (`<namespace>/<crew>/<thread>/<agent>/<object>`); any other key is
answered as not found. The coordinator deletes a thread's artifacts soon after the
thread closes ([Discussion Artifact Store](../discussion-artifact-store/)), and the
gateway then answers `404`.

Both endpoints are reached the same way as the stream, through the crew's
`<crew>-discussion` Service, so through the API server's service proxy they need the
same permission (`services/proxy` in the crew's namespace).

## Related

- [Agent Context](../agent-context/) - what the conversation and the thread contribute to an
  agent's model context.
- [The Moot - Consensus Model](../consensus-model/) - how a discussion is organized and settles.
- [Crews & Agents](../crews-and-agents/) - the team a conversation talks to.
- [Signals & Protocol](../signals-and-protocol/) - the messages exchanged on a thread.
- [Discussion Artifact Store](../discussion-artifact-store/) - how large tool outputs are passed by reference within a discussion.
- [Quickstart](../../introduction/quickstart/) - open a conversation and ask a question.
