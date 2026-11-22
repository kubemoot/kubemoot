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
`conversationId` (a UUID by default). Supply one to continue an existing conversation (1 to
128 letters, digits, `-` or `_`; the gateway answers 400 for any other id); omit it
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
`PostDiscussionRequest`, `PostDiscussionResponse`, `ConversationList`,
`ConversationEvent`, `ConversationControl`, `ConversationErasure` and `ErrorResponse`.

The stream survives restarts. The POST reply also carries `requestedAt`; pass it as
`?since=` on the stream so any gateway replica can tell this turn's thread from an
earlier turn's. Each thread event carries an SSE `id`; a client whose connection drops
reconnects with that id as the `Last-Event-ID` header (or the `lastEventId` query
parameter) and receives only what it missed. If NATS restarts while a stream is open, the
gateway waits for NATS and continues the stream after the last message it handled, so an
answer published once NATS is back still arrives; if NATS does not come back within five
minutes the stream ends with an `error` event, never silently. The coordinator likewise
waits for NATS and JetStream before it publishes the answer and the close. If the coordinator is replaced while it
works on a question, its successor starts the question again on a new thread, and the
stream announces it with a second `thread_found`; the earlier thread will not close.

### The requester's date and time zone

A server can run in another time zone than the person asking, so a turn carries where the person is. A client sends the person's IANA time zone and local date and time with the question:

| Where | Field | Example |
|-------|-------|---------|
| `POST /api/v1/discussions/{crew}` body | `timeZone`, `localDateTime` (both optional) | `America/Los_Angeles`, `2026-10-24T09:15:00` |
| The crew liaison's `ask` tool | `timeZone` (optional argument) | `America/Los_Angeles` |
| `thread_start` metadata (a client that publishes the thread itself) | `requester.timeZone`, `requester.localDateTime` | the same values |

`localDateTime` is ISO 8601 local time without an offset; its date part is the date that matters most. `kmctl conversation ask` sends the machine's zone and clock, and a web app that fronts a crew sends the browser's (the dashboard is read-only and asks nothing). The gateway queues the two values with the request, the coordinator puts them in the `requester` object of the `thread_start` metadata, and every agent keeps them for the life of the thread. A value that is not a known IANA zone or not an ISO local date-time is dropped (the gateway checks the zone against the IANA time zone database and refuses only a malformed `localDateTime`), so a bad value behaves like an absent one, and a client that sends neither gets the crew's default zone, then UTC. The client's `localDateTime` is kept for tools but never drives the date; the coordinator's clock in the resolved zone does, so a client with a wrong clock cannot move the date. The zone is carried with the question like the person's identity and is never stored beyond the thread.

