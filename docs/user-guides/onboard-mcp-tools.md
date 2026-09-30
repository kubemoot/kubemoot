---
title: "Onboard MCP Tools"
weight: 40
description: "Give a crew new capabilities by onboarding MCP servers."
---

A crew gains new abilities by gaining new **tools**, and tools come from MCP servers.
You can declare them directly, or let Kubemoot's **autonomic onboarding** acquire them
when the crew hits a capability gap. This guide covers both.

## Declare a tool server directly

When you already know the MCP server you want, declare an `MCPServer` and let an
`MCPGateway` discover it. The operator handles the deployment, health probes, and - for
stdio servers - the auto-injected `mcp-bridge` sidecar.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: kubernetes-mcp
  labels:
    app.kubernetes.io/part-of: my-crew
spec:
  image: mcp/kubernetes:latest
  transport: stdio
  command: ["node", "dist/index.js"]
```

The gateway selects servers by label, aggregates their tools, and exposes them to
agents. Then scope the tools onto a Tooler with `spec.enabledTools`, keeping the set
small (~10-15 tools) for reliable tool-calling. See
[MCP Tools](../../concepts/mcp-tools/) for the chain and the
[MCPServer](../../reference/mcpserver-guide/) /
[MCPGateway](../../reference/mcpgateway-guide/) references for the specs.

## Let the crew propose a tool for itself

When a discussion finds that no existing agent can answer, the coordinator signals a
capability gap. An agent running in onboarding mode reacts to that signal:

1. **Detects the gap** from the coordinator's signal.
2. **Proposes an MCP server** from its model's own knowledge (the call carries no tools).
3. **Replies with a proposed `MCPServer` manifest** when the user consents. You review and
   apply it.

Everything after the proposal is manual today: you apply the manifest, create the Tooler `Agent` that uses the new
server's tools, and optionally a `RAGSource` for its documentation. The operator does
not create the Tooler for you. Because every step produces ordinary Kubernetes
resources, you can inspect, version, and remove them like anything else.

Onboarding mode is an environment variable (`KUBEMOOT_ONBOARDING_MODE=true`) on an
`Agent` you declare. The operator chart's `internalAgents` values deploy the
`kubernetes-mcp` and `fetch-mcp` servers (`github-mcp` is opt-in) and a quality policy;
an `MCPCatalog` is rendered only when `onboardingAgent.enabled` is set. The onboarding and
RTFM agents are off by default and the chart creates no `Agent` for them. For the flow,
configuration, and troubleshooting, see the
[Onboarding Guide](../../operating/onboarding-guide/).

## Which path to use

- **Declare directly** when you know the tool and want it deterministically present -
  the normal way to equip a crew you're authoring.
- **Autonomic onboarding** when you want the crew to grow to meet questions it wasn't
  pre-built for.
