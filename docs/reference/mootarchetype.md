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
  description: "Consent decision-making, the archetype Kubemoot ships. Today its phases are the scheduling phases a CrewSchedulingPolicy rule may name; the operator's per-phase model selection reads mulling and triage."
  phases:
    - name: triaging
      role: gatekeeping
      description: "Scheduling phase name for the coordinator's selection of the agents that join a discussion."
    - name: mulling
      role: deliberation
      description: "Scheduling phase: the model an agent's tool-calling evaluation runs on."
    - name: triage
      role: deliberation
      description: "Scheduling phase: the lighter model an agent's should-I-contribute check runs on."
    - name: evaluating
      role: deliberation
      description: "Scheduling phase name for the deliberation, where agents emit agree, concern, stand_aside, or block."
    - name: synthesis
      role: closure
      description: "Scheduling phase name for the coordinator's synthesis of the answer."
  signals: [triaging, evaluating, agree, concern, stand_aside, block, advisory, proposal, consent]
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

### Phases

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | The phase name. `CrewSchedulingPolicy` rules reference it. |
| `role` | string | no | What the phase accomplishes, such as `gatekeeping`, `deliberation`, or `closure`. Informational. |
| `description` | string | no | A one-line explanation shown in dashboards. |

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
- **The declared phases are the scheduling phase names.** The phases `consent-3` declares
  are the names a `CrewSchedulingPolicy` rule may name, and the operator's per-phase model
  selection reads `mulling` and `triage`.
- **`CrewSchedulingPolicy` validates its phase names against it.** Each rule in a
  [`CrewSchedulingPolicy`](../../architecture/scheduler/) names a phase so the scheduler can choose a model
  for that phase. The policy's `archetypeRef` (default `consent-3`) selects the
  archetype, and every `rules[].phase` must be one of its declared phase names. A name
  that is not declared is reported in the policy's `status.validationError`. When the
  referenced archetype is not installed, the policy is accepted and the error notes
  that validation is waiting for the archetype.

## What it does not do yet

- **The agent runtime does not read it.** No part of the agent runtime consumes a
  `MootArchetype`. Editing one does not change how a discussion runs.
- **The discussion's phases and transitions are fixed in code.** The runtime moves a
  discussion through its own sequence: advisory, evaluating, deciding, concurring
  (when chosen), review, and synthesis. That sequence does not come from the archetype.
- **The review decision is not archetype-driven.** A crew declares it on its coordinator
  (environment variables and a `PromptModule`), not in the archetype.
- **The declared phases are scheduling phases.** The phases `consent-3` declares
  (`triaging`, `mulling`, `triage`, `evaluating`, `synthesis`) are the names a
  `CrewSchedulingPolicy` uses to choose a model per phase. They do not name the
  runtime's advisory, evaluating, deciding, concurring, review, and synthesis sequence, and there is no
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
