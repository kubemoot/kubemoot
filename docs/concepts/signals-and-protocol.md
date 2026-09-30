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

## The CONCERN: reply sentinel

An agent reply that starts with `CONCERN:` is published as a `concern` signal whose
content is the text after the sentinel, the same way `TOOL_GAP:` marks a tool gap. It
applies to any agent reply. The concurrence check uses it: the Analyst is asked to
start its reply with `CONCERN:` when something is missing or wrong.

## Review messages

| Message | Meaning |
|---------|---------|
| `review_decision` | The coordinator's review decision for the thread. Metadata: `decision` (`concur`, `full`, or `none`), `reason`, `forced` (a runtime guard set it, not the crew's policy), and `tier` (`fast` or `reasoning`). The dashboard timeline shows it. |
| `review_ready` | Wakes the analysts that review. It always names them in `innerCircle`, and `metadata.reviewMode` is `concur` (one analyst, with a concurrence request) or `full`. It never wakes every analyst: when none was selected, the single best resume match reviews, and with no ranking available the thread goes straight to synthesis. |

## Stand-asides for GPU capacity

A stand-aside usually means the agent had nothing to add. Three reasons mark a different
case, where the agent was selected and willing but the cluster could not run it:

| `metadata.reason` | Meaning |
|---|---|
| `gpu-busy` | Every GPU that can hold the model stayed busy until the discussion ended or the agent's capacity wait reached its safety limit. |
| `model-too-large` | No GPU in the cluster can ever hold the model (`metadata.model`). |
| `prompt-too-large` | No provider gives an acceptable model a context window that holds the prompt. The engine would drop the oldest messages, so the agent stands aside instead. |

When no agent contributed and at least one stood aside for one of these reasons (or was
still waiting), the crew's answer says the GPUs were the limit, not the crew's design.
See [Models & Scheduling](../models-and-scheduling/#when-every-gpu-is-busy).

## Discussion phases

A discussion advances by **state**, not by a fixed timer. The coordinator runs a
per-thread state machine with the following phases:

`SUBMITTED` → `ADVISORY` → `EVALUATING` → `DECIDING` → (`CONCURRING` →) `REVIEW` → `SYNTHESIZING` → `CLOSED`

When exactly one Tooler agreed with no concern or block and the crew has no Analysts,
the thread goes from `EVALUATING` straight to `SYNTHESIZING`. The review decision can
also skip `REVIEW` (after a concurrence, or with `none`). A `PAUSED` state can
interrupt any phase except `DECIDING`, `SYNTHESIZING`, and `CLOSED`; the
machine resumes to the same phase when unpaused. A dashboard Stop forces an
immediate transition to `SYNTHESIZING` on whatever signals exist. A human reply on a
closed thread reopens it to `EVALUATING`.

In plain terms, the flow is:

1. **ADVISORY** - the coordinator generates the framing advisory and selects the
   Tooler subcommittee; Toolers acknowledge with `triaging`.
2. **EVALUATING** - selected Toolers call their domain MCP tools and publish findings,
   each carrying a signal.
3. **DECIDING** - the coordinator shapes the review: `concur`, `full`, or `none`.
4. **CONCURRING** (only after `concur`) - one Analyst is asked whether it concurs.
5. **REVIEW** - Analysts (if the crew has them) reason over the Toolers' gathered data
   and contribute interpretive findings before synthesis. The coordinator waits for
   every woken Analyst to report; there is no fast path in this phase.
6. **SYNTHESIZING** - the coordinator composes the answer from all contributions.
7. **CLOSED** - the answer has been delivered and the thread is clean.

Timeouts exist only as safety nets. The normal path is driven by signal state: each
phase has a roster, and when every roster member has a terminal signal the phase
advances at once. The minimum evaluation and review times and the quiet window apply
only when the roster is unknown or still has signals pending. GPU
inference and model loading have unpredictable latency, so the protocol waits on
state transitions, not wall-clock deadlines.

The full state machine (all states, both automatic and event-driven transitions, and
the key design principles) is documented in
[The Moot - Discussion phase lifecycle](../consensus-model/#discussion-phase-lifecycle).

## Why a protocol instead of direct calls

Routing every contribution through the bus with an explicit signal makes disagreement
and abstention **observable**. The coordinator settles on what the crew actually
reported - agreements net of concerns and blocks - instead of taking the first or
loudest answer. It also means an agent crashing or timing out degrades gracefully:
its absence (or its `failure` signal) is visible to the coordinator rather than
silently corrupting the result.
