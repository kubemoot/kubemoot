---
title: "Questions"
weight: 70
description: "The questions people ask first, answered briefly, with where to read more."
---

Short answers, each one a position you can hold us to, with the page that backs it.

## Why not just use Claude Code with a frontier model?

Often you should. A frontier model wins open-ended, one-off work where data may leave
your network and per-token cost is acceptable. Kubemoot is for the other situation:
data that stays home, cost that is fixed by hardware rather than by usage, behaviour
governed as code, and agents that run where your platform already runs, under its
RBAC, quotas, and lifecycle. It is not either-or: Claude Code can use a crew as one
agent through the [crew liaison](../../integrations/crew-liaison/), the frontier model
at the edge and the private crew on the domain work. We do not claim that a crew of
small models out-reasons a frontier model; that is not what it is for. What a crew
does give you is the choice of where the compute is spent: tokens billed by a
provider, or watts on hardware you run.

## Why Kubernetes, and why an operator rather than a library?

Agents are long-running, stateful, resource-hungry workloads that need scheduling,
isolation, and lifecycle. Kubernetes already solves those. An operator makes crews,
agents, models, and rules first-class resources that reconcile to a declared state,
with `kubectl apply` as the interface. See
[The Orchestration Gap](../orchestration-gap/).

## Why not CrewAI, LangGraph, or AutoGen?

Use them when an in-process library fits: one application, a notebook, a prototype.
Kubemoot is for crews that run as shared platform services with tenancy, GitOps, and
GPUs to schedule. Different job; see [Why Kubemoot](../why-kubemoot/).

## Why a crew instead of one agent?

Separate roles with separate tools, context, and rules; a coordinator that convenes
only who is relevant; agreement signals instead of one unchecked answer; and failure
as a first-class signal rather than silence. Whether a crew answers better than one
agent on the same models is a hypothesis we measure with
[fitness functions](../../fitness/), not a claim we make. See
[The Consensus Model](../../concepts/consensus-model/).

## What is ADL, and does it make agents better?

Architecture Definition Language: WHEN/THEN, ASSERT, and NEVER rules from
[*Architecture as Code*](https://www.dijure.com/books/architecture-as-code/) (Richards,
Ford, and Johnson), applied to agents as `PromptModule` resources. The value we claim today is governance: rules you can read, review, diff,
and change with `kubectl apply`. Quality gains are under measurement; the interim
result is a tie with prose on quality and a lead on speed, on a small sample. See
[Write Agents and ADL](../../user-guides/write-agents-and-adl/).

## Isn't it slow?

A crew deliberates, and consensus takes minutes on smaller systems with GPUs you can
count on one hand; the [integrations](../../integrations/claude-code/) page shows a
real three-minute answer. The time is spent in the models and the serving engine, both of which are
replaceable behind the `ModelProvider` boundary ([Roadmap](../roadmap/)). The ticket
contract exists because crews take time, and the crew's answer carries its own
caveats, which is worth the wait more often than a fast confident guess.

## Do I need a GPU?

Not to try it: the [Quickstart](../quickstart/) runs a real two-agent discussion on a
2-CPU laptop with a small model. Yes for useful crews: larger models, more agents,
answers in tens of seconds rather than minutes. The two profiles are laid out in
[Installation](../installation/).

## Can I use hosted model APIs instead of local models?

Not yet. The `openai` and `anthropic` provider types exist on the CRD but are stubs;
Ollama is the model server today. Cloud and frontier models as crew members is on the
[Roadmap](../roadmap/), with data handling and cost controls as its prerequisites.

## Is it production-ready?

No. The API is `v1alpha1`, and Kubemoot is built and run on a homelab with
production-like patterns, not in production. Known limitations, including the open
message bus inside the cluster, are listed in the "Security model and known
limitations" section of the repository's
[SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md). Treat it
as a platform to evaluate and shape, and say so where you deploy it.

## Can agents change things, or only answer?

They answer and they generate; they do not change systems yet. A crew's output can be
new material as well as findings: a quiz written from a course's reading, a game
invented and played in chat, a report. What no crew does today is change the state of
anything outside the discussion. Nothing technical prevents it: an MCP server whose
tools write plugs into the same gateway as one whose tools read. What is missing is
testing and guardrails, consent before a change, an audit trail after it, and
permissions scoped to the one change allowed, and those come before any crew that
acts. See [Crews that act](../roadmap/#crews-that-act) on the Roadmap.

## How much of this was written with AI?

A great deal, in the open. Kubemoot is built with AI assistance and held to the same
gates as any contribution: tests with every change, a cyclomatic-complexity limit,
a SonarQube quality gate, and code review. The gates, not the authors, are what the
code answers to.

## What does "moot" mean?

An assembly that meets to discuss and decide. A Kubemoot crew does the same: every
voice, one answer. See [About the Name](../about-the-name/).

## Have a question that is not here?

Ask it in [Discussions](https://github.com/orgs/kubemoot/discussions), open an
[issue](https://github.com/kubemoot/kubemoot/issues) if something is wrong or missing,
or write to **moot@kubemoot.org**. Questions that come up more than once end up on
this page.
