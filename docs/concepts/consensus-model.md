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
| `EVALUATING` | `REVIEW` | Every selected Tooler has published a terminal signal (`agree`, `concern`, `stand_aside`, `block`, `failure`) in this phase, or its per-agent deadline has expired and counts as `failure`. The phase then advances at once, without waiting out the minimum evaluation time or a quiet period. When no subcommittee was selected (the roster is unknown), or a Tooler is still pending, the quiet-window rule applies as the safety net: every agent that started has reached a terminal signal, the bus has been quiet for the settle window, and the minimum evaluation time has elapsed. A sufficient-consensus fast path can also fire: once enough Toolers have agreed and the bus is quiet, the coordinator does not wait for stragglers. |
| `REVIEW` | `SYNTHESIZING` | Every Analyst that `review_ready` woke (the selected Analysts, or every Analyst when none was selected) has published a terminal signal in this phase, or its deadline has expired and counts as `failure`. Each woken Analyst is pending from the moment REVIEW begins, so the phase cannot settle before its reviewers have started. When the crew has no Analysts, the minimum review time and the quiet period are the safety net. There is no fast path in REVIEW: a slow Analyst is not dropped before its deadline. |

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

**Each phase settles on its own roster.** EVALUATING waits for the selected Toolers;
REVIEW waits for the Analysts `review_ready` woke. A phase advances as soon as every
member of its roster has published `agree`, `concern`, `stand_aside`, `block` or
`failure` in that phase, or has missed its deadline (which counts as `failure`).
A member that has only published `triaging`, `evaluating`, `heartbeat` or `waiting`
holds the phase, and a signal from an earlier phase does not count for the next one.
The minimum evaluation and review times and the quiet period are safety nets for a
phase whose roster is unknown or still pending, not the normal path.

**REVIEW waits for its own roster.** The fast-path rule applies only in EVALUATING.
Once the coordinator enters REVIEW, it holds until every woken Analyst has
reported or missed its deadline. A slow Analyst is not dropped in favour of a faster
synthesis.

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
- The `MootArchetype` resource ([reference](../../reference/mootarchetype/)) declares
  the scheduling phases and signals of an archetype, and the operator validates
  `CrewSchedulingPolicy` phase names against it so the scheduler can choose a model per
  phase. The agent runtime does not read it: the phases and transitions of a
  discussion (advisory, evaluating, review, synthesis) are fixed in code, so the orchestration of
  a discussion is fixed today, so the consensus flow is the only archetype
  that ships. Hierarchical organizations and stricter consent or voting rules are
  valid archetypes the design leaves room for; they are not implemented yet (see the
  [Roadmap](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)).
