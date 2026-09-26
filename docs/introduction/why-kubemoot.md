---
title: "Why Kubemoot"
weight: 10
description: "Where Kubemoot fits, the problems it solves, and how it differs from other approaches."
---

Kubemoot runs agentic AI as a **committee of small, specialized models that
deliberate to consensus** - on your own Kubernetes cluster, declared as Kubernetes
resources, and measured by executable fitness functions. This page is about *where
that fits*, *what it solves*, and *how it differs*.

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
- **vs. hosted agent platforms** - it runs on **your** infrastructure and GPUs, stays
  portable across clusters, and keeps your data and models in your control.

## What it is not

Kubemoot is a young, independent open-source project. It assumes Kubernetes and at
least one GPU-backed model provider, and it is built for multi-step, judgment-bearing
questions - not for trivial single-shot calls where a deliberating committee is
overkill.

Next: the [Overview](../kubemoot-brief/) for what's in the box, or the
[Quickstart](../quickstart/) to run a crew.
