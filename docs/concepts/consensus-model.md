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
| `DECIDING` | The coordinator shapes the review: a full review, a one-analyst concurrence check, or none. |
| `CONCURRING` | One Analyst checks the gathered results and answers whether it concurs, in one model turn with no tools. |
| `REVIEW` | Analysts are reasoning over the Toolers' gathered data and contributing interpretive findings before synthesis. |
| `SYNTHESIZING` | The coordinator is composing the final answer from all contributions. |
| `CLOSED` | The answer has been delivered and the thread is clean. |
| `PAUSED` | The discussion is suspended; settle timers are frozen until it resumes. `DECIDING` cannot be paused. |

### Transitions

Two kinds of transition move the machine forward.

**Automatic transitions** are checked periodically by the coordinator's signal
checker. They fire when the current signal state satisfies the advance condition:

| From | To | Advance condition |
|------|----|-------------------|
| `ADVISORY` | `EVALUATING` | The coordinator's inline advisory LLM call completes and the subcommittee is selected. |
| `EVALUATING` | `SYNTHESIZING` | Exactly one Tooler agreed with no concern or block, and the crew has no Analyst agents. |
| `EVALUATING` | `DECIDING` | Every other settled evaluation (see the settle rule below). |
| `DECIDING` | `CONCURRING` | The review decision is `concur`. |
| `DECIDING` | `REVIEW` | The review decision is `full`, or a runtime guard forced it, or the crew does not declare the decision. |
| `DECIDING` | `SYNTHESIZING` | The review decision is `none`, where the crew's policy allows it. |
| `CONCURRING` | `SYNTHESIZING` | The Analyst agreed or stood aside. |
| `CONCURRING` | `REVIEW` | The Analyst raised a concern, blocked, or failed (an empty reply is a failure). The review runs with the selected Analysts other than that one (or, if it was the only one, the next best resume match), with its view on the board. |
| `REVIEW` | `SYNTHESIZING` | The woken Analysts have all reported or expired. There is no fast path in REVIEW: a slow Analyst is not dropped. |

**How a phase settles.** Each phase has a roster: in `EVALUATING` the selected agents
other than Analysts; in `CONCURRING` and `REVIEW` the woken Analysts, who count as
pending from the moment the phase starts. When every roster member has reached a
terminal signal (`agree`, `concern`, `block`, `stand_aside`, `failure`) and none is
pending, the phase advances immediately. A member that never answers expires to a
failure at its deadline. The minimum evaluation time (`minEvalSeconds`, default 20 s),
the minimum review time (`minReviewSeconds`, default 10 s), and the quiet window apply
only when the roster is unknown (no agents were selected) or still has signals pending.
In EVALUATING, a sufficient-consensus fast path can also fire: once enough Toolers have
agreed and the bus is quiet, the coordinator does not wait for stragglers.

**Event transitions** fire immediately when a specific message arrives on the bus:

| Event | From | To |
|-------|------|----|
| `thread_start` | `SUBMITTED` | `ADVISORY` |
| synthesis complete | `SYNTHESIZING` | `CLOSED` |
| `thread_pause` | any state except `DECIDING`, `SYNTHESIZING`, or `CLOSED` | `PAUSED` |
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
Once the coordinator enters REVIEW, it holds until every woken Analyst has reported
or expired. A slow Analyst is not dropped in favour of a faster synthesis, and the
phase cannot settle before a woken reviewer has published its first signal.

**The coordinator shapes the review.** After EVALUATING the coordinator can decide how
much review the results need. A crew that declares the
[review decision](../../user-guides/compose-a-crew/#declare-the-review-decision)
gets one coordinator model call that answers `concur` (one Analyst checks the
results), `full` (the selected Analysts review), or `none` (straight to synthesis).
The policy lives in the crew's coordinator PromptModule. The runtime enforces guards
the policy cannot override: a Tooler failure, any concern or block, or no Tooler
agreement forces `full` without a model call, and an unreadable answer or failed call
is `full`. The decision is published on the thread as a `review_decision` message.
Crews without the decision keep the full review.

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
  Toolers' findings in the REVIEW phase (or check them in a one-analyst concurrence
  when the crew declares the review decision); and the coordinator synthesizes once the
  discussion settles (a consent-style model). The coordinator is itself a light form
  of hierarchy.
- The `MootArchetype` resource ([reference](../../reference/mootarchetype/)) declares
  the scheduling phases and signals of an archetype, and the operator validates
  `CrewSchedulingPolicy` phase names against it so the scheduler can choose a model per
  phase. The agent runtime does not read it: the phases and transitions of a
  discussion (advisory, evaluating, deciding, concurring, review, synthesis) are fixed in
  code, and the review decision is declared by the crew (coordinator environment and
  PromptModule), not by the archetype. The consensus flow is the only archetype
  that ships. Hierarchical organizations and stricter consent or voting rules are
  valid archetypes the design leaves room for; they are not implemented yet (see the
  [Roadmap](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)).
