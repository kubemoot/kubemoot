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

## Let the crew onboard a tool for itself

Kubemoot can close a capability gap on its own, with user consent. When a discussion
finds that no existing agent can answer, Toolers stand aside and a gap is detected,
and the onboarding system:

1. **Detects the gap** from the discussion's signals.
2. **Finds an appropriate MCP server** by searching registries.
3. **Evaluates its quality** against an `MCPQualityPolicy` before trusting it.
4. **Deploys the server** as an `MCPServer` on consent.
5. **Indexes its documentation** into a `RAGSource` so the new Tooler has context.
6. **Creates a Tooler `Agent`** wired to the tool and its docs.

The result is a fully functional Tooler the crew didn't have a minute earlier, and
because every step produces ordinary Kubernetes resources, you can inspect, version, and
remove them like anything else.

Onboarding is opt-in and configured in the operator's `internalAgents` values, which
deploy the onboarding agent, an RTFM (documentation-indexing) agent, and a quality
evaluator. For the architecture, configuration, and troubleshooting, see the
[Autonomic Onboarding Guide](../../operating/onboarding-guide/).

## Which path to use

- **Declare directly** when you know the tool and want it deterministically present -
  the normal way to equip a crew you're authoring.
- **Autonomic onboarding** when you want the crew to grow to meet questions it wasn't
  pre-built for.
