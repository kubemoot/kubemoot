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
| `SYNTHESIZING` | The coordinator is composing a draft answer from all contributions. |
| `RATIFY` | The draft is on the table. The agents that contributed check it against what they found and publish `agree`, `concern` or `block`; the coordinator revises once on their objections and puts the revision to the table again. See [Ratification](#ratification). |
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
| `REVIEW` | `SYNTHESIZING` | Every Analyst that `review_ready` woke (by the archetype's [`review`](../../reference/mootarchetype/#review) setting: the Analysts whose declared domains touch the question or the gathered data, or every Analyst) has published a terminal signal in this phase, or its deadline has expired and counts as `failure`. Each woken Analyst is pending from the moment REVIEW begins, so the phase cannot settle before its reviewers have started. When the crew has no Analysts, the minimum review time and the quiet period are the safety net. There is no fast path in REVIEW: a slow Analyst is not dropped before its deadline. |
| `RATIFY` | `CLOSED` | Every agent on the ratification roster has published a terminal signal (`agree`, `concern`, `block`, `stand_aside`, `failure`) for the current round, or its per-agent deadline has expired and counts as `failure`; and either nobody objected, or the archetype's maximum rounds are used up. When somebody objected and rounds remain, the coordinator revises and the phase starts its next round instead of closing. |

**Event transitions** fire immediately when a specific message arrives on the bus:

| Event | From | To |
|-------|------|----|
| `thread_start` | `SUBMITTED` | `ADVISORY` |
| draft complete | `SYNTHESIZING` | `RATIFY` (the draft is published as a `draft` message and its roster is woken); `CLOSED` when the archetype turns ratification off or no agent contributed, so there is nobody to ratify |
| `stop_requested` | `RATIFY` | `CLOSED` (the current draft is released at once; the objections so far are attached and every agent that has not answered is named as not having checked it) |
| `thread_pause` | any state except `SYNTHESIZING`, `RATIFY` or `CLOSED` | `PAUSED` |
| `thread_resume` | `PAUSED` | the saved prior state (settle timers resume) |
| `stop_requested` | any state except `SYNTHESIZING`, `RATIFY` or `CLOSED` | `SYNTHESIZING` (a person's stop request: forces synthesis on whatever signals exist) |
| human reply on a closed thread | `CLOSED` | `EVALUATING` (the thread is reopened for another round) |

### Key principles

**Signal-driven, not timeout-driven.** Accelerator inference and model loading have
unpredictable latency. The coordinator waits for agents to report, not for a clock to
expire. Timeouts fire only when an agent crashes or falls permanently silent; the
healthy path never reaches them.

**Per-agent deadline tracking, not a global wall clock.** When a Tooler publishes
`triaging`, the coordinator opens a generous deadline for that agent. When it
publishes `evaluating`, the deadline tightens to a P90-calibrated estimate. A
`heartbeat` signal refreshes the deadline. Only a missed deadline (no heartbeat, no
terminal signal) causes the coordinator to treat that agent as absent.

**Each phase settles on its own roster.** EVALUATING waits for the selected Toolers;
REVIEW waits for the Analysts `review_ready` woke; RATIFY waits for the contributors the `draft` woke. A phase advances as soon as every
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

## Ratification

Nobody at the table should see an answer for the first time when the person does. After
`SYNTHESIZING` the coordinator's synthesis is a **draft**: it is published to the table as a
`draft` message and the agents that produced the evidence check it against that evidence before
it is released. A draft that states a node, a number or a status no agent found is the failure
this phase exists to catch.

**The roster.** Every agent that published `agree`, `concern` or `block` in this turn is asked
to ratify: the Toolers that gathered evidence and the Analysts that reviewed it. An agent that
only stood aside or failed gathered nothing the draft could contradict and is not asked. The archetype may widen the roster ([`ratify.roster`](../../reference/mootarchetype/#ratify)).
A turn the coordinator answered directly, with no agent contributing, has no roster and skips the
phase.

**A round.**

1. The coordinator publishes `draft` (round 1) with the draft text and the roster as its inner
   circle, so only the roster wakes.
2. Each ratifier checks the draft against its own stored results with its tools: it compares the
   draft's names, counts and statuses with what its results hold, and does not rely on reading the
   draft and agreeing. It replies `agree`, `concern` (the draft states something its results do
   not support) or `block` (the draft contradicts its results). A `concern` or `block` carries the
   objection: which statement, and what the results show instead. Every reply carries
   `metadata.ratifies`, the round number, so a late signal from an earlier phase or round never
   counts.
3. When the roster has answered (or missed its deadline), the coordinator looks at the replies. No
   `concern` or `block`: the phase closes and the draft is the answer. Some objection and a round
   left: the coordinator revises **once**, with the objections in front of it, and publishes the
   revision as `draft` (round 2) to the same roster.
4. The phase closes when the roster has ratified, or after the archetype's maximum rounds (default
   2: the draft and one revision). If the table still objects, the answer is **released with the
   objections attached and visible**: the answer text ends with the objections that stand, and the
   `synthesis` message carries them in `metadata.ratification`. An answer is never released with
   an objection hidden.

**Failure is a signal here too.** A ratifier whose call fails publishes `failure`; one that misses
its deadline gets a `failure` the coordinator publishes for it. A failed ratifier is not an
objection and does not call for another round, but the released answer names every agent that did
not ratify (failed, missed its deadline, or had not answered when a stop or the hard ceiling
released the draft), so the person can see that the answer went out without that check. Silence is
never consent: an answer is `ratified` only when every agent on the roster agreed.

**State-driven.** Each ratifier has the same kind of deadline as in REVIEW (opened when the draft
is published, refreshed by its `heartbeat`). The round advances the moment the roster has
answered, never on a clock; the deadline is the safety net for an agent that crashed.

**On by default.** Ratification is the premise of the moot, so an archetype that declares no
`ratify` block ratifies. A crew turns it off with `ratify.enabled: false` (a crew whose answers are
checked elsewhere, or a fitness judge whose verdict must not be revised). With it off,
`SYNTHESIZING` goes straight to `CLOSED`.

**What is not ratified.** Answers written in code, not by the synthesis call, go out as they are:
the capacity message, the no-data answer, a fallback after a failed synthesis, a direct answer, and
the hard-ceiling close. They carry no claim beyond what the agents posted.

**A restart during `RATIFY`** runs the synthesis again: the draft, the replies and the round live
in the coordinator's memory, the log records the phase, and the answer is logged only when the
phase closes (see [Resume](../signals-and-protocol/#resume)).

## Consensus archetypes

The metaphor fixes the **goal** (deliberate to a decision), not one rigid structure.
*How* a crew organizes that deliberation - who convenes whom, who may object, how
strongly an objection counts, and how the discussion concludes - is a **consensus
archetype**.

- **Today** Kubemoot ships a single archetype, installed as the `consent-3`
  `MootArchetype`: a facilitating **coordinator**
  convenes the Toolers whose expertise fits the question; Analysts reason over the
  Toolers' findings in the REVIEW phase; the coordinator drafts an answer once the
  discussion settles; and the contributors ratify the draft before it is released (a
  consent-style model). The coordinator is itself a light form
  of hierarchy.
- The `MootArchetype` resource ([reference](../../reference/mootarchetype/)) declares
  the scheduling phases and signals of an archetype, and the operator validates
  `CrewSchedulingPolicy` phase names against it so the scheduler can choose a model per
  phase. The agent runtime reads two blocks of it, `review` (who reviews) and
  `ratify` (whether and how the draft is ratified); the phases and transitions of a
  discussion (advisory, evaluating, review, synthesis, ratify) are fixed in code, so the consensus flow is the only archetype
  that ships. Hierarchical organizations and stricter consent or voting rules are
  valid archetypes the design leaves room for; they are not implemented yet (see the
  [Roadmap](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)).
