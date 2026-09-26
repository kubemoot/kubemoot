---
title: "2. NATS Consensus Signals Over A2A Protocol"
weight: 2
---

Date: 2026-03-16

## Status

Accepted

## Context

Google's [Agent2Agent (A2A)](https://github.com/google/A2A) protocol is an open standard for agent interoperability: Agent Cards (JSON skill descriptors), bilateral task negotiation (request/response delegation), and a common wire format over JSON-RPC + HTTP/SSE.

Kubemoot's multi-agent discussion system communicates over NATS JetStream using typed JSON messages on the subject pattern `kubemoot.discuss.<crew>.<channel>.<threadId>`. Messages carry a consensus-signal vocabulary (`triaging`, `evaluating`, `agree`, `concern`, `stand_aside`, `block`, `advisory`, `proposal`) inspired by sociocracy 3.0 and human governance protocols. The coordinator convenes a subcommittee via the crew's resume model, then broadcasts to it; a facilitator synthesizes based on the spectrum of signals.

The two models differ in shape. A2A is bilateral and request/response: one agent delegates a task to another, the other accepts or rejects, work proceeds, a result returns. Kubemoot is a facilitated consensus table: the coordinator broadcasts to everyone; agents express a spectrum of positions (not binary accept/reject); blocks halt proceedings; an advisory enriches the whole table. Skill discovery - A2A's primary value - is already covered by Agent CRDs and the operator's complete view of the cluster.

## Decision

We will continue using NATS JetStream with custom consensus signals for all intra-cluster agent communication. We will not adopt A2A as an internal protocol. If cross-cluster or external agent federation becomes a requirement, we will introduce an **A2A Gateway** - a protocol bridge at the cluster boundary - following the same pattern as the MCP Gateway and the mcp-bridge sidecar.

## Consequences

- The consensus signal vocabulary is preserved without translation loss; blocks, stand-asides, and advisories have no A2A equivalent that survives a mapping.
- NATS JetStream advantages (persistent streams with replay, wildcard subscriptions, broadcast, backpressure) are fully exploited; HTTP/SSE bilateral connections would not fit the broadcast-and-collect pattern.
- A single source of truth for agent capabilities (the Kubernetes Agent CRD); no parallel Agent-Card registry to keep in sync.
- External agents cannot participate in Kubemoot discussions today; this is acceptable because Kubemoot is currently a single-cluster operator.
- Future A2A interop is additive: an A2A Gateway can expose Kubemoot agents as Agent Cards and translate inbound tasks into NATS `thread_start` broadcasts without touching internal protocols.
- This decision can be revisited as A2A matures or if cross-vendor agent collaboration becomes a routine requirement.

## References

- [Google A2A Protocol](https://github.com/google/A2A)
- [Consensus Decision Making - Seeds for Change](https://www.seedsforchange.org.uk/shortconsensus) - inspiration for the signal vocabulary
- [Voting or Consensus? Decision-Making in Multi-Agent Debate (ACL 2025)](https://arxiv.org/abs/2502.19130) - consensus improves knowledge tasks by 2.8%, validating the approach for tool-backed agents
- [agentic-consensus.md](../architecture/agentic-consensus.md) - discussion protocol design
- [0001-mcp-context-forge-evaluation.md](0001-mcp-context-forge-evaluation.md) - prior ADR establishing the gateway-bridge pattern
