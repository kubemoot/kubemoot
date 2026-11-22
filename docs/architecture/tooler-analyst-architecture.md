---
title: "Tooler-Analyst Architecture"
description: "Kubemoot splits discussion work into Toolers that query live systems through MCP and Analysts that reason over their findings with RAG, in separate phases."
weight: 3
---

## Two Roles That Serve Different Phases

Kubemoot organizes the data-gathering and reasoning work of a discussion into two complementary roles that mirror how real teams operate: **Toolers** who interact with live systems, and **Analysts** who reason over what the Toolers found.

| | **Toolers (MCP)** | **Analysts (RAG)** |
|---|---|---|
| **Phase** | EVALUATING | REVIEW |
| **Declared** | Agents with MCP tools | Agents with embedded domain docs |
| **Self-discovered** | Onboarding agent deploys new MCP servers | RTFM agent indexes new documentation |
| **Carries** | MCP tools, no live tools in reasoning | RAG sources, no MCP tools |
| **Thinking** | OFF (deterministic tool selection) | ON (reasoning over gathered data) |
| **Analogy** | The person with their hands on the keyboard | The person with the manual open, weighing the evidence |
| **Accelerator profile** | Heavier (tool-calling inference) | Lighter (reasoning-focused, no tool overhead) |

### Why Separate Them

LLMs have a practical limit on how much context they can process while still reliably selecting tools and interpreting results. When a single agent carries both MCP tool schemas and RAG documentation in its system prompt, the prompt bloats and tool selection degrades. Separating concerns gives each agent a smaller, focused prompt: Toolers are better at picking the right tool, Analysts are better at weighing the evidence from those tools.

Both participate in the same NATS discussion thread. The Coordinator synthesizes live data from Toolers with reasoning from Analysts into a single answer.

---

## Declared Agents

Declared agents are defined in a crew's Helm chart. They represent known capabilities that the crew needs from day one. The examples below come from the Homelab Pilot reference crew.

### Toolers

A Tooler has MCP tools and uses them to interact with live systems. It calls APIs, queries databases, runs analyzers, and reports real data. Toolers run with thinking OFF: tool selection over a small, focused toolset is a deterministic pattern-match and does not benefit from a reasoning chain.

```
Agent Pod                    MCPGateway Pod               MCPServer Pod
+------------------+        +------------------+        +---------------------+
| Tooler           |--HTTP/--> Gateway         |--HTTP/--> MCP Bridge         |
|                  |   SSE  | (discovers       |   SSE  | (sidecar)           |
| LLM decides      |        | servers via      |        |    |                |
| which tool       |        | labels)          |        |    v stdio          |
| to call          |        |                  |        | MCP Server          |
|                  | <-res- |                  | <-res- | (k8sgpt, kubectl)   |
+------------------+        +------------------+        +---------------------+
```

Examples:
- **k8s-workloads** - 9 kubectl tools for pod/deployment inspection
- **k8sgpt** - 2 tools for deterministic cluster analysis
- **proxmox-pve** - 9 tools for Proxmox hypervisor management
- **obs-metrics** - 7 tools for Prometheus and kubectl queries

### Analysts

An Analyst has RAG-embedded documentation and reasons over the data that Toolers gathered. It provides domain interpretation, weighs evidence, and surfaces caveats. No live tools: just knowledge retrieved from indexed documents via semantic search, plus thinking ON so the reasoning chain is visible and auditable.

Analysts participate in the REVIEW phase and reason over Tooler output rather than providing upfront context.

```
Agent Pod                    Query Service Pod            pgvector
+------------------+        +------------------+        +------------------+
| Analyst          |--HTTP--> Query Service    |--SQL--> Vector Store       |
|                  |        | (semantic        |        | (embeddings)      |
| LLM reasons      |        |  search)         |        |                  |
| over RAG context |        |                  |        |                  |
| + Tooler data    | <-cks- |                  | <-res- |                  |
+------------------+        +------------------+        +------------------+
```

