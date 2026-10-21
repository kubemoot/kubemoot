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
See [Models & Scheduling](../models-and-scheduling/#when-every-gpu-is-busy).

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
| Discussion thread | `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>` | `DiscussionMessage`: `messageId`, `threadId`, `agentName`, `messageType`, `content`, `channel`, `timestamp`, `metadata` |
| Agent lifecycle | `kubemoot.lifecycle.<namespace>.<crew>.<signal>.<agent>` | `DiscussionMessage` with `waking` or `ready` |
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
