---
title: "The Realm and the Crew"
weight: 21
description: "Kubemoot's resources fall into two layers: the realm a crew draws on, and the crew itself. Which kinds sit where, and why a crew stays portable."
---

Kubemoot has about twenty resource kinds. They fall into two layers, plus a small cluster
level above both:

- **The realm** is what crews draw on: the working context and the limits a crew works
  within. Models, tools, knowledge, and where notifications go.
- **The crew** is the crew itself: who sits at the table and how it behaves.

<svg role="img" aria-label="Three layers. At the top, the cluster level: KubemootConfig and MootArchetype. Below it, two boxes side by side: the crew on the left (Crew, Agent, PromptModule, Skill, CrewFitness, CrewFitnessSuite) and the realm on the right (models, tools, knowledge, delivery). An arrow labeled capabilities points from the crew to the realm, and CrewSchedulingPolicy sits on the line between them." viewBox="0 0 640 300" width="100%" style="max-width:640px" font-family="sans-serif" font-size="13" fill="none" stroke="currentColor" stroke-width="1.5">
<rect x="10" y="10" width="620" height="50" rx="6"/>
<text x="320" y="31" text-anchor="middle" fill="currentColor" stroke="none" font-weight="bold">Cluster level</text>
<text x="320" y="49" text-anchor="middle" fill="currentColor" stroke="none">KubemootConfig, MootArchetype</text>
<rect x="10" y="90" width="270" height="200" rx="6"/>
<text x="145" y="112" text-anchor="middle" fill="currentColor" stroke="none" font-weight="bold">The crew</text>
<text x="145" y="145" text-anchor="middle" fill="currentColor" stroke="none">Crew, Agent</text>
<text x="145" y="175" text-anchor="middle" fill="currentColor" stroke="none">PromptModule, Skill</text>
<text x="145" y="205" text-anchor="middle" fill="currentColor" stroke="none">CrewFitness, CrewFitnessSuite</text>
<rect x="360" y="90" width="270" height="200" rx="6"/>
<text x="495" y="112" text-anchor="middle" fill="currentColor" stroke="none" font-weight="bold">The realm</text>
<text x="495" y="140" text-anchor="middle" fill="currentColor" stroke="none">Models: ModelServer, Model,</text>
<text x="495" y="157" text-anchor="middle" fill="currentColor" stroke="none">EmbeddingModel</text>
<text x="495" y="184" text-anchor="middle" fill="currentColor" stroke="none">Tools: MCPServer, MCPGateway,</text>
<text x="495" y="201" text-anchor="middle" fill="currentColor" stroke="none">MCPCatalog, MCPQualityPolicy,</text>
<text x="495" y="218" text-anchor="middle" fill="currentColor" stroke="none">MCPServerReport</text>
<text x="495" y="244" text-anchor="middle" fill="currentColor" stroke="none">Knowledge: RAGSource</text>
<text x="495" y="272" text-anchor="middle" fill="currentColor" stroke="none">Delivery: NotificationSink</text>
<line x1="280" y1="150" x2="352" y2="150"/>
<polygon points="360,150 350,145 350,155" fill="currentColor"/>
<text x="320" y="140" text-anchor="middle" fill="currentColor" stroke="none">asks for</text>
<text x="320" y="170" text-anchor="middle" fill="currentColor" stroke="none">capabilities</text>
<rect x="215" y="240" width="140" height="40" rx="6" fill="none" stroke-dasharray="5 3"/>
<text x="285" y="257" text-anchor="middle" fill="currentColor" stroke="none">CrewSchedulingPolicy</text>
<text x="285" y="272" text-anchor="middle" fill="currentColor" stroke="none">on the line</text>
</svg>

## The cluster level

[KubemootConfig](../../reference/kubemootconfig-guide/) and
[MootArchetype](../../reference/mootarchetype/) are cluster-scoped. They set how Kubemoot
runs and which consensus archetypes exist, for every crew in the cluster.

## The realm

| Part | Kinds | What it gives a crew |
|---|---|---|
| Models | `ModelServer`, `Model`, `EmbeddingModel` | The catalog of models the cluster can run, and the servers that serve them. The cluster owner keeps `ModelServer`s and `Model`s in the operator's namespace |
| Tools | `MCPServer`, `MCPGateway`, `MCPCatalog`, `MCPQualityPolicy`, `MCPServerReport` | The tools agents may call, the gateway they reach them through, and the rules for which community servers are let in. `MCPServerReport` is generated, not written: it records how each server fared in trials, as a status report beside the quality policy that reads it |
| Knowledge | `RAGSource` | Indexed documents an agent can search |
| Delivery | `NotificationSink` | Where a concern raised in a discussion is sent |

These are namespaced kinds, and where one lives depends on who needs it. The model catalog
is shared, so it sits with the operator. A crew's own tools, knowledge, and notification
sink can ship with the crew, in the crew's namespace, and serve that crew only.

## The crew

| Part | Kinds | What it is |
|---|---|---|
| The crew itself | `Crew`, `Agent` | Who sits at the table and the role each plays. See [Crews and agents](../crews-and-agents/) |
| Its rules and know-how | `PromptModule`, `Skill` | The [ADL](../agent-definition-language/) rules that govern an agent and the skills it can load |
| Its evidence | `CrewFitness`, `CrewFitnessSuite` | Scenarios that score the crew against known answers. See [Fitness](../../fitness/) |

### On the line: CrewSchedulingPolicy

A `CrewSchedulingPolicy` belongs to a crew, but it says how that crew draws on the
realm: for example, quality over speed, or which capabilities are hard needs and which
are wishes. It is the crew resource that selects from the catalog.

## The point: crews ask, the realm fills

An agent declares the capability it needs, never a model, a provider, or an endpoint.
The scheduler places each call on what the realm offers at that moment (see
[Models and scheduling](../models-and-scheduling/)).

That is why a crew is portable. The same crew runs on a small cluster with two accelerators, on a
cluster with a large pool, or against a catalog that changes underneath it, with no change to
the crew: moving a model to another accelerator changes the realm, not the crew's namespace.

It also divides the work. A platform team curates the realm: which servers run, which models
the catalog holds, which tool sources are allowed. Crew authors write crews and ask for
capabilities. Neither edits the other's resources.

## Related

- [Crews and agents](../crews-and-agents/)
- [Models and scheduling](../models-and-scheduling/)
- [Choosing a model](../choosing-a-model/)
