---
title: "The Moot Mesh"
weight: 6
description: "The sidecar that sits between an agent's harness and everything else: the bus, the models, the tools, and the trace. What it guarantees, its three doors, and what it does not do yet."
---

> **Audience:** crew authors who bring their own harness; operator and sidecar developers; anyone deciding how much to trust a harness at the table.

The Moot Mesh is the layer that lets any agent harness sit at the table without
knowing how the table works. It follows the shape of a service mesh:

| Service mesh | Moot Mesh |
|---|---|
| Control plane | The Kubemoot operator. It writes each agent's contract, binds its models, creates its bus consumer, and builds its pod. |
| Data plane proxy (Envoy) | The Moot Mesh sidecar, one per agent pod. |
| The application | The harness: the program that does the agent's reasoning. |
| The mesh's shared services | The bus (NATS JetStream), observability, and identity. |

A harness does not connect to NATS, does not choose a model server, and does not
reach the tool gateway. It talks to three loopback addresses and its sidecar does
the rest. That is what makes a harness replaceable: the agent-runtime that ships
with Kubemoot, a command-line agent behind a thin shim, and a framework you wrote
yourself all join the table the same way.

In code and configuration the sidecar is called `agent-sidecar`; its image is
`images.agentSidecar` on the [KubemootConfig](../../reference/kubemootconfig-guide/).

## What the sidecar guarantees

**The sidecar is the only party on the bus.** A harness cannot publish a signal,
claim a ticket, write a heartbeat, or call a tool the agent may not use, because it
has no path to any of them. Whatever a harness says goes through the sidecar, which
decides what is published.

**The sidecar measures; the harness does not report its own numbers.** Tokens,
the models that served each call, the tools called, and the time a turn took are
counted where the calls pass through. A signal carries what the sidecar saw.

**Failure is always a signal, never silence.** A harness that errors, is canceled,
asks for input nobody can give, runs past the turn deadline, or returns an empty
answer produces a `failure` signal with a failure type. An empty reply is never
turned into a quiet stand-aside. See [Signals & Protocol](../../concepts/signals-and-protocol/).

**A long turn is never delivered twice.** Each agent has a durable JetStream
consumer with a fixed acknowledgement wait. While a turn runs, the sidecar reports
progress to JetStream on a fixed beat (a quarter of that wait by default), so a
turn that outlasts the wait is not redelivered while it is alive. A duplicate
delivery of a turn that already ran is recognized and dropped.

