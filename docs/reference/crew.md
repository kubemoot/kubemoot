---
title: "Crew CRD"
weight: 2
---

## Overview

A `Crew` groups a set of `Agent`s (joined by the `kubemoot.ai/crew` label) into one collaborating team, and configures crew-wide concerns: the discussion gateway and the crew's **working memory**. Agents are the workers; the Crew is the team-level context they share.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Crew
metadata:
  name: homelab-pilot
  namespace: crew-homelab-pilot
spec:
  description: "Homelab infrastructure crew"
  discussion:
    enabled: true
  memory:
    enabled: true
    maxFacts: 5000
    ttlDays: 365
    injectLimit: 8
    verifyOnAdd: true
```

The namespace separates names. The same crew name can run in many namespaces side by
side: the discussion, request, artifact, and memory subjects and keys a crew uses carry its
namespace and crew name after a fixed prefix (some operator subjects carry no namespace), so two crews named
`homelab-pilot` in two different namespaces do not collide. This is naming separation,
not a security boundary: NATS has no authentication in this release, and a client that
can reach it can read any subject. See
[Security model and known limitations](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md#security-model-and-known-limitations).

## Domain-Agnostic by Design

Kubemoot is the orchestration substrate; the **domain** comes from the crew, not from Kubemoot. A crew declares its own Toolers (live data via MCP) and Analysts (reasoning via RAG) in its Helm chart, points them at the operator, and gets a panel of experts with consensus collaboration. The same machinery serves any domain:

| Application | Toolers (MCP Tools) | Analysts (RAG Knowledge) |
|---|---|---|
| **Infrastructure ops** (homelab-pilot) | kubectl, Helm, Proxmox, Prometheus, k8sgpt | Kubernetes/Proxmox/Talos docs |
| **Kafka operations** | Kafka MCP tools (topics, consumer groups, configs) | Kafka / Confluent docs |
| **Oncology research** | (discussion-only) | PubMed papers, trial data, protocols |
| **Security operations** | SIEM / scanner tools | CVE databases, compliance frameworks, playbooks |

Because the domain is the crew's, a crew must be **portable** across clusters - which is exactly why nothing cluster-specific is baked into prompts and why working memory (below) is learned at runtime, per cluster.

## Spec Fields

| Field | Type | Description |
|-------|------|-------------|
| `description` | string | Human-readable purpose |
| `discussion` | object | Discussion gateway config (`enabled`, `resources`) - the HTTP entry point for external clients |
| `memory` | object | Crew working-memory policy - see below |

## Working Memory

The crew learns facts about *its* environment at runtime and recalls them later, so it stops re-discovering what it already figured out. This is what lets a crew be **portable**: nothing about a specific cluster (node names, label schemes, topology) is baked into agent prompts - the crew discovers those at runtime and remembers them per-cluster.

### The discover → remember → recall loop

1. **Discover, never assume.** When a query names a logical resource (e.g. a GPU "gpu-a"), agents do NOT assume how it's labeled. They run a discovery query (e.g. the bare metric to read its real labels), map the logical name to a real label value, then query. (See the `tool-using-specialist-discipline` PromptModule.)
2. **Remember.** When an agent resolves a durable fact, it emits a directive in its response:
   ```
   REMEMBER: <topic> | <key> | <value>
   example: REMEMBER: gpu-topology | gpu-a | DCGM label exported_namespace="ollama-a", RTX 5090
   ```
   The runtime persists it to crew memory and strips the line from the user-facing answer (same structured-output convention as `TOOL_GAP:`).
3. **Recall (auto-injection).** On every query, the runtime injects the crew's *relevant* facts into the agent's system context under `## Crew Working Memory`. The agent starts already knowing - it does not have to choose to look it up. Discovery becomes a one-time cost **per cluster**, amortized across all later queries.

### Storage vs injection

Storage is cheap (NATS KV, facts are a few hundred bytes); injection into LLM context is expensive (tokens + tool-calling degradation). They are configured separately:

- **Storage** (`maxFacts`) is generous.
- **Injection** (`injectLimit`) is small and **relevance-filtered**: facts whose `topic`/`key`/`value` share a token with the query rank first, then by recency, capped at `injectLimit`. A GPU query surfaces GPU facts, not the whole memory.

### Dedup & conflict (`verifyOnAdd`)

When `verifyOnAdd` is true, a new fact is vetted against the existing same-key fact before storing:

- identical value → **duplicate** → touch only (refresh usage), no rewrite
- different value → **conflict** → the fresh discovery **supersedes** (the environment is the source of truth); the prior revision is kept in NATS KV history; the supersession is logged

Cross-key semantic duplication/conflict is handled by the LLM itself - it sees the relevant memory via injection and is directed (ADL) not to re-`REMEMBER` known facts and to emit a correcting `REMEMBER` when it finds a contradiction. No extra inference.

### Garbage collection

Facts are kept alive by use and aged out when abandoned:

| Mechanism | Criterion | Effect |
|-----------|-----------|--------|
| Same-key overwrite | re-`REMEMBER` of an existing `topic`/`key` | replaces (dedup/supersede) |
| LRU cap | crew exceeds `maxFacts` on write | evict least-recently-used |
| Touch-on-read | a recalled fact's `usedAt` older than 6h | refresh `usedAt` (keep-alive) |
| TTL | fact unused/unrefreshed past `ttlDays` | pruned on read (per-crew) |

### Lifecycle

- **Crew update** keeps memory.
- **Crew delete** purges that crew's facts (operator finalizer removes `<crew>.*` keys).

### `spec.memory` fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `true` | Turn working memory on/off (recall + persist) |
| `maxFacts` | int32 | `5000` | Per-crew storage cap; LRU-evicted above this |
| `ttlDays` | int32 | `365` | Age backstop; used facts are touched and survive |
| `injectLimit` | int32 | `8` | Max facts injected into context per query (small - context budget, not storage) |
| `verifyOnAdd` | bool | `true` | Vet new facts for duplicates/conflicts on write |

## Namespace lifecycle

Deleting a Crew leaves its namespace alone by default. A Crew that carries the annotation
`kubemoot.ai/manage-namespace: "true"` asks the operator to delete the namespace with it,
and the operator does so only when the namespace itself opts in with the label
`kubemoot.ai/managed-namespace: "true"`. Someone with rights on the `Namespace` object sets
that label; a namespaced Crew author cannot, and the operator never writes it. The operator
never deletes `kube-system`, `default`, `kube-public`, `kube-node-lease`, or its own
namespace, whatever the annotation and labels say.

## Storage

Working memory lives in the NATS KV bucket `kubemoot_crew_memory` (provisioned by the operator's nats-streams-job). Keys are namespace-and-crew-scoped: `<namespace>.<crew>.<topic>.<key>`; values carry `value`, `learnedBy`, `learnedAt`, `usedAt`. The agent-runtime reads/writes it directly (`CrewMemoryClient`), native-safe via `readTree`, degrading to a no-op when NATS is unavailable.

## Related

- [Agent CRD](../reference/agent.md) - the workers a crew groups
- [Agentic Consensus](../architecture/agentic-consensus.md) - how the crew discusses
