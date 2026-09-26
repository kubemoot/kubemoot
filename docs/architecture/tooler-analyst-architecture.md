---
title: "Tooler-Analyst Architecture"
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
| **GPU profile** | Heavier (tool-calling inference) | Lighter (reasoning-focused, no tool overhead) |

### Why Separate Them

LLMs have a practical limit on how much context they can process while still reliably selecting tools and interpreting results. When a single agent carries both MCP tool schemas and RAG documentation in its system prompt, the prompt bloats and tool selection degrades. Separating concerns gives each agent a smaller, focused prompt: Toolers are better at picking the right tool, Analysts are better at weighing the evidence from those tools.

Both participate in the same NATS discussion thread. The Coordinator synthesizes live data from Toolers with reasoning from Analysts into a single answer.

---

## Declared Agents

Declared agents are defined in the application's Helm chart (e.g., homelab-pilot). They represent known capabilities that the team needs from day one.

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

The former per-layer RAG agents (k8s-advisor, proxmox-advisor, obs-advisor, nvidia-gpu-advisor, scheduler-advisor) are Analysts: they participate in the REVIEW phase rather than the old ADVISORY phase and reason over Tooler output rather than providing upfront context.

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
- **proxmox-analyst** - RAG sources covering Proxmox API and PVE documentation; interprets Tooler data about VM/GPU state

### How They Work Together

When a user asks "Why is my pod in CrashLoopBackOff?", the three tiers contribute in sequence:

1. **k8s-workloads** (Tooler) calls `pods_get` and `events_list` to report the actual pod status, restart count, and error events
2. **k8sgpt** (Tooler) runs the `analyze` tool for a deterministic diagnostic scan
3. **k8s-analyst** (Analyst) receives the REVIEW-phase context, reads the Toolers' findings, and reasons over Kubernetes documentation to weight the evidence and surface the most likely causes
4. The **coordinator** synthesizes: live data from Toolers plus reasoned interpretation from the Analyst equals a complete answer

Neither type alone gives the full picture. Toolers know what is happening. Analysts know what it means and what to do about it.

---

## Self-Discovered Agents

Kubemoot does not just run what you declare: it recognizes gaps and fills them autonomously. The two self-discovery systems are symmetric.

### Tool Gap: Onboarding Agent (Tool Shopper)

When no Tooler can answer a question (0 agrees, multiple stand-asides), the onboarding agent detects the gap and searches for MCP tools:

```
Gap Detected -> Search MCP registries -> Evaluate quality -> Propose to user
    -> User consents -> Deploy MCPServer CR -> Operator creates Tooler Agent
```

The onboarding agent is a **tool shopper**: it finds, evaluates, and deploys new capabilities. Quality checks include GitHub stars, recent commits, known authors, and suspicious keyword filtering.

### Knowledge Gap: RTFM Agent (Research Librarian)

When a new MCP server is onboarded, the RTFM agent automatically finds and indexes its documentation:

```
MCPServer deployed -> Search for docs (GitHub, web) -> Create RAGSource CR
    -> Indexer clones, chunks, embeds -> Query service available
    -> Operator creates Analyst Agent
```

The RTFM agent is a **research librarian**: it finds authoritative documentation, indexes it into the vector store, and makes it available to Analysts. The documentation enriches future REVIEW-phase reasoning about the newly onboarded domain.

### The Symmetry

| Step | Tool System (MCP) | Knowledge System (RAG) |
|------|-------------------|----------------------|
| **Gap signal** | 0 Tooler agrees | New MCPServer with no docs |
| **Discovery agent** | Onboarding agent | RTFM agent |
| **What it creates** | MCPServer CR | RAGSource CR |
| **What the operator creates** | Tooler Agent | Analyst Agent |
| **Result** | New tools in the EVALUATING phase | New reasoning depth in the REVIEW phase |

---

## GPU Tiering: Two-Phase Inference

A crew with two GPU tiers - one lighter, one heavier - can split inference into two
phases per discussion instead of sending every candidate agent straight to the
expensive tier. On the reference homelab (an RTX 5090 node and an RTX 4090 node) that
looks like this:

```
  Every candidate agent                Agents that pass triage
  +---------------------+           +---------------------+
  | Phase 1: Triage     |           | Phase 2: Mulling    |
  | lighter GPU tier    |   --->    | primary GPU tier    |
  | small triage model  | CONTRIBUTE| reasoning model     |
  | a few seconds/agent |           | tens of seconds/agent|
  | "Should I help?"    |           | Full tool-calling   |
  +---------------------+           +---------------------+
     most stand aside                  Publishes agree/concern
     (NOTHING_TO_ADD)                  with tool results
```

| Phase | GPU tier | Model | Purpose |
|---|---|---|---|
| **Triage** | Lighter (RTX 4090 on the reference homelab) | Small triage model | Quick relevance assessment: "do my tools apply to this question?" |
| **Mulling** | Primary (RTX 5090 on the reference homelab) | Reasoning model | Full inference with MCP tools: parse schemas, call tools, interpret results |
| **Synthesis** | Primary | Reasoning model | Coordinator synthesizes all agent contributions into a user-facing answer |

### Why Two Phases

Without triage, every candidate agent hits the primary GPU for full inference; under a
provider that serves one request at a time, that queue grows linearly with crew size
and the lighter tier sits idle. With triage, the lighter GPU makes the cheap "should I
contribute?" decision for every candidate first. Most stand aside after a few seconds
of triage, and only the agents that pass proceed to the expensive tier for full
mulling - a large reduction in how many agents queue for the primary GPU, and in
wall-clock time.

### Queue, don't fail closed

Triage does not fail an agent out on a short fixed timeout. The GPU is a shared
resource, so an agent queued for triage publishes a `triaging` signal and keeps
heartbeating while it waits its turn; the coordinator tracks it as pending rather than
racing a clock. Only genuine unavailability - the triage call errors outright, or the
agent's deadline lapses with no heartbeat - results in a `stand_aside` or `failure`,
never a silent fall-through to the expensive tier. This prevents a saturated triage
tier from cascading load onto the primary tier while still giving a slow-queuing agent
the chance to be heard.

### Dashboard Visibility

The span graph on the discussions page shows which GPU tier each agent used per
discussion, colored per provider, alongside each signal. An agent that never reached
either tier (filtered before triage) shows no GPU badge at all.

---

## Beyond Infrastructure: Domain-Agnostic Crews

Kubemoot is the orchestration substrate; the domain comes from the crew, not from Kubemoot. The same Tooler-and-Analyst machinery serves infrastructure ops, Kafka operations, research, security, and more: each crew declares its own agents and points them at the operator. The cross-domain examples and the portability rationale live with the crew documentation: see [Crew CRD: Domain-Agnostic by Design](../reference/crew/#domain-agnostic-by-design).

