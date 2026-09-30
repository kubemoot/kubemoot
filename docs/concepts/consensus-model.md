---
title: "The Moot - Consensus Model"
weight: 10
description: "How a crew settles a question by deliberation, and how that deliberation can be organized."
---

Kubemoot's defining idea is that an answer is reached by **deliberation among
agents**, not asserted by a single model. This page explains the goal, the
signal vocabulary, the discussion phases, and, importantly, that *how* a crew
organizes its deliberation is configurable. For the units of interaction the
deliberation sits inside - conversations, turns, and the thread each discussion
runs on - see [Conversations, Turns & Threads](../conversations-turns-and-threads/).

## The goal

Settle a question through structured deliberation rather than by one authority. A
crew of agents each contributes what it knows; the result is composed from the
crew, so a single model being confidently wrong does not, by itself, become the
answer.

## Consensus signals

Agents communicate over the message bus with a vocabulary of consensus signals:

- `agree` - this contribution supports the emerging answer.
- `concern` - a reservation that should be weighed before settling.
- `stand_aside` - no relevant contribution; abstain without blocking.
- `block` - a strong objection.
- `failure` - the agent could not do its work (a tool call failed or a source was
  unreachable).

Additional protocol signals exist for facilitation (e.g. `triaging`, `evaluating`,
`advisory`, `proposal`). A declared **failure** is first-class: agents surface failure
as signal rather than going silent.

## Discussion phase lifecycle

Every discussion runs a per-thread state machine in the coordinator. The machine
advances by **signal and state**, not by elapsed time. Timeouts are safety nets for
stuck agents; the normal path never hits them.

### States

| State | What it means |
|-------|---------------|
| `SUBMITTED` | The user question has arrived; no deliberation has begun yet. |
| `ADVISORY` | The coordinator is generating the framing advisory and selecting the tooler subcommittee. |
| `EVALUATING` | Selected Toolers are calling their domain MCP tools and publishing findings. |
| `REVIEW` | Analysts are reasoning over the Toolers' gathered data and contributing interpretive findings before synthesis. |
| `SYNTHESIZING` | The coordinator is composing the final answer from all contributions. |
| `CLOSED` | The answer has been delivered and the thread is clean. |
| `PAUSED` | The discussion is suspended; settle timers are frozen until it resumes. |

### Transitions

Two kinds of transition move the machine forward.

**Automatic transitions** are checked periodically by the coordinator's signal
checker. They fire when the current signal state satisfies the advance condition:

| From | To | Advance condition |
|------|----|-------------------|
| `ADVISORY` | `EVALUATING` | The coordinator's inline advisory LLM call completes and the subcommittee is selected. |
| `EVALUATING` | `REVIEW` | Every convened Tooler has reached a terminal signal (`agree`, `concern`, `stand_aside`, `block`, `failure`) or its per-agent deadline has expired, **and** the bus has been quiet for the settle window, **and** the minimum evaluation time has elapsed. A sufficient-consensus fast path can also fire: once enough Toolers have agreed and the bus is quiet, the coordinator does not wait for stragglers. In either case, settle is state-driven, not clock-driven. |
| `REVIEW` | `SYNTHESIZING` | The review roster has settled (every selected Analyst has reported or timed out) and the minimum review time has elapsed. There is no fast path in REVIEW: a slow Analyst is not dropped. |

**Event transitions** fire immediately when a specific message arrives on the bus:

| Event | From | To |
|-------|------|----|
| `thread_start` | `SUBMITTED` | `ADVISORY` |
| synthesis complete | `SYNTHESIZING` | `CLOSED` |
| `thread_pause` | any state except `SYNTHESIZING` or `CLOSED` | `PAUSED` |
| `thread_resume` | `PAUSED` | the saved prior state (settle timers resume) |
| `stop_requested` | any state except `SYNTHESIZING` or `CLOSED` | `SYNTHESIZING` (the dashboard Stop button: forces synthesis on whatever signals exist) |
| human reply on a closed thread | `CLOSED` | `EVALUATING` (the thread is reopened for another round) |

### Key principles

**Signal-driven, not timeout-driven.** GPU inference and model loading have
unpredictable latency. The coordinator waits for agents to report, not for a clock to
expire. Timeouts fire only when an agent crashes or falls permanently silent; the
healthy path never reaches them.

**Per-agent deadline tracking, not a global wall clock.** When a Tooler publishes
`triaging`, the coordinator opens a generous deadline for that agent. When it
publishes `evaluating`, the deadline tightens to a P90-calibrated estimate. A
`heartbeat` signal refreshes the deadline. Only a missed deadline (no heartbeat, no
terminal signal) causes the coordinator to treat that agent as absent.

**REVIEW waits for its own roster.** The fast-path rule applies only in EVALUATING.
Once the coordinator enters REVIEW, it holds until every selected Analyst has
reported. A slow Analyst is not dropped in favour of a faster synthesis.

**PAUSED suspends settle timers.** While the machine is in PAUSED, the coordinator
does not advance settle windows or expire deadlines. When `thread_resume` arrives,
timers pick up from where they stopped.

**The machine lives in the coordinator, not in a CRD.** Discussion state is
high-churn, per-thread, and short-lived (seconds to a few minutes). Modelling it as
a reconciled Kubernetes custom resource would impose control-plane write amplification
at every signal. Instead, the machine runs in the agent-runtime coordinator process's
in-memory state over NATS, with NATS JetStream providing the durability (24-hour
retention) needed to survive coordinator restarts.

For a detailed walkthrough of how agents and the coordinator interact signal by
signal, see [Agentic Consensus](../../architecture/agentic-consensus/).

## Consensus archetypes

The metaphor fixes the **goal** (deliberate to a decision), not one rigid structure.
*How* a crew organizes that deliberation - who convenes whom, who may object, how
strongly an objection counts, and how the discussion concludes - is a **consensus
archetype**.

- **Today** Kubemoot ships a single archetype, installed as the `consent-3`
  `MootArchetype`: a facilitating **coordinator**
  convenes the Toolers whose expertise fits the question; Analysts reason over the
  Toolers' findings in the REVIEW phase; and the coordinator synthesizes once the
  discussion settles (a consent-style model). The coordinator is itself a light form
  of hierarchy.
- The `MootArchetype` resource names a discussion's phase vocabulary and signals, and
  the operator's scheduler reads it. The agent runtime does not: the orchestration of
  a discussion is fixed in code today, so the consensus flow is the only archetype
  that ships. Hierarchical organizations and stricter consent or voting rules are
  valid archetypes the design leaves room for; they are not implemented yet (see the
  [Roadmap](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)).
