---
title: "Overview"
weight: 20
description: "What Kubemoot is, its building blocks, and how a question becomes an answer."
---

Kubemoot is a Kubernetes operator and runtime for **multi-agent AI consensus**. A
question is answered not by one model but by a **crew** of small, specialized agents
that deliberate over a message bus and settle by signal. Everything - the crew, its
agents, their prompts, and the models they use - is a Kubernetes resource you
declare and version like any other.

## The mental model

A **moot** is an assembly that reaches a decision by deliberation. In Kubemoot:

- A **coordinator** receives a question and convenes the Toolers whose expertise
  fits it.
- Each **Tooler** investigates with its domain MCP tools and contributes a finding
  carrying a **signal** - `agree`, `concern`, `stand_aside`, `block`, or `failure`.
- **Analysts** (if the crew has them) reason over the Toolers' findings in the REVIEW
  phase, adding interpretive depth before synthesis.
- The coordinator watches the signals and, when the discussion settles, **synthesizes**
  the answer. No single model dictates it; consensus emerges from the crew.

The coordinator-facilitated flow above is the **consensus archetype** that ships
today. Other organizations, such as a hierarchy, are valid archetypes that the design
leaves room for; the runtime fixes the orchestration in code today, so the
deliberation *goal* stays constant and the structure does not yet vary.

Capability is composed **horizontally** from many small models rather than one large
one - portable across clusters and runnable on commodity or local GPUs.

## Building blocks (all Kubernetes resources)

| Resource | What it is |
|----------|------------|
| **Crew** | A group of agents that deliberate together. |
| **Agent** | One participant: a `coordinator`, a `Tooler`, or an `Analyst`, with declared capabilities and tools. |
| **PromptModule** | An agent's behavior, written in ADL (the Architecture Definition Language); composed by reference. |
| **Model** / **ModelProvider** | A model (by capability, not a hardcoded name) and the GPU-backed endpoint that serves it. |
| **RAGSource** | A knowledge source that's indexed and made queryable for agents. |
| **MCPServer** / **MCPGateway** | Tools an agent can call, discovered through a gateway (Model Context Protocol). |
| **CrewFitness** / **CrewFitnessSuite** | Executable tests that score a crew against ground truth. |

Because these are CRDs, you author crews with `kubectl apply`, review changes with
`git diff`, and never recompile to change behavior.

## How a question flows

1. A question is posted to a crew's discussion endpoint.
2. The coordinator selects the relevant Toolers and opens a discussion thread on
   the message bus (NATS).
3. Toolers triage, then evaluate, calling their MCP tools and publishing findings with
   consensus signals. Analysts reason over the gathered data in the REVIEW phase.
   Models are scheduled **just in time** onto available GPUs.
4. When the signals settle, the coordinator synthesizes the answer and the thread
   closes. Failure is surfaced as signal, never hidden.

## What runs it

The **operator** (a Kubernetes controller) reconciles the resources above; the
**agent runtime** hosts each agent; **NATS JetStream** carries the discussion; an
**MCP gateway** brokers tools. You bring a Kubernetes cluster and at least one
GPU-backed model provider.

## Next

- [Why Kubemoot](../why-kubemoot/) - where it fits and how it differs.
- [Concepts](../../concepts/) - the consensus model, crews, signals, and scheduling in depth.
- [Quickstart](../quickstart/) - run a crew and ask it a question.
