---
title: "Crews & Agents"
weight: 20
description: "Crews, coordinators, Toolers, Analysts, and how they compose."
---

A **crew** is a team of agents that deliberate together; an **agent** is one
participant in that team. Both are Kubernetes resources you declare and version like
any other workload.

## Crew

A `Crew` groups a set of `Agent`s - joined by the `kubemoot.ai/crew` label - into one
collaborating team, and configures crew-wide concerns: the discussion gateway (the
HTTP entry point external clients talk to) and the crew's **working memory**. Agents
are the workers; the crew is the team-level context they share.

The crew is also where the **domain** lives. Kubemoot is the orchestration substrate
and carries no domain knowledge of its own - an infrastructure crew, a Kafka crew, and
an oncology-research crew all run on the same machinery and differ only in the agents,
tools, and knowledge they declare. Because the domain belongs to the crew, a crew is
designed to be **portable** across clusters: nothing cluster-specific is baked into
agent prompts. What a crew learns about a particular cluster is discovered at runtime
and kept in working memory.

See the [Crew CRD reference](../../reference/crew/) for spec fields and the
working-memory policy.

## Agent

An `Agent` declares one participant as a thin, **capability-only** resource. The spec
carries no model name, no provider, and no GPU hint - an agent declares *what it can
do* (`spec.capabilities`, e.g. `tool-calling`, `reasoning`, `kubernetes`) and the
scheduler matches it to a concrete `(model, provider, endpoint)` at decision time. An
agent composes four things:

- **Models** - resolved by the scheduler from the agent's capabilities, never named
  in the spec. See [Models & Scheduling](../models-and-scheduling/).
- **Knowledge** - RAG retrieval via `spec.ragSources`.
- **Tools** - MCP tool execution through the gateway, filtered by
  `spec.enabledTools` / `spec.disabledTools`. See [MCP Tools](../mcp-tools/).
- **Prompts** - behavior composed from `PromptModule` CRs (written in
  [ADL](../agent-definition-language/)) referenced by `spec.promptRefs`. Prompt
  text always lives in PromptModules, never inline.

See the [Agent CRD reference](../../reference/agent/) for the full spec.

## Agent roles

Within a crew an agent plays a **role** (`spec.discussRole`):

- A **coordinator** receives the question, convenes the Toolers whose expertise
  fits it, and synthesizes the answer once the discussion settles. A crew has one
  coordinator.
- A **Tooler** (`discussRole: tooler`) acts in the EVALUATING phase with thinking
  OFF. It calls its domain MCP tools, surfaces live data, and contributes a finding
  carrying a consensus signal (`agree`, `concern`, `stand_aside`, `block`,
  `failure`). An agent with no `discussRole` set defaults to `generic` and
  participates as a general contributor without the Tooler raw-output contract.
- An **Analyst** (`discussRole: analyst`) acts in the REVIEW phase (or the one-analyst concurrence check) with thinking ON.
  It carries RAG sources and reasons over the data Toolers gathered, weighing the
  evidence before synthesis. Analysts hold no live MCP tools.
- A **researcher** contributes to synthesis but is excluded from settle triggers and
  gap detection. Used for non-settle-gating augmentation such as internet search,
  where you want context without requiring a definitive answer.

## How they compose

A question enters through the crew's discussion gateway, the coordinator selects a
subcommittee of relevant Toolers, each Tooler calls its domain tools and deliberates
over the message bus, the coordinator wakes Analysts by name to check or review the
Toolers' findings, and the coordinator composes the result. The crew is the unit you
deploy and operate; the agents are how it thinks. The deliberation itself, signals,
phases, and how a crew settles, is covered in [The Moot](../consensus-model/) and
[Signals & Protocol](../signals-and-protocol/).

## Building and designing a crew

For the mechanics of declaring the Kubernetes resources see
[Build a Crew](../../user-guides/build-a-crew/). For the design decisions inside
those resources, which Toolers and Analysts to include, how to write resumes the
coordinator reasons over, and how to ground the coordinator so it routes well and
writes helpful advisory briefs, see [Compose a Crew](../../user-guides/compose-a-crew/).