**Every published message is a bus-contract message.** The sidecar builds its
messages from the types generated from the [bus contract](../../concepts/signals-and-protocol/#the-bus-contract),
and its tests validate them against the schema. A harness written in any language
produces contract-conformant signals because it never writes one.

## The three doors

The harness meets the mesh at three doors, all on the pod's loopback interface
except the turn, which the harness serves.

| Door | Protocol | Address | Who serves it |
|---|---|---|---|
| Turn | A2A over HTTP+JSON | `http://127.0.0.1:<harness port>` (default 9000) | The harness. The sidecar is the client. |
| Model | OpenAI-compatible HTTP | `http://127.0.0.1:8081/v1` | The sidecar |
| Tools | MCP, streamable HTTP, stateless | `http://127.0.0.1:8082/mcp` | The sidecar |

The harness learns these through environment variables set by the operator:
`KUBEMOOT_MODEL_URL`, `KUBEMOOT_TOOLS_URL`, `KUBEMOOT_TURN_PORT`, and
`KUBEMOOT_NATIVE_TOOLS`. The [Agent reference](../../reference/agent/#specharness)
lists them.

### The turn door

When a discussion selects the agent, the sidecar sends the harness one A2A
`SendMessage` request: the question as a text part, and the **Kubemoot turn
profile** as a data part. The profile carries the agent's name, crew and role, the
discussion phase, the agent's assembled instructions, the earlier messages on the
thread, the model aliases it may name, a per-turn key, the turn deadline, the
current time, and the requester's `requestDate` and `timeZone` when the question
carried them.

The harness answers with the standard A2A reply. A text answer counts as an
agreement. A data part with a `signal` of `agree`, `concern`, `block`, or
`stand_aside`, plus `content`, `reason`, and `evidence`, lets the harness say more.
A rejected task is a stand-aside; every other non-completed state is a `failure`
with a failure type that names the state.

The harness serves its agent card at `/.well-known/agent-card.json`. The sidecar
finds the harness by fetching it, and the pod is ready only while it answers. A
harness that declares a different turn profile than the sidecar speaks is reported
and does not join the table.

### The model door

The harness calls an OpenAI-compatible chat completions endpoint. The `model` field
is an **alias**, not a model name: `default` for the agent's main model, `triage`
for its triage model. The contract maps each alias to a ranked list of acceptable
models. The **per-turn key** arrives as the API key; the sidecar uses it to attribute each call
to its turn.

Every call is placed by the placement service, which picks the model and the
accelerator at the moment of the call, the same just-in-time placement the
[scheduler](../scheduler/) does for legacy agents. When the service cannot be
reached, the call runs on the server the operator bound the alias to, the floor
the contract carries for each alias, and the turn's signal says it ran degraded.
A call the service says cannot fit anywhere is not overridden: the answer is
the service's decision. Responses stream back unbuffered, and the sidecar
releases the placement ticket when the call ends.

### The tools door

The harness speaks MCP to one endpoint. The sidecar lists only the tools the agent
may use, taken from the agent's `enabledTools` and `disabledTools`, and enforces
the same allow-list again on each call, so a harness that guesses a tool name does not
reach it. The sidecar forwards calls to the crew's MCP gateway.

If the agent declares tools the gateway does not list, the turn fails with
`failureType` `tools_unavailable` before the harness starts. A turn that cannot use
the tools it was promised does not run on guesses.

When the turn carries the requester's `requestDate` and `timeZone`, the sidecar
passes them to the tools in the call's `_meta`, so a tool computes "today" for the
person who asked.

## The pod

A sidecar-mode agent pod has two containers:

- **The sidecar**, added as a native sidecar: an init container with
  `restartPolicy: Always`. It starts before the harness and runs for the life of the
  pod. It has small fixed resource requests and limits, no privileges, no
  capabilities, and a read-only root file system.
- **The harness**, the pod's one regular container, named `agent`.

The probes are separate by design:

| Probe | What it checks |
|---|---|
| Sidecar startup | A TCP connect to the sidecar port. It passes when the sidecar listens, so the pod never waits on the harness. |
| Sidecar readiness (`/readyz`) | State: NATS is connected and the harness card answers. A restarting harness takes the pod out of rotation without restarting the sidecar. |
| Sidecar liveness (`/healthz`) | The sidecar process is up. |
| Harness readiness | The harness answers its agent card, however its image defines readiness. |

The harness gets no service account token and no NATS address. The operator mounts
the agent's contract, policy, and skills into the sidecar only.

### What the shared pod does and does not isolate

Containers in a pod share a network namespace. A harness that learns the NATS
address could dial it, and nothing in the pod stops the connection. The mesh
closes that gap with identity, not with a network wall:

- **Per-agent NATS credentials held only by the sidecar.** The credentials are
  mounted into the sidecar container and not into the harness. Once NATS requires
  credentials, a harness that reaches NATS without them is refused.
- **Subject permissions generated from the contract.** Each agent's NATS user is
  allowed to publish its own signals, its own heartbeat, and its crew's discussion
  subjects, and to read its crew's discussion, and nothing else. The permission
  set is computed from the agent's identity by `Identity.Permissions` in the shared
  mesh package, so the operator and the sidecar share one definition.
- **A sandbox runtime class for untrusted harnesses.** A harness you do not trust
  belongs under a sandbox runtime class, which gives it its own kernel boundary. The
  Agent spec has no field for the runtime class yet.

Minting the per-agent NATS users is not built yet (see
[What is not built yet](#what-is-not-built-yet)). The two ends of the credential path
exist: the operator mounts an optional Secret named `<agent>-nats` into the sidecar
only, and sets the contract's `bus.credentialsDir` to where it lands. The sidecar
reads a `creds` file, or a `user` and `password`, from it. With no Secret the
directory is empty and the sidecar connects anonymously, which is how a cluster
without NATS accounts runs today.

## Observability

Trace context follows a question through every hop. The sidecar carries W3C trace
context on the A2A request to the harness, on the model and tool calls the harness
makes back, and on every bus message, in the `traceparent` and `tracestate` fields
of the message and in the `traceparent` header of the NATS message. A trace opened
by the coordinator therefore continues through each agent's turn, its model calls,
and its tool calls.

When the contract names a collector (`observability.otlpEndpoint`, set from the
KubemootConfig's `defaults.otelCollectorEndpoint`), the sidecar exports spans over OTLP/HTTP.
With no collector, trace context still flows; nothing is exported.

Metrics are served on the sidecar port at `/metrics`. The chart keeps the existing
agent PodMonitor for legacy pods and adds a second one for pods labelled
`kubemoot.ai/harness-mode: sidecar`, so each kind of pod is scraped at its own path
with the same `crew` and `agent` relabelings.

Prometheus metrics are served on the sidecar port at `/metrics`. The scheduler
series keep the names agent-runtime uses, so existing dashboards keep working:

- `kubemoot_scheduler_calls_total`
- `kubemoot_scheduler_call_seconds`
- `kubemoot_scheduler_generated_tokens_total`
- `kubemoot_scheduler_capacity_wait_seconds`

The sidecar's own series start with `kubemoot_sidecar_`: turns and turn time,
signals published, progress acknowledgements, bus deliveries by result (turn,
duplicate, not selected, ignored), and requests to the model and tools doors. Every
series carries the same three labels, `namespace`, `crew`, and `agent`, whatever the
harness is. See [Observability](../../operating/observability/).

## Why the turn is A2A and the bus is not

[Agentic Consensus](../agentic-consensus/) explains why Kubemoot coordinates agents
over a message bus rather than agent-to-agent calls. That argument is about the
**discussion**: a facilitated broadcast where every agent sees every contribution
and the coordinator settles on what the crew reported. Point-to-point calls between
agents cannot give that.

The **turn** is a different shape. It is bilateral: one sidecar asks one harness one
question and waits for the answer. That is what A2A is for, so the sidecar uses it
as the client. A harness author implements a standard agent card and
`SendMessage`, and no part of Kubemoot's bus.

## What is not built yet

- **Direct chat.** The sidecar's `/api/chat` answers `501`. A sidecar-mode agent
  takes part in discussions only.
- **A `ContractMismatch` condition.** A harness that speaks a different turn profile
  is logged and kept out of the table, but the Agent's status does not carry a
  condition for it.
- **Per-agent NATS user minting.** The permission sets are generated; creating the
  NATS users and their credentials Secrets is not automated.
- **The injection webhook.** The mesh patch that adds the sidecar is one function the
  Agent controller calls for pods Kubemoot builds. A mutating webhook that joins
  pods Kubemoot did not build to the mesh is planned.
- **Learned placement policies.** The contract names the placement policies
  (algorithm, learned, shadow). Learned and shadow policies are not built.
- **`/debug/turns`.** A view of recent turns on the sidecar port does not exist yet.

## Related

- [Agent reference: `spec.harness`](../../reference/agent/#specharness) - the fields and the contract file.
- [Build a Crew: Choosing a harness mode](../../user-guides/build-a-crew/#choosing-a-harness-mode) - when to use it and what a harness must do.
- [The Table and the Harnesses](../../concepts/the-table-and-the-harnesses/) - where harnesses sit in a crew.
- [Signals & Protocol](../../concepts/signals-and-protocol/) - the signals the sidecar publishes.
