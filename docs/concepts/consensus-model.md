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

Agents communicate over the message bus with a vocabulary of consensus signals. The
kinds an agent can record come from its crew's archetype: the `consent-3` archetype
declares these, each with a plain meaning the model reads:

- `agree` - this finding stands.
- `concern` - a finding on the table is wrong or unverified. It names the agent whose claim
  it doubts, and that agent is asked to recheck (see
  [Clarifying questions](#clarifying-questions)); with no agent named it is a note on the table.
- `question` - ask the agent that made a finding to recheck it (see
  [Clarifying questions](#clarifying-questions)).
- `stand_aside` - no relevant contribution; abstain without blocking.
- `tool_gap` - answering needs a tool the agent does not have.
- `no_data` - the agent's tools ran but returned nothing usable.
- `block` - a strong objection.

An agent records its verdict by calling a built-in `signal` tool whose kinds are the
archetype's names, not by writing a special word; its reply stays its finding (see
[How an agent records its verdict](../signals-and-protocol/#how-an-agent-records-its-verdict)).
Because the kinds are declared, an archetype can define its own consensus states without a
change to the runtime.

The runtime adds `failure`: the agent could not do its work (a tool call failed or a source
was unreachable). Additional protocol signals exist for facilitation (e.g. `triaging`,
`evaluating`, `advisory`, `proposal`). A declared **failure** is first-class: agents surface
failure as signal rather than going silent.

## Discussion phase lifecycle

Every discussion runs a per-thread state machine in the coordinator. The machine
advances by **signal and state**, not by elapsed time. Timeouts are safety nets for
stuck agents; the normal path never hits them.

### States

| State | What it means |
|-------|---------------|
| `SUBMITTED` | The user question has arrived; no deliberation has begun yet. |
| `ADVISORY` | The coordinator is generating the framing advisory and selecting the tooler subcommittee. |
| `EVALUATING` | Selected Toolers are calling their domain MCP tools and publishing findings. An agent may ask another to recheck a finding. |
| `REVIEW` | Analysts are reasoning over the Toolers' gathered data and contributing interpretive findings before synthesis. An Analyst that doubts a finding asks the agent that made it to recheck. |
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
| `REVIEW` | `SYNTHESIZING` | Every Analyst that `review_ready` woke (by the archetype's [`review`](../../reference/mootarchetype/#review) setting: the Analysts whose declared domains touch the question or the gathered data, or every Analyst) has published a terminal signal in this phase, or its deadline has expired and counts as `failure`. With the archetype's `review.order: generalistsFirst` (the default archetype's setting), REVIEW has two passes: the Analysts that declare no domain are woken first, and the Analysts that declare a domain are woken, with the generalists' findings in front of them, once every generalist has signalled or expired; the phase advances after the second pass. Each woken Analyst is pending from the moment REVIEW begins, so the phase cannot settle before its reviewers have started. When the crew has no Analysts, the minimum review time and the quiet period are the safety net. There is no fast path in REVIEW: a slow Analyst is not dropped before its deadline. |
| `RATIFY` | `CLOSED` | Every agent on the ratification roster has published a terminal signal (`agree`, `concern`, `block`, `stand_aside`, `failure`) for the current round, or its per-agent deadline has expired and counts as `failure`; and either nobody objected, or the archetype's maximum rounds are used up. When somebody objected and rounds remain, the coordinator revises and the phase starts its next round instead of closing. |

**Event transitions** fire immediately when a specific message arrives on the bus:

| Event | From | To |
|-------|------|----|
| `thread_start` | `SUBMITTED` | `ADVISORY` |
| draft complete | `SYNTHESIZING` | `RATIFY` (the draft is published as a `draft` message and its roster is woken); `CLOSED` when the archetype turns ratification off or no agent contributed, so there is nobody to ratify |
| `thread_pause` | any state except `SYNTHESIZING`, `RATIFY` or `CLOSED` | `PAUSED` |
| `thread_resume` | `PAUSED` | the saved prior state (settle timers resume) |
| `stop_requested` | any state except `CLOSED` | `CLOSED` (a person's stop abandons the turn: no synthesis, no draft; the thread closes with a text that says the person stopped it, and a synthesis still running is dropped) |
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
REVIEW waits for the Analysts `review_ready` woke (with `generalistsFirst`, the generalists first, then the domain Analysts, each on its own roster); RATIFY waits for the contributors the `draft` woke. A phase advances as soon as every
member of its roster has published `agree`, `concern`, `stand_aside`, `block` or
`failure` in that phase, or has missed its deadline (which counts as `failure`).
A member that has only published `triaging`, `evaluating`, `heartbeat` or `waiting`
holds the phase, and a signal from an earlier phase does not count for the next one.
The minimum evaluation and review times and the quiet period are safety nets for a
phase whose roster is unknown or still pending, not the normal path.

**A question holds the phase.** While a clarifying question is open, the agent asked is
pending and the asker has not finished its turn, so the phase cannot settle, and the
EVALUATING fast path does not fire. See [Clarifying questions](#clarifying-questions).

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

## Clarifying questions

The strength of a crew comes from the discussion, not from one large model. When an agent
doubts another's finding, it does not overrule it or replace it with its own guess: it asks
the agent that made it, the way experts check each other's work.

> k8s-advisor: "@k8s-nodes, can you recheck the node count? 100 seems high for this homelab."

**The round.**

1. The asker records a `question` with the signal tool, naming the agent in `to`. A `concern`
   that names the agent it doubts starts the same round (the archetype declares this with
   `startsRecheck`); a concern that names no agent, or one the crew does not have, is a note
   on the table. Its turn stays open: a question is not a terminal signal.
2. The coordinator relays it as `question_asked` to that agent alone. The agent asked is now
   pending, with the same kind of deadline a triaging agent gets.
3. The agent asked runs its assessment again with its tools (no triage: it was named) and
   publishes its verdict and its new finding. The finding **replaces** its earlier one in what
   the coordinator weighs; the bus keeps both, so the transcript shows the exchange.
4. The coordinator wakes the asker with `question_answered`, which carries the revised
   finding. The asker is pending again.
5. The asker checks again and signals. That ends its turn, and the phase settles as it does
   without a question.

The archetype's [`recheck.maxRounds`](../../reference/mootarchetype/#recheck) (default 1) is
the most questions relayed per thread; `0` turns the round off.

**When a question is not relayed.** A question past the round limit, with the round off, to an
agent not in the crew, to the asker itself, to an agent already in a question, or asked after
the crew has stopped gathering and reviewing (during synthesis, ratification, or on a paused or
closed thread) is recorded as the asker's `concern`, with the question as its text, and the
asker's turn ends. So is a question whose agent fails or misses its deadline. An asker that fails
drops its own question. The doubt stays on the table for the
synthesis; it is never dropped.

**State-driven.** Every step waits on a message, never on a clock. The deadlines are safety nets
for an agent that crashed: the agent asked refreshes its deadline with heartbeats while it
works, as in any phase.

**What the synthesis sees.** The question, the revised finding, and a line in the signal summary
for each question (who asked whom, and whether it was answered). A replaced finding is shown
only as replaced, without its text, so a wrong value does not reach the answer.

**Recorded.** The coordinator log records `question_asked` and `question_answered`
([the coordinator log](../signals-and-protocol/#the-coordinator-log)), and the dashboard and
transcripts show the `question`, `question_asked` and `question_answered` messages.

## Ratification

> **Opt-in and experimental.** Ratification is off by default. A crew turns it on with
> `ratify.enabled: true` on its archetype. Three problems found in measurement are open (see
> [Known problems](#known-problems)); until they are fixed, leave it off unless you are testing it.

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
   revision as `draft` (round 2) to the **whole roster again**, including the agents that agreed:
   a revision rewrites statements they had checked, so no agent's earlier agreement carries over.
4. The phase closes when the roster has ratified, or after the archetype's maximum rounds (default
   2: the draft and one revision). If the table still objects, the answer is **released with the
   objections attached and visible**: the answer text ends with the objections that stand, and the
   `synthesis` message carries them in `metadata.ratification`. An answer is never released with
   an objection hidden.

**What a ratifier is asked.** The instructions are a `PromptModule` named `ratify-prompt`, written in
ADL, that a crew's chart ships and lists in the `promptRefs` of the agents that can be on the
roster, like its other shared modules (`discussion-protocol`, `analyst-review-protocol`). The runtime
ends the message a ratifier answers with the marker `[RATIFY]`, and the module speaks to that
marker. A crew overrides the wording by editing its module. An agent whose prompt does not carry a
`ratify-prompt` module (one that opens with `DEFINE COMPONENT ratify-prompt`) is given a two-line
default by the runtime (check against your own results; open with the verdict word), so a crew that
has not adopted the module still ratifies. The reply must open with `AGREE`, `CONCERN` or `BLOCK`; one that
does not is published as a `failure`, never counted as agreement. The instructions name no tool: an
agent chooses its tools by their own descriptions.

**Failure is a signal here too.** A ratifier whose call fails publishes `failure`; one that misses
its deadline gets a `failure` the coordinator publishes for it. A failed ratifier is not an
objection and does not call for another round, but the released answer names every agent that did
not ratify (failed, missed its deadline, or had not answered when a stop or the hard ceiling
released the draft), so the person can see that the answer went out without that check. Silence is
never consent: an answer is `ratified` only when every agent on the roster agreed.

**State-driven.** Each ratifier has the same kind of deadline as in REVIEW (opened when the draft
is published, refreshed by its `heartbeat`). The round advances the moment the roster has
answered, never on a clock; the deadline is the safety net for an agent that crashed.

**Off by default.** An archetype that declares no `ratify` block, or a block without `enabled`,
does not ratify: `SYNTHESIZING` goes straight to `CLOSED` and the draft is the answer. A crew opts
in with `ratify.enabled: true`.

### Known problems

The first measurement ran one question about node health 10 times without ratification and 10
times with it, on the same crew. Ratification invented no nodes in either run, but it did not
yet earn its cost:

- **Slower, and not more accurate.** The mean time per answer rose from 73 s to 125 s. Answers
  with a wrong count went from 1 to 2 of 10.
- **Missed deadlines.** One ratifier missed its deadline in 6 of the 10 runs, so those answers
  went out with that agent named as not having checked them.
- **Agreement without checking.** Ratifiers agreed with wrong counts without comparing the draft
  against their stored results, which is the check the phase exists for.
- **Internal text in answers.** Released answers carried the phase's own bookkeeping, such as a
  list of agents that did not answer and the coordinator's account of how an objection was
  resolved, instead of only the answer.

Ratification stays opt-in until these are fixed.

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
  phase. The agent runtime reads four blocks of it: `review` (who reviews), `ratify`
  (whether and how the draft is ratified), `signals` (the kinds of verdict an agent can
  record) and `recheck` (how many clarifying questions a thread may have). The phases and
  transitions of a discussion (advisory, evaluating, review, synthesis, ratify) are fixed in
  code, so the consensus flow is the only archetype that ships. Hierarchical organizations and stricter consent or voting rules are
  valid archetypes the design leaves room for; they are not implemented yet (see the
  [Roadmap](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)).
