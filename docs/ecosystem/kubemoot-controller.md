---
title: "Kubemoot - the Controller"
weight: 10
description: "The Kubernetes operator and runtime that wires crews together."
---

Kubemoot is **the controller** - the Kubernetes operator, agent runtime, and MCP
components that turn declared crews into running, deliberating systems. It is the
project everything else in this ecosystem is built on. A crew, an authoring tool, or a
CLI is only useful because the controller reconciles the resources they produce.

## What it includes

- **The operator** (Go, controller-runtime) - reconciles the Kubemoot CRDs (`Crew`,
  `Agent`, `Model`, `ModelProvider`, `MCPServer`, `MCPGateway`, `RAGSource`,
  `PromptModule`, and the cluster-scoped `KubemootConfig`), and owns lifecycle:
  scheduling, finalizers, namespace and resource cleanup.
- **The agent runtime** (Java/Quarkus + LangChain4j) - the process each agent runs as,
  which deliberates over the message bus, calls tools through the gateway, retrieves
  RAG knowledge, and emits consensus signals.
- **The MCP components** - the gateway and the `mcp-bridge` sidecar that connect agents
  to tools.

## What it does

The controller is the substrate; it carries **no domain knowledge of its own**. It
provides the machinery - reconciliation, GPU-aware scheduling, the tool gateway,
consensus orchestration, working memory, and observability - and the domain comes
entirely from the crews that run on it. That separation is what lets one controller
serve infrastructure ops, Kafka operations, research, or security work without change.

## Where it fits

The rest of this ecosystem sits on top:

- **[Crews](../crews/)** are what you run on the controller.
- **[Pilot](../pilot/)** is one reference crew that demonstrates it.
- **[kmctl](../kmctl/)** is the tool for authoring and operating crews from the
  terminal; [CrewForge](../crewforge/) is the VS Code extension for developing crews, deploying
  them, and talking to them from the editor.

For the operator's design and the consensus model it implements, see
[Concepts](../../concepts/consensus-model/) and the
[Architecture](../../architecture/) section.
