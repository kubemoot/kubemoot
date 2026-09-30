---
title: "Onboarding Guide"
weight: 1
---

## Overview

When no agent in a crew can answer a question, the coordinator signals a capability gap. An agent running in onboarding mode reacts to that signal: it proposes an MCP server that could fill the gap and, with the user's consent, creates an `MCPServer` resource for it.

This page describes what ships today, how to enable it, and what is not automated yet. For the consensus signals that produce a gap, see [Agentic Consensus](../../architecture/agentic-consensus/).

## What ships today

| Piece | Where it lives | State |
|-------|----------------|-------|
| Gap signal | The coordinator's discussion logic in the agent runtime | Ships |
| Consent-driven onboarding subscriber (`OnboardingSubscriber`) | Agent runtime, active when `KUBEMOOT_ONBOARDING_MODE=true` | Ships |
| Documentation subscriber (`RtfmSubscriber`) | Agent runtime, active when `KUBEMOOT_RTFM_MODE=true` | Ships; listens for an onboarding-deployed event |
| Internal MCP servers, MCP catalog, quality policy | Operator Helm chart (`internalAgents`) | Ships |
| Internal onboarding, RTFM, and quality-evaluator `Agent` resources | Not deployed by the chart | You declare them yourself |
| Automatic Tooler creation from an onboarded server | Not implemented | You create the `Agent` yourself |

The operator chart deploys the internal MCP servers and an `MCPCatalog` and `MCPQualityPolicy`. It does not create the onboarding `Agent` resources, and the operator does not create a Tooler agent when an onboarded `MCPServer` becomes ready. Everything after the `MCPServer` is created is a manual step today.

## The flow

### Step 1: Gap signal

The coordinator's reasoning over the discussion decides that no agent can help and publishes a gap signal. An agent in onboarding mode listens for it.

### Step 2: Proposal

The onboarding agent uses its MCP tools to look for a candidate server (registries or GitHub) and publishes a `proposal` message to the discussion thread:

> Found **kafka-mcp**. Reply `onboard kafka` to deploy.

### Step 3: Consent

The user replies with one of these forms:

```
onboard <domain>
onboard <domain> <doc-url-1> <doc-url-2>
yes, onboard <domain> <doc-url-1>
```

Documentation URLs in the reply are stored on the `MCPServer` as the `kubemoot.ai/doc-urls` annotation.

### Step 4: MCPServer creation

On consent, the onboarding agent creates an `MCPServer` through its Kubernetes MCP tool:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: kafka-mcp
  labels:
    kubemoot.ai/onboarded: "true"
    kubemoot.ai/domain: kafka
  annotations:
    kubemoot.ai/discuss-channel: kubernetes
    kubemoot.ai/doc-urls: "https://kafka.apache.org/documentation"
spec:
  image: example/kafka-mcp:latest
  transport: stdio
  replicas: 1
```

### Step 5: Agent and documentation (manual today)

From here you wire the new server into the crew:

1. Wait for the `MCPServer` to report `Ready` and its tools to appear in `status.tools`.
2. Create a Tooler `Agent` (`discussRole: tooler`) whose `enabledTools` lists the tools that fit its domain. Keep the set small; see [MCP Tools](../../concepts/mcp-tools/).
3. Optionally create a `RAGSource` for the server's documentation so an Analyst can reason over it. See the [RAGSource guide](../../reference/ragsource-guide/).

## Enabling onboarding mode

Onboarding mode is a property of an `Agent`. Declare an agent that carries the onboarding annotations and a Kubernetes MCP tool with permission to create Kubemoot resources:

| Annotation | Env var | Effect |
|------------|---------|--------|
| `kubemoot.ai/onboarding-mode: "true"` | `KUBEMOOT_ONBOARDING_MODE=true` | Activates `OnboardingSubscriber` |
| `kubemoot.ai/rtfm-mode: "true"` | `KUBEMOOT_RTFM_MODE=true` | Activates `RtfmSubscriber` |
| `kubemoot.ai/discuss-priority: low` | `KUBEMOOT_DISCUSS_PRIORITY=low` | Observer priority |

The operator chart's `internalAgents` values control the shared MCP servers, catalog, and quality policy. The `onboardingAgent` and `rtfmAgent` toggles default to off.

The Kubernetes MCP server that creates resources needs permission to create `mcpservers` and `ragsources`. Grant it a narrow `Role` in the crew's namespace rather than a cluster-wide grant.

## Troubleshooting

### The onboarding agent does not react to gaps

1. Confirm an `Agent` with the onboarding annotation exists: `kubectl get agents -A -o yaml | grep onboarding-mode`.
2. Check that the agent's logs show a NATS connection.
3. Confirm `KUBEMOOT_ONBOARDING_MODE=true` is set on the agent pod.

### Consent is not recognized

The consent parser expects `onboard <domain>` or `yes, onboard <domain>`. The domain must match a pending proposal.

### The onboarded server has no tools

1. Confirm the `MCPServer` pod is running.
2. Confirm the gateway registered the server: `kubectl get mcpgw -o yaml` and check `status.mcpServers`.
3. Check the bridge sidecar logs.
