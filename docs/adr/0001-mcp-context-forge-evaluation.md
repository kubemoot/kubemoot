---
title: "1. MCP Context Forge Evaluation"
weight: 1
---

Date: 2025-01-15

## Status

Accepted

## Context

IBM's [mcp-context-forge](https://github.com/IBM/mcp-context-forge) is an open-source MCP (Model Context Protocol) gateway and registry. It provides protocol translation (REST/gRPC/stdio/SSE/HTTP), multi-tenant RBAC, SSO integration, OpenTelemetry, Redis-backed caching, plugin hooks, and federation. Implementation is Python 3.11+ on FastAPI, with PostgreSQL/Redis backends.

As Kubemoot designs its MCPServer and MCPGateway CRDs, the question is whether to adopt mcp-context-forge as a core dependency, fork and port it, or build native MCP capabilities from scratch.

Kubemoot is a Kubernetes operator: declarative CRDs, etcd-backed state, controller-runtime in Go, GitOps-native. The audience is platform engineers running production clusters who want AI infrastructure managed through Kubernetes APIs. mcp-context-forge is a gateway service: imperative HTTP API, application-database state, Python runtime, oriented toward application developers who deploy an MCP gateway and call it.

The mcp-context-forge project explicitly states: *"MCP Gateway is not a standalone product - it is an open source component with **NO OFFICIAL SUPPORT** from IBM or its affiliates."*

## Decision

We will not adopt mcp-context-forge as a core dependency. Kubemoot will implement MCP capabilities natively in Go using kubebuilder/controller-runtime patterns, drawing on protocol-translation and plugin-hook ideas from mcp-context-forge as references.

## Consequences

- Architectural consistency is preserved: every Kubemoot control-plane component is a Go operator with declarative CRDs and no Python runtime in the control plane.
- Full control over MCP roadmap; no dependency on an unsupported upstream.
- More implementation work: protocol translation, observability integration, and plugin extensibility must be built rather than adopted.
- Users who want mcp-context-forge's specific features can still deploy it as a workload, but Kubemoot does not optimize for or test against this configuration.
- This decision may be revisited if mcp-context-forge changes governance (official IBM support, donation to a foundation, Go rewrite).

## References

- [mcp-context-forge architecture](https://ibm.github.io/mcp-context-forge/architecture/)
- Other MCP gateway projects evaluated for inspiration: [Docker MCP Gateway](https://github.com/docker/mcp-gateway) (Go, container isolation), [Microsoft MCP Gateway](https://github.com/microsoft/mcp-gateway) (.NET, Kubernetes session routing), [Lasso Security](https://github.com/lasso-security/lasso) (security-first guardrails)
