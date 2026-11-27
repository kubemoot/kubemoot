---
title: "MootArchetype CRD"
weight: 9
description: "The MootArchetype resource: the declared phases, signal kinds, and state machine of a consensus archetype, and the review, ratify, signal and recheck settings the agent runtime reads. A v1alpha1 API."
---

## Overview

A `MootArchetype` declares the shape of a consensus archetype: the phases it passes
through, the kinds of signal agents may record, and the legal transitions between phases. The
operator installs one, `consent-3`, and validates the resource and the scheduling
policies that reference it.

> **Maturity.** `MootArchetype` is a newer feature and is not fully vetted. The API is
> `kubemoot.ai/v1alpha1`: expect the fields to change. Read
> [What it does today](#what-it-does-today) and
> [What it does not do yet](#what-it-does-not-do-yet) before building on it.

A `MootArchetype` is cluster-scoped (short name `moot`), so one archetype can be
referenced by `CrewSchedulingPolicy` resources in any namespace. The concept behind it
is described in [Consensus Model](../../concepts/consensus-model/#consensus-archetypes).

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MootArchetype
metadata:
  name: consent-3
spec:
  description: "Sociocracy 3.0 consent decision-making."
  phases:
    - name: triaging
      role: gatekeeping
      description: "Coordinator probes which agents should join the thread."
    - name: evaluating
      role: deliberation
      description: "Agents emit agree, concern, stand_aside, or block signals."
    - name: synthesis
      role: closure
      description: "Coordinator synthesizes the deliberation into a response."
  signals:
    - name: agree
      meaning: "Your finding stands."
    - name: concern
      meaning: "A finding on the table is wrong or unverified."
    - name: question
      meaning: "Ask the agent that made a finding to recheck it."
      terminal: false
      addressed: true
  recheck:
    maxRounds: 1
  stateMachine:
    initial: triaging
    transitions:
      - from: triaging
        to: evaluating
        "on": all-triaged
      - from: evaluating
        to: synthesis
        "on": settled
```

## Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `description` | string | no | Human-readable explanation of the archetype. |
| `phases` | list of phase | yes (at least one) | The phase definitions. See [Phases](#phases). |
| `signals` | list of signal | no | The kinds of verdict an agent records with the signal tool. See [Signals](#signals). When absent, agents record their verdict the way they did before the tool existed. |
| `recheck` | object | no | The clarifying-question round. See [Recheck](#recheck). When absent, a thread may have one question. |
| `stateMachine` | object | no | The legal phase transitions. See [State machine](#state-machine). When absent, progression is sequential in the order the phases are declared. |
| `review` | object | no | Which analysts the coordinator convenes for review. See [Review](#review). When absent, every analyst reviews. |
| `ratify` | object | no | Whether the contributors ratify the coordinator's draft before it is released, and how. See [Ratify](#ratify). Ratification is opt-in and experimental: when absent, they do not. |

### Phases

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | The phase name. `CrewSchedulingPolicy` rules reference it. |
| `role` | string | no | What the phase accomplishes, such as `gatekeeping`, `deliberation`, or `closure`. Informational. |
| `description` | string | no | A one-line explanation shown in dashboards. |

### Review

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `convene` | `all` or `domain` | `all` | `all`: the analysts the coordinator itself selected, or every analyst when it selected none (the behavior before review was declared). `domain`: only the analysts whose declared [`domains`](../agent/#specdomains) touch the question or the data the toolers gathered. |
| `fallback` | `primary` or `all` | `primary` | Applies when `convene` is `domain` and no analyst matches. `primary`: the one analyst that best matches the question's words and the gathering toolers' domains. `all`: every analyst. |
| `generalists` | `always` or `selected` | `always` | Applies when `convene` is `domain`. `always`: an analyst that declares no `domains` (a generalist, such as the arithmetic analyst) reviews every discussion. `selected`: a generalist reviews only the discussions where the coordinator selected it for the question, so a question that needs no computation does not wait on it. |

A crew that has analysts never reviews with none. With `generalists: always` (the default), an analyst that declares no `domains` is a generalist and reviews every discussion in `domain` mode. When `domain` matches nothing and nothing
ranks the analysts either (no analyst declares a domain the question or the data touches), every
analyst reviews, because choosing one would be arbitrary. See
[Who reviews](../../architecture/tooler-analyst-architecture/#who-reviews) for how the match works.

```yaml
spec:
  review:
    convene: domain
    fallback: primary
    generalists: always
```

The operator reads the block of the crew's archetype (the `archetypeRef` of the
`CrewSchedulingPolicy` naming the crew, `consent-3` without one) and gives it to the crew's
coordinator as `KUBEMOOT_DISCUSS_REVIEW_CONVENE`, `KUBEMOOT_DISCUSS_REVIEW_FALLBACK` and `KUBEMOOT_DISCUSS_REVIEW_GENERALISTS`. The
`consent-3` archetype the operator installs sets `convene: domain`. An edit to the block
re-reconciles the coordinators. An Agent's own `spec.deployment.env` wins over both.

The CRD gains the `review` field, so apply the new CRDs before the operator chart that sets it. Flux installs use `crds: CreateReplace` on the HelmRelease; plain `helm` installs apply the CRDs first (`kubectl apply -f` the chart's `crds/` directory), because `helm upgrade` does not update CRDs.

### Ratify

After the coordinator drafts the answer, the agents that contributed check it against what they found
(see [Ratification](../../concepts/consensus-model/#ratification)). The block declares how.

Ratification is **off by default** and experimental. Three problems found in measurement (a mean
answer time of 125 s against 73 s without it, one ratifier missing its deadline in 6 of 10 runs, and
reviewers agreeing without checking their stored results) are described under
[Known problems](../../concepts/consensus-model/#known-problems). A crew opts in with
`enabled: true`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | `true` turns ratification on. `false`, or omitting it, releases the draft as the answer, with no ratification (`SYNTHESIZING` goes straight to `CLOSED`). |
| `maxRounds` | int, 1 to 5 | `2` | Most rounds the draft is put to the table: round 1 is the draft, each further round a revision on the objections. At `2` the coordinator revises once. At `1` it never revises: an objection is released with the answer. |
| `roster` | `contributors` or `crew` | `contributors` | Who ratifies. `contributors`: every Tooler with a terminal `agree`, `concern` or `block` in the turn, and every Analyst that reviewed. `crew`: also every other Tooler and Analyst of the crew that has resumes. |

```yaml
spec:
  ratify:
    enabled: true
    maxRounds: 2
    roster: contributors
```

The operator reads the block of the crew's archetype (the same lookup as `review`) and gives it to the
crew's coordinator as `KUBEMOOT_DISCUSS_RATIFY_ENABLED`, `KUBEMOOT_DISCUSS_RATIFY_MAX_ROUNDS` and
`KUBEMOOT_DISCUSS_RATIFY_ROSTER`. An absent block, or a block without `enabled`, sets
`KUBEMOOT_DISCUSS_RATIFY_ENABLED=false`, and a coordinator that is given no value leaves ratification off. An Agent's own `spec.deployment.env` wins over both. An edit to the block re-reconciles
the coordinators.

The shipped `consent-3` archetype sets `ratify.enabled: false`. The separate
`consent-3-unratified` archetype is **deprecated** and is removed in the next release: it differs
from `consent-3` only by this setting, which is now the default. Change an `archetypeRef` that
names it to `consent-3`.

The field is optional and additive: a crew whose archetype has no `ratify` block works as it did
before ratification existed. Apply the new CRDs before the operator chart that sets the block (see
[Review](#review)).

### Signals

Each entry declares one kind of verdict. The runtime offers every agent of a crew on the
archetype one tool, `signal`, built from this list: its `kind` parameter takes the declared
names, and its description lists each name with its meaning. An agent records its verdict by
calling the tool; its reply text stays its finding. See
[How an agent records its verdict](../../concepts/signals-and-protocol/#how-an-agent-records-its-verdict).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | The kind, as the agent passes it: lower case letters, digits and underscores, at most 32 characters. Unique in the list. |
| `meaning` | string | required | What the kind says, in plain words, at most 300 characters. The model reads it to choose a kind. |
| `terminal` | bool | `true` | Whether the kind ends the agent's turn in the phase, so the phase can settle on it. A question is not terminal: the asker's turn stays open until it signals again. |
| `addressed` | bool | `false` | Whether the kind is said to one named agent. The tool then requires `to`, the agent's name. |

The runtime knows what these names do in a phase:

| Name | Published as | What the coordinator does |
|------|--------------|---------------------------|
| `agree` | `agree` | Counts the finding as standing. |
| `concern` | `concern` | Weighs the objection; the phase does not take the early settle while it stands. |
| `question` | `question` | Starts the [clarifying-question round](../../concepts/consensus-model/#clarifying-questions). |
| `stand_aside` | `stand_aside` | Counts the agent as having nothing to add. |
| `tool_gap` | `concern` whose content is the missing tool | Weighs it as a concern and feeds gap detection. |
| `no_data` | `failure` with `failureType: gather_failed` | Counts a failed gather, never an agreement. |
| `block` | `block` | Records a strong objection. |

An archetype may declare other names. The runtime publishes such a kind as a `signal` message
with the name in `metadata.signalKind`, ends the agent's turn when the kind is terminal, and
shows it to the synthesis. It has no other phase logic for it.

The operator gives the declared kinds to every agent of the crew as
`KUBEMOOT_DISCUSS_SIGNALS`, a JSON list. An edit to the list re-reconciles the crew's agents.
An archetype stored before kinds were declared, whose `signals` is a list of bare names, still
decodes; a bare name has no meaning, so it is not offered to a model, and an archetype with only
bare names offers no tool.

```yaml
spec:
  signals:
    - name: agree
      meaning: "Your finding stands: it answers your part of the question from what your tools returned."
    - name: question
      meaning: "You doubt a finding and want the agent that made it to recheck."
      terminal: false
      addressed: true
```

### Recheck

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `maxRounds` | int, 0 to 5 | `1` | Most clarifying questions the coordinator relays per thread. `0` turns the round off: a question is then recorded as the asker's concern. |

The operator gives the value to the crew's coordinator as `KUBEMOOT_DISCUSS_RECHECK_MAX_ROUNDS`.

```yaml
spec:
  recheck:
    maxRounds: 1
```

The `consent-3` archetype the operator installs declares the seven kinds in the table above and
`recheck.maxRounds: 1`. Apply the new CRDs before the operator chart that sets them (see
[Review](#review)).

### State machine

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `initial` | string | yes | The name of the starting phase. |
| `transitions` | list of transition | yes (at least one) | The legal edges. |

Each transition has:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `from` | string | yes | The phase the edge leaves. |
| `to` | string | yes | The phase the edge enters. |
| `on` | string | no | The trigger condition, such as `all-triaged` or `settled`. Informational. |

Quote `"on"` as a YAML key so it is not read as a boolean.

## Status

| Field | Description |
|-------|-------------|
| `valid` | `true` when the spec validates. |
| `message` | The result: a summary when valid, the first violation when not. |
| `conditions` | Standard Kubernetes conditions. |

`kubectl get mootarchetypes` (or `kubectl get moot`) lists each archetype with its
phase names and validity.

## What it does today

- **The runtime reads `review`, `ratify`, `signals` and `recheck`.** The signal kinds become
  each agent's signal tool, and `recheck` sets how many clarifying questions a thread may have.
- **The operator validates state-machine edges.** `stateMachine.initial` and every
  transition `from` and `to` must name a declared phase. A violation sets
  `status.valid` to `false` and reports the first offending name in `status.message`.
- **`CrewSchedulingPolicy` validates its phase names against it.** Each rule in a
  [`CrewSchedulingPolicy`](../../architecture/scheduler/) names a phase so the scheduler can choose a model
  for that phase. The policy's `archetypeRef` (default `consent-3`) selects the
  archetype, and every `rules[].phase` must be one of its declared phase names. A name
  that is not declared is reported in the policy's `status.validationError`. When the
  referenced archetype is not installed, the policy is accepted and the error notes
  that validation is waiting for the archetype.

## What it does not do yet

- **The runtime does not read the phases or the state machine.** Editing them does not change
  how a discussion runs. Editing `review`, `ratify`, `signals` or `recheck` does.
- **The discussion's phases and transitions are fixed in code.** The runtime moves a
  discussion through its own sequence: advisory, evaluating, review, synthesis, and ratify.
  That sequence does not come from the archetype.
- **The declared phases are scheduling phases.** The phases `consent-3` declares
  (`triaging`, `mulling`, `triage`, `evaluating`, `synthesis`) are the names a
  `CrewSchedulingPolicy` uses to choose a model per phase. They do not name the
  runtime's advisory, evaluating, review, synthesis, and ratify sequence, and there is no
  mapping between the two.
- **`role` and `on` are informational.** The operator does not interpret them.
- **A declared kind outside the runtime's table has no phase logic.** It is recorded, settles
  the agent's turn when terminal, and is shown to the synthesis; nothing else.
- **Adding an archetype does not add a behavior.** Applying a new `MootArchetype`
  extends the set of phase names a scheduling policy may use. It does not add a new way
  for a crew to deliberate.

## Direction

The aim is for an archetype to become the declared shape of a crew's deliberation:
its phases, who participates in each, and the decision points, with the decision
criteria governed by the crew's [ADL](../adl-reference/) and the runtime enforcing the
mechanics (timeouts, delivery, and the signal protocol). Changing how a crew
deliberates would then be a manifest edit, the way changing what an agent believes is
already a `PromptModule` edit. Consent, hierarchy, debate, and expert-panel patterns
are all valid archetypes under that model.

This is roadmap direction, not shipped behavior. See
[Consensus archetypes declared, not coded](../../introduction/roadmap/#consensus-archetypes-declared-not-coded)
on the roadmap and [Consensus Model](../../concepts/consensus-model/#consensus-archetypes)
for the concept.

## Related

- [Consensus Model](../../concepts/consensus-model/): the archetype concept.
- [Scheduler](../../architecture/scheduler/): how `CrewSchedulingPolicy` uses phases.
- [Models](../models/): model selection per phase.
