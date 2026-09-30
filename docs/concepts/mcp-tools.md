---
title: "MCP Tools"
weight: 50
description: "How agents gain capabilities through MCP servers and the gateway."
---

An agent's knowledge comes from its model and its RAG sources; its *abilities* - to
read a cluster, query a database, search the web - come from **tools**. Kubemoot
exposes tools through the [Model Context Protocol (MCP)](https://modelcontextprotocol.io),
so any MCP server becomes a capability a crew can use.

## Servers, gateway, agents

Three resources form the tool chain, and agents never connect to a tool server
directly:

```
Agent → MCPGateway → MCPServer
```

- An **MCPServer** deploys one MCP tool server (a container image) and manages its
  lifecycle: the deployment, health probes, and - for the many servers that only
  speak stdio - an auto-injected `mcp-bridge` sidecar that bridges HTTP/SSE to the
  server's stdin/stdout.
- An **MCPGateway** is the hub agents talk to. It discovers MCPServers by label
  selector, performs the MCP handshake with each, aggregates all their tools, and
  routes an agent's tool call to the right server.
- An **Agent** reaches the gateway through its runtime and sees the union of tools the
  gateway exposes, narrowed by the agent's own `enabledTools` / `disabledTools`.

This indirection means you can add or remove a tool server, or scale the gateway,
without touching agent specs - the gateway rediscovers servers and re-aggregates tools.

## Keep an agent's tool set small

Tool-calling reliability falls off as the tool count grows. A capable model handles
roughly 10-15 tools well; beyond that, selection degrades. So tools are scoped per
agent rather than handed wholesale to every agent: a Tooler gets the handful that
fit its domain, set with `spec.enabledTools`. When a useful server carries far more
tools than one agent should hold, split it across more than one Tooler.

## Where tools come from

You declare MCPServers directly. When no existing agent can answer a question, the
coordinator signals a capability gap, and an agent running in onboarding mode can
propose an MCP server and create it with user consent. Wiring the new server to a
Tooler agent is a manual step today. See
[Onboard MCP Tools](../../user-guides/onboard-mcp-tools/) for the workflow and the
[MCPServer](../../reference/mcpserver-guide/) and
[MCPGateway](../../reference/mcpgateway-guide/) references for the specs.
