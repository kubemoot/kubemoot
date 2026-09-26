---
title: "Agentic Consensus"
weight: 2
description: "How a coordinator convenes a subcommittee, why failure is a signal, and how a discussion settles."
---

Kubemoot answers a question by convening the agents whose expertise fits it, letting
them investigate and deliberate, and settling on a synthesis once the crew has spoken.
This page is the architecture behind that: how the coordinator selects who takes part,
what a consensus signal means to the protocol, and why a discussion settles by state
rather than by a clock. For the concept-level model (roles, phases, signal vocabulary)
see [Crews & Agents](../../concepts/crews-and-agents/),
[The Moot](../../concepts/consensus-model/), and
[Signals & Protocol](../../concepts/signals-and-protocol/) - this page assumes them and
goes one level deeper into the coordinator's mechanics.

## Why a message bus, not a shared process

Most multi-agent frameworks run every agent inside one process: agents, memory, and
tool orchestration all live in one application's runtime and coordinate through shared
in-memory state. Kubemoot takes the opposite approach. Each agent is an independent
Kubernetes pod, and agents never call each other directly - they coordinate entirely by
publishing to NATS JetStream. The LLM library inside each agent pod does one narrow
job: it wraps the HTTP call to the model provider and formats tool specifications for
the model. The phased protocol, the consensus signals, gap detection, and onboarding are
Kubemoot's own code running over NATS, not framework machinery.

This distribution has consequences worth naming plainly:

| Concern | Shared-process model | Kubemoot (distributed) |
|---------|-----------------------|-------------------------|
| Scaling | All agents scale together | Each agent scales independently |
| Failure isolation | One agent crashing can take down the process | An agent crash doesn't affect others; NATS persists messages so it can rejoin |
| Resource limits | One shared memory pool | Per-agent CPU/memory limits |
| Deployment | Redeploy the whole process for any agent change | Update one agent's image or prompt independently |
| State durability | In-memory, lost on crash | NATS JetStream retention; a discussion survives a coordinator restart |
| Observability | Shared logs, hard to attribute | Per-agent logs and per-call provider attribution |

The tradeoff is real: a message bus makes agents coordinate by explicit signal instead
of shared context, which is more moving parts than one process reasoning over one
context window. Kubemoot accepts that cost for independent scaling, failure isolation,
and the ability to run many small models instead of depending on one large one.

## The discussion table

Every discussion happens on NATS JetStream subjects,
`kubemoot.discuss.<crew>.<channel>.<threadId>`. A question doesn't get routed to one
channel and hope the right agents are subscribed there; the coordinator selects a
**subcommittee** first, then broadcasts `thread_start` to every channel with that
subcommittee (the `innerCircle`) attached. Only the selected agents evaluate the
question. The rest see the broadcast go by and take no action.

```
   Coordinator ──broadcast thread_start──► every channel
        │
        │  selects subcommittee via the resume model:
        │  embeds the question, queries the crew's resume index,
        │  picks the closest-matching 1-5 agents
        ▼
   advisory_ready { innerCircle: [k8s-workloads, k8s-helm] }
        │
        ▼
   k8s-workloads, k8s-helm  → evaluate, call tools, publish a signal
   every other agent        → sees the broadcast, not in innerCircle, takes no action
```

### How the subcommittee is chosen

Selection is driven by each agent's **resume** - its description, keywords, tools,
role, and discussion channels. At crew-reconcile time the operator compiles a resume
for every agent, writes the set to a NATS KV bucket keyed by crew, and provisions a
per-crew `RAGSource` that embeds each resume into its own vector collection. This
happens at deploy time and is hash-gated: resumes are re-embedded only when one
changes, so the index is ready before the first question is ever asked.

When a question arrives, the coordinator embeds it and queries that collection for the
agents whose resumes are semantically closest - a vector pre-filter that runs *before*
the broadcast. Because keywords live inside each resume, this semantic match subsumes
literal keyword matching: a question about GPU "activity" matches an agent whose resume
says "utilization" with no shared keyword. If the resume query service is unavailable,
the coordinator falls back to an LLM triage call over the same resumes; either way,
selection is the coordinator's job, performed once, centrally - not a per-agent
relevance gate that every agent re-litigates for itself.

When every resume scores below a confidence threshold, that is itself a signal: no
agent on the crew has the expertise this question needs. The coordinator treats this as
a capability gap rather than forcing an ill-suited agent to answer (see
[Onboarding Guide](../../operating/onboarding-guide/) for what happens next).

Channels remain useful as display and logging hints for the dashboard, but they are
metadata, not a routing gate - the broadcast reaches every channel regardless of which
one the coordinator classified as primary.

### Model tiering inside the coordinator

