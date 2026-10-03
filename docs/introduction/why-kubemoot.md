---
title: "Why Kubemoot"
weight: 10
description: "Where Kubemoot fits, the problems it solves, and how it differs from other approaches."
---

Kubemoot runs agentic AI as a **committee of small, specialized models that
deliberate to consensus** - on your own Kubernetes cluster, declared as Kubernetes
resources, and measured by executable fitness functions. This page is about *where
that fits*, *what it solves*, and *how it differs*.

## Where it started, and where it can go

Kubemoot began as a small agent system to help with observability, security, and
administration on one homelab cluster, and grew into a general way to run crews of
agents on Kubernetes. Today it works well on local clusters with a couple of GPUs or
more, running open models you serve yourself.

The core does not change as it grows: agents around a table, each bringing its own
expertise and tools, deliberating until the crew settles on an answer. What can
change is who sits at the table and what powers them. The [Roadmap](../roadmap/)
describes directions such as rented cloud GPUs for capacity a cluster does not own,
and hosted frontier models joining local agents as voices in the same discussion.
Those are directions, not features; this page describes what Kubemoot is today.

## Who it's for

- **Platform and infrastructure teams** who want agentic AI to live where the rest
  of their workloads do - Kubernetes - with the same reconciliation, scheduling,
  RBAC, and observability they already operate.
- **Teams running on their own GPUs** (commodity or local), who would rather compose
  capability from many small open models than depend on a single hosted frontier
  model.
- **People who need answers they can trust and re-check** - where a crew's quality
  is something you measure over time, not assume.

If you want a hosted chatbot or a one-line SaaS call, Kubemoot is not the shortest
path. It earns its keep when the agents, their prompts, and their evaluation need to
be **operable infrastructure**.

## The problems it solves

1. **The orchestration gap.** Agent frameworks are excellent for prototyping but leave
   deployment, scaling, and operations to you. Kubemoot makes crews, agents, prompts,
   and models **first-class Kubernetes resources** - reconciled by an operator, with
   GPU-aware scheduling, an MCP tool gateway, and live observability. See
   [The Orchestration Gap](../orchestration-gap/).
2. **Frontier-model dependency.** Instead of routing everything through one large
   hosted model, Kubemoot composes capability **horizontally** from many small models
   scheduled just-in-time across the GPUs you have - portable across clusters, no
   single-vendor dependency.
3. **Opaque quality.** Crew behavior is measured by **fitness functions** -
   reference-grounded, fabrication-aware scenarios - so you can track whether a change
   helped or hurt rather than eyeballing transcripts.
4. **Brittle single-model answers.** A lone model is confidently wrong in one voice.
   A Kubemoot crew **deliberates**: Toolers and Analysts raise `concern` or `block`,
   abstain with `stand_aside`, and a coordinator synthesizes. Failure is first-class
   signal, not silence.

## What differentiates it

- **vs. a single model or RAG chatbot** - Kubemoot is a *deliberating assembly*, not
  one model. Consensus signals make disagreement and abstention explicit; the answer
  is settled by the crew, not asserted by one voice.
- **vs. code-based agent frameworks** - agents, prompts (ADL), and models are
  **declarative Kubernetes objects** authored with `kubectl apply` and versioned with
  `git diff`, not code that must be recompiled and redeployed. Operations -
  scheduling, tool discovery, cleanup - are built into the operator.
- **vs. other Kubernetes-native agent projects such as kagent** - these also declare
  agents as Kubernetes resources. What Kubemoot adds today is a deliberation protocol
  between agents (consensus signals and a coordinator that synthesizes the answer),
  GPU-aware scheduling that picks a model server per inference call, and executable
  fitness functions that score a crew over time. See
  [Related Projects](../related-projects/) for a fuller comparison.
- **vs. model-serving layers such as KServe, llm-d, and vLLM** - those serve models;
  Kubemoot sits above them: it schedules each inference call onto a model server it can
  read state from. Ollama is the only model server it drives today; reading engine state from other
  servers is on the [Roadmap](../roadmap/). See [Related Projects](../related-projects/).
- **vs. hosted agent platforms** - it runs on **your** infrastructure and GPUs, stays
  portable across clusters, and keeps your data and models in your control.
- **vs. agent harnesses such as Claude Code** - not a competitor but a table they can
  use. A harness can ask a whole crew a question as one of its tools through the
  [crew liaison](../../integrations/crew-liaison/), and a crew is itself a harness made
  of harnesses; see [The Table and the Harnesses](../../concepts/the-table-and-the-harnesses/).

## What it is not

Kubemoot is a young, independent open-source project. It assumes Kubernetes and at
least one GPU-backed model provider, and it is built for multi-step, judgment-bearing
questions - not for trivial single-shot calls where a deliberating committee is
overkill.

Its goals and features are not fixed. As people run crews on their own clusters and
report what works and what gets in the way, Kubemoot will refine what it sets out to do
and how it does it. Share yours in
[Discussions](https://github.com/orgs/kubemoot/discussions) or open an issue; the
[Roadmap](../roadmap/) shows the directions under consideration today.

Next: the [Overview](../kubemoot-brief/) for what's in the box, or the
[Quickstart](../quickstart/) to run a crew.
