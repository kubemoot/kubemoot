---
title: "3. Crew Label as Multi-Tenant Grouping Primitive"
weight: 3
---

Date: 2026-03-16

## Status

Accepted, amended

Namespaces became the naming boundary for crews: NATS subjects and keys carry the namespace before the crew name. Namespaces separate names; they are not a security boundary today (see [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md)).

## Context

Kubemoot started as the orchestration layer for one application (homelab-pilot). As the operator matures, multiple applications will each need their own set of Agents, MCPServers, MCPGateways, RAGSources, PromptModules, and CrewSchedulingPolicies - all running in the same cluster, managed by the same operator.

The question is how to associate a set of Kubemoot CRs with the application that owns them, scope inter-agent communication to that group, and still let shared infrastructure (kubernetes-mcp, pgvector, Ollama) serve multiple groups without duplication.

The natural Kubernetes primitives for this are **namespaces** and **labels**.

Namespaces model trust boundaries: distinct teams sharing a cluster with separate RBAC, NetworkPolicies, and ServiceAccounts. Per-crew namespaces would require duplicating shared MCPServers (kubernetes-mcp, filesystem-mcp), engineering cross-namespace DNS for pgvector and Ollama, and bridging NATS subscriptions (NATS subjects are cluster-scoped: there is no namespace concept in the NATS subject hierarchy). The MCPGateway's `mcpServerSelector` (a `metav1.LabelSelector`) operates on labels in a single namespace by default; cross-namespace routing would require extra machinery.

Labels are the Kubernetes-native grouping primitive: queryable via selectors, additive (unlabeled resources keep working), and already the integration point for MCPGateway. The term "crew" was chosen because it evokes coordinated teamwork and aligns with broader AI-agent vocabulary (CrewAI, crew-based orchestration) without coupling Kubemoot to any specific framework.

## Decision

We will apply `kubemoot.ai/crew: <crew-name>` as a standard Kubernetes label to Agent, CrewSchedulingPolicy, MCPServer, MCPGateway, RAGSource, and PromptModule CRs. All resources for all crews live in a shared namespace (e.g., `kubemoot`). We will scope discussion broadcasts by prefixing the NATS subject with the crew (`kubemoot.discuss.<crew>.<channel>.<threadId>`, later widened to `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>` when namespaces became the isolation boundary). We will not use one namespace per crew.

## Consequences

- MCPGateway scopes its tool discovery via `mcpServerSelector: matchLabels: { kubemoot.ai/crew: <name> }`; no API change required, the machinery already exists.
- Shared infrastructure (kubernetes-mcp, pgvector, Ollama) lives unlabeled and is accessible to all crews; no duplication or cross-namespace DNS.
- NATS subjects are scoped via subject prefix, not network bridging; crews are acoustically isolated by subject hierarchy.
- Single namespace means NATS credentials, imagePullSecrets, and DB secrets are managed once for the whole platform.
- Isolation is softer than namespaces: no per-crew RBAC or NetworkPolicy enforcement out of the box. If trust isolation between crews becomes a requirement (e.g., a multi-tenant SaaS scenario), additional controls will need to be built.
- Migration is additive: existing resources continue working without the label and are treated as crew-less / shared.
- Dashboard adds a crew filter populated from distinct `kubemoot.ai/crew` values; this replaces the less meaningful namespace dropdown for crew views.

## References

- [Kubernetes Labels and Selectors](https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/)
- [Kubernetes Namespaces](https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/) - guidance on when namespaces are and are not the right tool
- [0001-mcp-context-forge-evaluation.md](0001-mcp-context-forge-evaluation.md) - gateway bridge pattern (MCPGateway `mcpServerSelector` established here)
- [0002-nats-consensus-over-a2a.md](0002-nats-consensus-over-a2a.md) - NATS subject hierarchy design
- [agentic-consensus.md](../architecture/agentic-consensus.md) - discussion protocol and subject structure
