---
title: "Models & Scheduling"
weight: 40
description: "Loose model coupling and just-in-time GPU scheduling."
---

Kubemoot composes capability from **many small models** rather than one large one,
and it binds an agent to a model **as late as possible**. Two ideas make that work:
loose model coupling and just-in-time scheduling.

## Loose model coupling

An `Agent` spec names no model. It declares abstract **capabilities** -
`tool-calling`, `reasoning`, `kubernetes`, and so on. Concrete models are separate
resources: a `Model` (an LLM, by capability labels, not a hardcoded name) served by a
`ModelProvider` (a GPU-backed inference endpoint such as Ollama on a particular GPU).

The match between the two is made by a `CrewSchedulingPolicy`, whose rules select
candidate `Model` CRs by their labels. Because the agent never names
`qwen3:8b` (or any model), you can swap models, add a provider, or re-tier a crew by
changing labels and policy - not by editing and redeploying every agent. The same crew
runs unchanged on different hardware.

## Just-in-time scheduling

Model selection happens **per inference call**, at decision time - not when the agent
reconciles. The scheduler reads live provider state (which models are loaded, which
GPUs are free) from a shared store and picks a `(model, provider, endpoint)` that
satisfies the agent's capabilities then. This matters because GPU inference, model
loading, and pod scheduling all have unpredictable latency: a static binding made at
deploy time would be wrong as soon as load shifted.

To avoid churn, the scheduler is **sticky** - it reuses an existing assignment while
that model and provider are still ready, rather than re-deciding on every fluctuation.
Cold start is treated the same as warm start: if a model isn't resident yet, the
system waits on the state transition rather than failing or requiring a manual warm-up.

## Why horizontal composition

Spreading work across small, swappable models keeps a crew **portable** (no dependency
on one vendor's frontier model) and **runnable on the GPUs you have** (commodity or
local). It also feeds the consensus model: Toolers gathering ground truth and Analysts
reasoning over it produce an answer the crew settles on, instead of one large model
asserting it. For
the operational details of the scheduler - spread vs. bin-pack, capacity discovery,
and provider state - see the [Scheduling architecture](../../architecture/scheduling/).

## Which models to actually pick

Loose coupling decides *how* a model is bound. It does not decide *which* model belongs
in which role, or what fits the GPU memory you have. For the selection framework - why
tool-calling fidelity outranks coding benchmarks, how to budget VRAM, why mixture-of-experts
models size by total parameters, and how to match model size to discussion role - see
[Choosing a Model](../choosing-a-model/).
