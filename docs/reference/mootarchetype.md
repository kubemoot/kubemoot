---
title: "MootArchetype CRD"
weight: 9
description: "The MootArchetype resource: the declared phases, signals, and state machine of a consensus archetype. A v1alpha1 API that the operator validates today and the agent runtime does not yet read."
---

## Overview

A `MootArchetype` declares the shape of a consensus archetype: the phases it passes
through, the signals agents may emit, and the legal transitions between phases. The
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
  signals: [agree, concern, stand_aside, block, advisory]
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
| `signals` | list of string | no | The signal types agents may emit during a discussion. An open set of names; informational. |
| `stateMachine` | object | no | The legal phase transitions. See [State machine](#state-machine). When absent, progression is sequential in the order the phases are declared. |
| `review` | object | no | Which analysts the coordinator convenes for review. See [Review](#review). When absent, every analyst reviews. |

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

A crew that has analysts never reviews with none. An analyst that declares no `domains` is a generalist and reviews every discussion in `domain` mode. When `domain` matches nothing and nothing
ranks the analysts either (no analyst declares a domain the question or the data touches), every
analyst reviews, because choosing one would be arbitrary. See
[Who reviews](../../architecture/tooler-analyst-architecture/#who-reviews) for how the match works.

```yaml
spec:
  review:
    convene: domain
    fallback: primary
```

The operator reads the block of the crew's archetype (the `archetypeRef` of the
`CrewSchedulingPolicy` naming the crew, `consent-3` without one) and gives it to the crew's
coordinator as `KUBEMOOT_DISCUSS_REVIEW_CONVENE` and `KUBEMOOT_DISCUSS_REVIEW_FALLBACK`. The
`consent-3` archetype the operator installs sets `convene: domain`. An edit to the block
re-reconciles the coordinators. An Agent's own `spec.deployment.env` wins over both.

The CRD gains the `review` field, so apply the new CRDs before the operator chart that sets it. Flux installs use `crds: CreateReplace` on the HelmRelease; plain `helm` installs apply the CRDs first (`kubectl apply -f` the chart's `crds/` directory), because `helm upgrade` does not update CRDs.

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

- **The agent runtime reads only `review`.** Editing the phases, signals or state machine of
  a `MootArchetype` does not change how a discussion runs; editing `review` changes which
  analysts review.
- **The discussion's phases and transitions are fixed in code.** The runtime moves a
  discussion through its own sequence: advisory, evaluating, review, and synthesis.
  That sequence does not come from the archetype.
- **The declared phases are scheduling phases.** The phases `consent-3` declares
  (`triaging`, `mulling`, `triage`, `evaluating`, `synthesis`) are the names a
  `CrewSchedulingPolicy` uses to choose a model per phase. They do not name the
  runtime's advisory, evaluating, review, and synthesis sequence, and there is no
  mapping between the two.
- **`role`, `signals`, and `on` are informational.** The operator does not interpret
  them.
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