The coordinator's own classification work - generating the framing advisory and
selecting the subcommittee - runs on a fast, lightweight model rather than the larger
reasoning model used for synthesis. Only the final synthesis call uses the
reasoning-tier model. This keeps the cheap classification calls off the larger model's
prefill path, so a crew's most expensive inference is reserved for the answer, not for
deciding who should look at the question.

## Consensus signals, briefly

Every contribution to a discussion carries a signal from a small vocabulary - `agree`,
`concern`, `stand_aside`, `block`, `failure`, plus the facilitation signals `triaging`,
`evaluating`, `advisory`, and `proposal`. The full vocabulary and what each means to the
coordinator's synthesis is documented once, in
[Signals & Protocol](../../concepts/signals-and-protocol/); this page assumes it.

### Failure as a first-class signal

One design choice is worth calling out on its own: **agent failure is a first-class
signal**, not an edge case that gets hidden. An agent that fails a tool call, times out,
or hits an internal error publishes `failure` rather than going silent or reporting
`stand_aside`.

The distinction matters because the two mean different things:

| Signal | Means | Likely cause |
|---|---|---|
| `stand_aside` | "I looked, and this isn't my domain." | A participation decision. Working as designed. |
| `failure` | "I tried and could not complete." | An infrastructure problem: a tool timed out, the model errored, a dependency was unreachable. |

Collapsing both into `stand_aside` would corrupt every downstream reader of that
signal: gap detection would propose onboarding a new specialist when the real problem
is a broken MCP server, the dashboard would show "agent declined" when the truth is
"agent's tool broke," and quality metrics couldn't tell "no relevant expertise" from
"the expertise exists but its tooling failed." A `failure` signal carries a cause
(a tool timeout, a model error, a provider that couldn't be reached, and similar
categories) so downstream consumers can branch on root cause instead of parsing
free-form error text.

The coordinator classifies a discussion's outcome into one of three gap types when no
agent agreed:

| Gap type | Trigger | What it tells the reader |
|---|---|---|
| Tool gap | Zero agrees, one or more agents named a missing tool | The crew has the right domain expertise but lacks a specific capability. |
| Infrastructure gap | Zero agrees, one or more agents reported `failure` | An agent exists for this domain, but its tools or model are broken right now. |
| Specialist gap | Zero agrees, only `stand_aside` signals | No agent on the crew has relevant expertise at all. |

An infrastructure gap and a specialist gap call for opposite responses - fix a broken
MCP server versus onboard a new specialist - so keeping them distinct is the point.

## Not Robert's Rules: consent, not majority

A common misreading is to map this protocol onto *Robert's Rules of Order* - motions,
seconds, majority votes. It is closer to the opposite. Kubemoot's signal vocabulary
draws on **consent-based decision-making**: a proposal proceeds unless someone
`block`s it, and `stand_aside` is a first-class, recognized position rather than a
missing vote.

| Dimension | Robert's Rules | Kubemoot |
|---|---|---|
| Pass condition | Majority of "yes" votes | No `block` raised |
| Minority position | Loses the vote | A single `block` halts the action |
| Abstention | A missing data point | `stand_aside` - explicit and recognized |
| `agree` | "Aye" - counts toward a majority | A contribution, not a vote tally |

This matters in the coordinator's code, not just in spirit: it does not count `agree`
signals against a population to declare a winner. It synthesizes the contributions
once the discussion has settled, and it halts on `block` even when every other agent
agreed - a single specialist's objection can stop a destructive action regardless of how
many others endorsed it.

## Worked example

```
User: "Delete all pods in production"

The coordinator broadcasts thread_start to every channel and selects the
subcommittee whose resumes match "pods" and "production" - a Kubernetes workload
specialist among them.

The specialist calls its tools, confirms the blast radius, and publishes:

  BLOCK: "Deleting all pods in production will cause service downtime.
          42 pods across 8 deployments would be affected."

The coordinator halts rather than synthesizing an answer that proceeds:

  "This action was blocked: deleting all pods in production will cause
   service downtime, affecting 42 pods across 8 deployments.
   Would you like to proceed despite this objection?"
```

A single `block` outweighs any number of `agree`s. This is consent, not a vote count.

## How a discussion settles