When the coordinator takes the question off the queue it fixes the asker's date, once, from the moment the gateway queued it: in the client's zone, else the crew's `defaultTimeZone`, else UTC. The result, `requester.requestDate` (`YYYY-MM-DD`) beside `requester.timeZone`, rides on `thread_start` and `advisory_ready`, so a discussion that crosses midnight keeps the date it was asked on. Every agent's turn opens with it as [one line of context](../agent-context/#the-turn-context-line). The fields are additive in the [bus contract](../signals-and-protocol/#the-bus-contract) (`PostDiscussionRequest`, `DiscussionRequest`, and `Requester` in `DiscussionMetadata`).

Agents pass the requester to every tool call of the thread as the MCP request's `_meta`; see [Request metadata](../../reference/mcpgateway-guide/#request-metadata-_meta) and [how a tool reads the requester's date](../../user-guides/onboard-mcp-tools/#read-the-requesters-date-in-a-tool).

### Activity on the stream

Some steps run for minutes without a new phase event: a model loading onto an accelerator, an
agent waiting in the accelerator queue, a long tool-calling evaluation, the coordinator
synthesizing the answer. So a client can tell a crew that is working from one that has
stopped, the stream sends an `activity` event every 15 seconds while anyone is busy on
the thread, and sends none while no one is:

```json
{"type":"activity","at":"2026-10-15T12:04:37Z","agents":[
  {"agent":"compute","doing":"evaluating","accelerator":"ollama-rig0","gpu":"ollama-rig0","since":"2026-10-15T12:02:36Z"},
  {"agent":"rules","doing":"waiting_for_accelerator","model":"qwen3:14b","since":"2026-10-15T12:03:10Z"}]}
```

| `doing` | Meaning |
| --- | --- |
| `waking` | The agent is starting up for the thread. |
| `triaging` | The agent is deciding whether it has something to add. |
| `evaluating` | The agent is running its tool-calling evaluation (model calls and tool calls), on `gpu`. |
| `waiting_for_accelerator` | The agent is queued for an accelerator with room for `model`. |
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
| `accelerator` | The accelerator the agent ran on (findings; phase events already had it). |
| `gpu` | Deprecated old name of `accelerator`, sent with it in this release and removed in the next. |
| `toolsUsed` | The tools the agent has enabled, sent with `agree`. It lists what the agent could call, not the calls it made. |
| `failureType`, `failedTool`, `failureCount`, `lastError` | For a `failure`: the kind of failure, the tool that failed, how many tool calls failed, and the last error. |

```json
{"type":"finding","agent":"pilot-proxmox","signal":"agree","accelerator":"rig1-gpu0","gpu":"rig1-gpu0",
 "summary":"The cluster has three Proxmox nodes, all online.","content":"...",
 "timestamp":"2026-10-15T09:00:16.4Z","modelName":"qwen3:14b",
 "inputTokens":5210,"outputTokens":640,"totalTokens":5850,"inferenceMs":9800,
 "toolsUsed":["proxmox_nodes","proxmox_vms"]}
```

### The subcommittee

When the coordinator decides which agents to convene, the stream sends a
`subcommittee` event with the convened agents in `members`. The coordinator's
advisory produces one with `source` `advisory`, which also carries the brief the
coordinator wrote for the agents (one text; each agent reads its own task, see
[Compose a Crew](../../user-guides/compose-a-crew/#one-task-per-member)), its confidence, and the technologies it named.
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

A conversation can be taken away as one zip; see [Taking a conversation away](../../operating/data-ownership/#taking-a-conversation-away). The discussion gateway does not serve it: the operator does, beside the memory export, because it checks the caller's Kubernetes access.

`GET /api/v1/discussions/{crew}` lists the crew's recent conversations, newest first by
their latest turn, at most `limit` (default 20, at most 100):

```json
{"conversations":[{"conversationId":"6733c361-aa0c-4bde-85c6-e143af23b251",
  "question":"Describe the proxmox nodes.","startedAt":"2026-10-15T09:00:00Z",
  "lastTurnAt":"2026-10-15T09:12:40Z","turns":2}]}
```

The gateway reads the crew's messages from the discussion stream and keeps the
`thread_start` ones (the message type is not in the subject), so the list covers what the
stream still holds (about a day, at most 5,000 messages across all crews, which bounds the
read). Only a coordinator's `thread_start` (always with a conversation) and a person's
(which has no conversation yet) start conversations; agents never publish one. `question` is the first
question the stream holds for the conversation, and `turns` counts its threads; a
question the coordinator started again counts twice. A thread a person starts with no
conversation (the dashboard does) lists as a one-turn conversation whose ID is the thread's
ID, and streaming that ID follows the thread.

### Hearing that a conversation has something new

A conversation that has gone quiet can come back: a scheduled reminder fires, a follow-up the crew scheduled reopens it, or an answer arrives after a client stopped waiting. `GET /api/v1/discussions/{crew}/events` is one event stream for all of a caller's conversations, so a web client subscribes once and fetches the conversation named by each event, instead of polling each one. No client subscribes to NATS: the gateway reads the discussion stream and tells each caller only what concerns them.

Each event is a `ConversationEvent`, sent as the data of a server-sent event:

```
id: 4182
data: {"type":"conversation_updated","conversationId":"6733c361-aa0c-4bde-85c6-e143af23b251","reason":"reminder","threadId":"9f3e...","at":"2026-10-15T10:30:00Z"}
```

`reason` is `answer` (a synthesis, so a late answer), `reminder` (a scheduled reminder fired) or `followup` (a scheduled follow-up reopened the conversation or started a turn). An event carries no content, only which conversation to fetch and why; a reply a person typed is not an event, since that person is already looking. `connected` is sent once the stream is open and `heartbeat` every 30 seconds. The `id` of an event is the sequence of the discussion stream message it came from: reconnect with it as `Last-Event-ID` (or `lastEventId`) and the stream continues after it, as far back as the discussion stream still holds.

Who hears what follows [who owns a conversation](#who-owns-a-conversation): a caller receives events for the conversations they own and no others, and an operator who may `list` `crews/conversations` receives every conversation's with `?all=true` and a `Kubemoot-Access-Reason` header (audited once, when the stream opens). On a crew that verifies no one there are no owners, so the stream carries every conversation's events, as the list shows every conversation. A conversation with no owner record is heard of by an operator only, so a scheduled thread that began on its own (a reminder whose source thread had expired) reaches no owner. If the owner of an event cannot be checked, the stream ends and the client reconnects with its last `id`; the event is sent again, never dropped. A conversation whose `thread_start` the discussion stream no longer holds cannot be matched to its conversation, so its events are not sent. An operator's right is checked when the stream opens, so a revoked right takes effect when the stream reconnects. An `answer` event also fires for the answer of a turn the client is following on `/{conversationId}/stream`; treat an event as "fetch the conversation", which is safe to repeat.

Each open event stream holds one consumer on the crew's discussion subjects and reads every message of the crew; plan for as many streams as there are open browser tabs.

### Stopping, pausing, resuming and deleting a conversation

Four authenticated endpoints act on a conversation by its id, each checked by the [owner rule](#who-owns-a-conversation):

| Endpoint | What it does |
|----------|--------------|
| `POST /api/v1/discussions/{crew}/{conversationId}/stop` | Makes each running discussion of the conversation synthesize now, from the signals it has |
| `POST /api/v1/discussions/{crew}/{conversationId}/pause` | Pauses each running discussion |
| `POST /api/v1/discussions/{crew}/{conversationId}/resume` | Resumes each paused discussion |
| `DELETE /api/v1/discussions/{crew}/{conversationId}` | Erases the conversation from every store that holds it |

Stop, pause and resume find the conversation's threads in the discussion stream and publish the message the coordinator already understands (`stop_requested`, `thread_pause`, `thread_resume`) on each running thread's own subject. The gateway sends it as itself, `discussion-gateway`, never as a person, and the coordinator handles it exactly as it handled the dashboard's old controls. The answer is a `ConversationControl`: the action and the threads it was sent to. A conversation with no thread the stream holds is `404`; one with nothing running is `409`. A control only asks: a thread already past synthesis ignores a stop, and the coordinator decides.

Delete answers a `ConversationErasure`, which says what it removed from each store and what it cannot remove. It refuses with `409` while a discussion runs, because the coordinator would write its answer into the history again; stop it first, or pass `?force=true`, which stops each running discussion before it erases and leaves nothing to write the answer again unless the stop is ignored (a discussion already past synthesis); delete again once it has closed if the history reappears. A delete that could not clear a store answers `500` with the same body and the stores in `failed`; the owner record is removed last, so the owner can repeat it. A second delete of a deleted conversation is `404`. The stores, and what is kept, are listed in [Answering an erasure request](../../operating/data-ownership/#answering-an-erasure-request).

On a crew that verifies no one there are no owners, so any caller who can reach the gateway can stop, pause, resume or delete any conversation, as any caller can read any of them; put the gateway behind your own access control, or turn on `spec.discussion.access`.

### Keeping a person's own token for the turn

A crew whose [service token verifier](../who-is-asking/#subscribers) has `passToTools: true` keeps the bearer token of each person who asks, in the gateway's memory, so a tool the crew calls for that question can call its service as that person. Two routes belong to this and nothing else returns the token.

**Renewal.** `PUT /api/v1/discussions/{crew}/{conversationId}/credential` replaces the token held for a conversation with the one in the request's `Authorization: Bearer` header. A chat app whose token was refreshed during a long answer sends the fresh one. The normal access check applies, and the caller must be the person who started the conversation.

| Answer | Meaning |
|--------|---------|
| `200` | The grant is in force. The body is `{"conversationId": "...", "expiresAt": "..."}`, the time it ends. A token that ends earlier than the one already held does not replace it, and the answer still carries the time of the one in force |
| `401` | The token is missing, not accepted, or has no time left |
| `403` | The caller is not the conversation's owner, or the caller is a Kubernetes caller, or this crew keeps no credentials for tools |
| `404` | No conversation with that id has an owner |
| `409` | Another caller's grant holds the id |
| `503` | The gateway holds as many grants as it may (1000 by default), or an access check could not be made |

A renewal also restores a grant the gateway lost when it restarted. The same holds when a person asks: the token of a verified person is kept before the question is queued, and a Kubernetes token never is.

**Lease.** `POST /internal/v1/lease` is how a crew's MCP gateway gets the token for one tool call. It is served only on a separate internal port (`INTERNAL_PORT`, default `8081`), never on the port the Service for people exposes, and only when `ACCESS_BROKER_SUBJECT` names the one ServiceAccount allowed to ask. The caller sends its projected ServiceAccount token, bound to the audience `kubemoot-credential-broker`, as the bearer; the gateway checks it with a TokenReview and refuses any other ServiceAccount. The body names the turn:

```json
{"conversationId": "...", "principalId": "...", "verifier": "oreilly"}
```

| Answer | Meaning |
|--------|---------|
| `200` | `{"token": "...", "expiresAt": "..."}` with `Cache-Control: no-store`. The caller uses the token for one upstream request and does not keep it |
| `400` | The body is not the three fields, or the conversation id is not valid |
| `401` | No ServiceAccount token, one that is not valid, or one not bound to the audience |
| `403` | The ServiceAccount is not the one allowed, or the principal or verifier does not match the grant's |
| `404` | The conversation holds no grant. The gateway may have restarted; the next renewal restores it |
| `410` | The grant has expired |
| `503` | The ServiceAccount token could not be checked |

A grant ends at the earliest of the token's own expiry and 30 minutes after it was written or renewed. It is also dropped when the conversation's synthesis or done event goes out on a stream the owner is reading (an operator reading it changes nothing), when the conversation is stopped, and when it is deleted. Ending on a stream event means a replayed stream of an older turn can drop a newer turn's grant; the next renewal restores it. The token is never published on the bus, written to a log or a store, or sent in any other response. What is held, where and for how long is in [Data ownership](../../operating/data-ownership/#a-persons-own-token-during-a-turn).

### Fetching a stored result

`GET /api/v1/discussions/{crew}/workspace?key=<key>` returns a stored result's bytes as text,
for the `artifact.key` a finding names. The older `GET /api/v1/discussions/{crew}/artifact?key=<key>`
answers the same for one more release, with a `Deprecation: true` header and a `Link` to the new
route; it is removed in the next release. The gateway reads the workspace bucket first and the
bucket the workspace was stored in before it was renamed second. The key must belong to the crew in the
gateway's namespace (`<namespace>/<crew>/<thread>/<agent>/<object>`); any other key is
answered as not found. The coordinator deletes a thread's artifacts soon after the
thread closes ([The workspace](../workspace/)), and the
gateway then answers `404`.

Both endpoints are reached the same way as the stream, through the crew's
`<crew>-discussion` Service, so through the API server's service proxy they need the
same permission (`services/proxy` in the crew's namespace).

### Who owns a conversation

A crew whose gateway [verifies callers](../who-is-asking/) (`spec.discussion.access`) gives each conversation an owner: the principal that asked its first question. The gateway records it in the conversations bucket, under `own.<namespace>.<crew>.<conversationId>`, next to the conversation's history and under the same retention: the record is rewritten with every question and removed by the crew's cleanup when the history is. It holds the owner's pseudonymous id and kind, never a username, an email address or a token.

| Operation | The owner | An operator | Anyone else |
|-----------|-----------|-------------|-------------|
| Ask on the conversation | yes | no: an operator reads other callers' conversations and does not add turns to them | `404` |
| List | their own | `?all=true` lists every conversation of the crew, with each `owner` id | their own |
| Stream or resume | yes | yes | `404` |
| Stop, pause or resume | yes | yes, with a reason and the update right | `404` |
| Delete | yes | yes, with a reason and the delete right | `404` |
| Events (`/events`) | their own conversations' | every conversation's with `?all=true` and a reason | their own |
| Fetch a stored result (`/workspace`) | results of threads in their conversations | yes | `404` |
| Export the conversation (the operator's report service, with the caller's own token) | yes | yes, with a reason | refused (`403`) |

A conversation that is not yours is answered exactly as one that does not exist, so an id cannot be probed. An id you choose that nobody owns starts a new conversation owned by you; an id whose history is still held but whose owner record is gone cannot be claimed. A conversation with no record is listed to no one and is readable only by an operator. That includes conversations that began before a crew turned on `access`: their owners cannot read them, so turn `access` on when no conversation of value is in flight, or expect an operator to read what is left. The owner record is rewritten when a question is accepted and the history when its turn completes, so for the length of one turn the record is slightly older than the history; set the conversation window well above the longest turn.

**The operator role** is an ordinary RBAC right on a virtual resource: `get` (to read one conversation) or `list` (for `?all=true`) on `crews/conversations` in the crew's namespace. The chart ships the ClusterRole `kubemoot-conversation-reader`; nobody is bound to it by default. Stopping, pausing and resuming another caller's conversation is `update` on the same resource, and deleting it is `delete`; the chart ships these as the separate ClusterRole `kubemoot-conversation-controller`, so the role that reads conversations does not change them. An operator who uses it must say why, in the `Kubemoot-Access-Reason` header (1 to 200 characters), and every such access is written to the gateway's log as an audit record with the fixed event name `kubemoot.conversation.access`: the operator, the operation, the conversation, the owner's id and the reason. Kubemoot builds no audit store; route the log to the backend the installer keeps audit records in.

Anonymous callers of a crew that accepts them share one anonymous principal: they see and continue anonymous conversations and never another principal's. A crew with no `access` has no owners, so nothing here applies and every conversation is open as before.

## Related

- [Who is asking](../who-is-asking/) - how a crew verifies the caller.
- [Agent Context](../agent-context/) - what the conversation and the thread contribute to an
  agent's model context.
- [The Moot - Consensus Model](../consensus-model/) - how a discussion is organized and settles.
- [Crews & Agents](../crews-and-agents/) - the team a conversation talks to.
- [Signals & Protocol](../signals-and-protocol/) - the messages exchanged on a thread.
- [The workspace](../workspace/) - how large tool outputs are passed by reference within a discussion.
- [Quickstart](../../introduction/quickstart/) - open a conversation and ask a question.