Examples:
- **k8s-analyst** - RAG sources covering kubectl, Kubernetes concepts, Helm, Talos, MCP tools; reasons over Toolers' findings in the REVIEW phase
- **proxmox-analyst** - RAG sources covering Proxmox API and PVE documentation; interprets Tooler data about VM/accelerator state

### Who reviews

Not every Analyst reviews every discussion. Each Analyst runs a full reasoning pass, so
convening one whose domain the question never touches adds time and no evidence (an
NVIDIA GPU Analyst reviewing a Proxmox answer, for example). The coordinator convenes reviewers by declared domain, the way it selects Toolers.

The consensus archetype says how ([`review`](../../reference/mootarchetype/#review) on the
`MootArchetype`). With `convene: domain`, an Analyst is convened when any of these is true:

1. A domain it declares ([`spec.domains`](../../reference/agent/#specdomains)) is a domain of
   the question. The coordinator already names the question's domains in the same model call that
   selects the Toolers, so convening costs no extra call.
2. A domain it declares is a domain of an agent that gathered data in this discussion (an
   agent that agreed, raised a concern, or was selected as a Tooler).
3. The coordinator selected it by name.

When no Analyst matches, the archetype's `fallback` decides: `primary` convenes the single
Analyst that shares the most words with the question and the gathering Toolers' domains, and
`all` convenes everyone. When nothing ranks the Analysts, all of them review. A crew with
Analysts therefore never reviews with none, and a crew that wants every Analyst to review sets
`convene: all`.

An Analyst that declares no `domains` is a generalist and reviews every discussion; a Tooler that
declares none gathers data without domain context, so it adds nothing to the match. A crew gets
the narrowing by declaring domains. The coordinator reports why it convened the reviewers in the `reviewBasis` of the `review_ready` message
(`domain`, `selected`, `primary`, `fallback-all` or `all`) and in its log.

### Ratifying the answer

Both roles come back after the coordinator drafts the answer. Every Tooler that agreed, raised a
concern or blocked, and every Analyst that reviewed, is woken by the `draft` message and checks the
draft against what it found: a Tooler against the results of its own tool calls, an Analyst against
the data it reasoned over. They check with the tools they already have for reading a stored result
(a search over the result, a query of it), not by rereading the draft and agreeing. An objection
names the statement and what the results show. See
[The Moot - Ratification](../../concepts/consensus-model/#ratification).

### How They Work Together

When a user asks "Why is my pod in CrashLoopBackOff?", the Toolers, the Analyst, and the coordinator contribute in sequence:

1. **k8s-workloads** (Tooler) calls `pods_get` and `events_list` to report the actual pod status, restart count, and error events
2. **k8sgpt** (Tooler) runs the `analyze` tool for a deterministic diagnostic scan
3. **k8s-analyst** (Analyst) receives the REVIEW-phase context, reads the Toolers' findings, and reasons over Kubernetes documentation to weight the evidence and surface the most likely causes
4. The **coordinator** synthesizes a draft: live data from Toolers plus reasoned interpretation from the Analyst equals a complete answer
5. When the archetype opts in (off by default), the Toolers and the Analyst **ratify** the draft against what they found, and the coordinator revises it once if they object

Neither type alone gives the full picture. Toolers know what is happening. Analysts know what it means and what to do about it.

---

## Self-Discovered Agents

A crew can recognize a gap and propose to fill it. Two agent roles in the agent runtime support this, and both are opt-in: the operator chart does not deploy them, and the operator does not create the resulting agents. See the [Onboarding Guide](../../operating/onboarding-guide/) for exactly what ships.

### Tool Gap: Onboarding Agent

When no Tooler can answer a question, the coordinator signals a gap. An agent in onboarding mode proposes an MCP server:

```
Gap signal -> Propose an MCP server to the user
    -> User consents -> You apply the MCPServer -> You create the Tooler Agent
```

### Knowledge Gap: RTFM Agent

An agent in RTFM mode listens for an onboarding-deployed event and finds documentation for the new server:

```
Onboarding-deployed event -> Search for docs (GitHub, web) -> Create RAGSource CR
    -> Indexer clones, chunks, embeds -> Query service available
```

The documentation enriches REVIEW-phase reasoning by Analysts about the newly onboarded domain. Creating the Analyst `Agent` that uses it is a manual step.

### The Symmetry

| Step | Tool System (MCP) | Knowledge System (RAG) |
|------|-------------------|----------------------|
| **Gap signal** | No Tooler agrees | New MCPServer with no docs |
| **Discovery agent** | Onboarding agent | RTFM agent |
| **What it proposes or creates** | Proposes an MCPServer | RAGSource CR |
| **What you create** | Tooler Agent | Analyst Agent |
| **Result** | New tools in the EVALUATING phase | New reasoning depth in the REVIEW phase |

---

## Accelerator Tiering: Two-Phase Inference

A crew with two accelerator tiers - one lighter, one heavier - can split inference into two
phases per discussion instead of sending every candidate agent straight to the
expensive tier. On the reference homelab (an RTX 5090 node and an RTX 4090 node) that
looks like this:

```
  Every candidate agent                Agents that pass triage
  +---------------------+           +---------------------+
  | Phase 1: Triage     |           | Phase 2: Mulling    |
  | lighter accel. tier |   --->    | primary accel. tier |
  | small triage model  | CONTRIBUTE| reasoning model     |
  | a few seconds/agent |           | tens of seconds/agent|
  | "Should I help?"    |           | Full tool-calling   |
  +---------------------+           +---------------------+
     most stand aside                  Publishes agree/concern
     (NOTHING_TO_ADD)                  with tool results
```

| Phase | Accelerator tier | Model | Purpose |
|---|---|---|---|
| **Triage** | Lighter (RTX 4090 on the reference homelab) | Small triage model | Quick relevance assessment: "do my tools apply to this question?" |
| **Mulling** | Primary (RTX 5090 on the reference homelab) | Reasoning model | Full inference with MCP tools: parse schemas, call tools, interpret results |
| **Synthesis** | Primary | Reasoning model | Coordinator synthesizes all agent contributions into a user-facing answer |

### Why Two Phases

Without triage, every candidate agent hits the primary accelerator for full inference; under a
server that serves one request at a time, that queue grows linearly with crew size
and the lighter tier sits idle. With triage, the lighter accelerator makes the cheap "should I
contribute?" decision for every candidate first. Most stand aside after a few seconds
of triage, and only the agents that pass proceed to the expensive tier for full
mulling - a large reduction in how many agents queue for the primary accelerator, and in
wall-clock time.

### Queue, don't fail closed

Triage does not fail an agent out on a short fixed timeout. The accelerator is a shared
resource, so an agent queued for triage publishes a `triaging` signal and keeps
heartbeating while it waits its turn; the coordinator tracks it as pending rather than
racing a clock. Only genuine unavailability - the triage call errors outright, or the
agent's deadline lapses with no heartbeat - results in a `stand_aside` or `failure`,
never a silent fall-through to the expensive tier. This prevents a saturated triage
tier from cascading load onto the primary tier while still giving a slow-queuing agent
the chance to be heard.

### Dashboard Visibility

The span graph on the discussions page shows which accelerator tier each agent used per
discussion, colored per server, alongside each signal. An agent that never reached
either tier (filtered before triage) shows no GPU badge at all.

---

## Beyond Infrastructure: Domain-Agnostic Crews

Kubemoot is the orchestration substrate; the domain comes from the crew, not from Kubemoot. The same Tooler-and-Analyst machinery serves infrastructure ops, Kafka operations, research, security, and more: each crew declares its own agents and points them at the operator. The cross-domain examples and the portability rationale live with the crew documentation: see [Crew CRD: Domain-Agnostic by Design](../reference/crew.md#domain-agnostic-by-design).