The coordinator runs a per-thread state machine driven by signal state, not by a fixed
timer: `SUBMITTED` → `ADVISORY` → `EVALUATING` → `REVIEW` → `SYNTHESIZING` → `CLOSED`,
with a `PAUSED` state that can interrupt most of it. The full state table, every
transition, and the design principles behind signal-driven settling (per-agent
deadlines calibrated from historical latency, a sufficient-consensus fast path in
EVALUATING, no fast path in REVIEW) are documented once, in
[The Moot - Discussion phase lifecycle](../../concepts/consensus-model/#discussion-phase-lifecycle).
This page does not repeat it.

One behavior worth calling out here because it surprises people reading a discussion
timeline: a **dropped straggler's vote still counts**. When the coordinator settles
EVALUATING on sufficient consensus, it stops *waiting* on agents still evaluating - it
does not reject their eventual answer. If a straggler finishes during the REVIEW window
and publishes `agree`, that signal is folded into the synthesis. An `agree` can
legitimately land after the `REVIEW` phase's own marker and before `SYNTHESIZING`
without being an ordering bug; a late agree that arrives after synthesis has begun is
the one case that isn't incorporated, because the thread has moved on.

Model selection for each individual inference call, including which GPU or provider
serves it, is decided separately at the moment the call is made - the mechanics and
the reasoning live in [Scheduler](../scheduler/#jit-per-call-provider-selection).

## Roles inside a discussion

A crew has one coordinator, some Toolers, and optionally some Analysts; what each
role is and how they compose is defined in
[Crews & Agents](../../concepts/crews-and-agents/). Inside a discussion the phases
give that composition its shape: Toolers act in `EVALUATING` with tool-calling and
thinking off; Analysts act in `REVIEW` with thinking on and no live tools, reasoning
over what the Toolers gathered. The rationale for splitting the two roles, the
GPU cost profile of each, and how new Toolers and Analysts get created automatically
when a capability gap is filled are covered in
[Tooler-Analyst Architecture](../tooler-analyst-architecture/) - that page is the
canonical home for the split; this page only needs the phase mapping above.

The coordinator itself plays three parts in sequence rather than delegating them to
separate agents: it generates the framing advisory, selects the subcommittee, and
synthesizes the final answer. It doesn't impose an answer - it arbitrates. Once the
discussion settles, it applies the plain rule from the worked example above: a `block`
halts and gets reported; concerns are folded in as caveats on an otherwise-synthesized
answer; clean agrees get synthesized directly; and if nothing came back at all, it
answers from its own capabilities rather than leaving the user with nothing.

## Capability gaps and onboarding

When a gap is detected - a specialist gap most of all - Kubemoot doesn't just report
the miss. An onboarding agent watches every thread, and on a specialist or tool gap it
searches MCP registries for a server that could fill it, evaluates the candidate's
trustworthiness, and proposes it to the user. On consent, it deploys the server and the
operator automatically creates the new Tooler agent from it - tools, prompt, and
discussion channels included. A parallel documentation-discovery agent does the same
for knowledge gaps, turning newly onboarded servers into indexed RAG sources an Analyst
can reason over. The full pipeline, the trust-evaluation criteria, and the CRDs
involved are documented in [Onboarding Guide](../../operating/onboarding-guide/) and in
[Tooler-Analyst Architecture](../tooler-analyst-architecture/#self-discovered-agents);
this page only needs the shape: a gap is a signal, and the crew can grow in response to
one without a human writing a new Agent manifest by hand.

## Graceful degradation

The protocol is designed to keep answering, in a reduced form, when a piece is
missing:

| Scenario | Behavior |
|----------|----------|
| Resume query service is down | Coordinator falls back to an LLM triage call over the same resumes. |
| No agent's resume clears the confidence threshold | Treated as a capability gap; the onboarding agent can propose a fix. |
| An agent crashes mid-evaluation | No heartbeat means its deadline expires; the coordinator treats it as absent and moves on. |
| A block is raised | Coordinator halts, reports the block, and asks the user whether to proceed. |
| Synthesis itself fails | A fallback synthesis publishes the raw agrees collected so far; the thread still closes cleanly. |
| Every agent stands aside or nothing responds | The coordinator answers using its own capabilities rather than returning nothing. |
| NATS isn't configured | All discussion features are no-ops; agents answer direct chat requests without deliberating. |

## Watching a discussion

The Kubemoot dashboard renders the discussion table in real time - the thread list,
each agent's timeline with its signal and the GPU it ran on, and the final synthesis -
and the same stream powers Homelab Pilot's live "who's working on this" view during
chat. The dashboard's own design, its span graph, and the observability stack behind
it are documented separately in [Dashboard](../../operating/dashboard/) and
[Observability](../../operating/observability/); this page is the protocol those
surfaces render.

## Related

- [Crews & Agents](../../concepts/crews-and-agents/) - roles and how a crew composes.
- [The Moot](../../concepts/consensus-model/) - the phase state machine and consensus
  archetypes.
- [Signals & Protocol](../../concepts/signals-and-protocol/) - the full signal
  vocabulary.
- [Tooler-Analyst Architecture](../tooler-analyst-architecture/) - why the two roles
  are split and how the crew grows to fill a gap.
- [Scheduler](../scheduler/) - how an individual inference call picks its model and
  provider.
- [Onboarding Guide](../../operating/onboarding-guide/) - the operational detail behind
  capability-gap-driven onboarding.
