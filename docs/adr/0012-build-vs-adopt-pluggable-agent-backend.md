---
title: "12. Pluggable Agent Backend"
weight: 12
---

Date: 2026-06-24

## Status

Accepted

## Context

The consensus moot - the coordinator convening a subcommittee, agents deliberating by
signal, the coordinator synthesizing - is Kubemoot's differentiator. The thing that
actually runs an individual agent's turn (calling a model, calling a tool, producing a
signal) is a separate concern, and several capable agent runtimes already exist that do
part of that well: robust tool-calling loops, shared file storage, memory conventions,
code-as-action patterns. Baking the moot logic to any one of those runtimes would trade
away the moot's runtime independence for whatever that runtime is good at.

The category worth keeping distinct is between an **open standard** (the Model Context
Protocol, governed independently of any vendor), an **open convention** (a design idiom
like a memory-file layout or a code-as-action pattern, which can be reimplemented on a
different substrate without reinventing anything), and a **specific product** (a
particular agent runtime's binary or SDK, which carries its own licensing terms and is
not something the operator should require).

## Decision

The consensus moot talks to an agent through a thin, backend-agnostic contract: in
comes the moot question, the agent's role in the discussion, and the set of available
tools; out comes a consensus signal, a rationale, and supporting evidence. Behind that
contract the operator can place any of the following without any change to the moot
logic:

- The native GraalVM thin agent - the default, pure OSS, minimal VRAM and startup cost.
- An external agent process the user supplies and runs under their own license.
- A raw local model reached directly through the Ollama provider.

The moot is backend-agnostic: it neither knows nor cares which backend sits behind the
contract for a given agent.

## Consequences

- The core builds, runs, and demos with zero dependency on any proprietary agent
  product - the native thin agent plus local models via Ollama are enough on their own.
  An external backend is always optional, never bundled.
- Consistent with [ADR 0001](0001-mcp-context-forge-evaluation.md)'s reasoning applied
  to a different layer: build natively against open standards, adopt the standard
  directly (MCP), and keep any specific product swappable behind a contract rather than
  load-bearing.
- Opens a set of follow-on work items, tracked separately:
  - A Skill CRD convention: skill bundles stored in the NATS Object Store artifact
    bucket, registered as Kubernetes CRs, addressable by name.
  - Code-as-action: sandboxed code execution so agents are not limited to predefined
    read-only tools.
  - Crew memory over NATS: a memory-file convention backed by NATS KV.
  - The formal backend-agnostic moot contract itself: a Go interface plus an adapter
    registry in the operator.

## References

- [0001-mcp-context-forge-evaluation.md](0001-mcp-context-forge-evaluation.md) - the same build-vs-adopt reasoning applied to the MCP gateway
- [0002-nats-consensus-over-a2a.md](0002-nats-consensus-over-a2a.md) - the multi-agent consensus moot over NATS this contract sits behind
- [Model Context Protocol](https://modelcontextprotocol.io/) - the open standard adopted directly
- [agentic-consensus.md](../architecture/agentic-consensus.md) - discussion protocol and signal vocabulary design
