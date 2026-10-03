---
title: "Onboarding Guide"
description: "How a Kubemoot crew proposes an MCP server when no agent can answer: the capability gap signal, the onboarding agent, consent, and the steps that stay manual."
weight: 1
---

## Overview

When no agent in a crew can answer a question, the coordinator signals a capability gap. An agent running in onboarding mode reacts to that signal: it proposes an MCP server that could fill the gap. On the user's consent it replies with a proposed `MCPServer` manifest in the discussion thread. Applying that manifest, and creating the Tooler `Agent` that uses it, are manual steps today.

This page describes what ships today, how to enable it, and what is not automated yet. For the consensus signals that produce a gap, see [Agentic Consensus](../../architecture/agentic-consensus/).

## What ships today

| Piece | Where it lives | State |
|-------|----------------|-------|
| Gap signal (`gap_detected`) | The coordinator's discussion logic in the agent runtime | Ships |
| Consent-driven onboarding subscriber (`OnboardingSubscriber`) | Agent runtime, active when `KUBEMOOT_ONBOARDING_MODE=true` | Ships; proposes, does not create |
| Documentation subscriber (`RtfmSubscriber`) | Agent runtime, active when `KUBEMOOT_RTFM_MODE=true` | Ships; listens for an onboarding-deployed event |
| Internal MCP servers and quality policy | Operator Helm chart (`internalAgents`) | Ships, opt-in |
| `MCPCatalog` | Operator Helm chart | Rendered only when `onboardingAgent.enabled` is set |
| Onboarding, RTFM, and quality-evaluator `Agent` resources | Not deployed by the chart | You declare them yourself |
| Automatic Tooler creation from an onboarded server | Not implemented | You create the `Agent` yourself |

The operator chart's `internalAgents` values deploy the `kubernetes-mcp` and `fetch-mcp` servers (`github-mcp` is opt-in) and an `MCPQualityPolicy`. An `MCPCatalog` is rendered only when `onboardingAgent.enabled` is set. Both `onboardingAgent` and `rtfmAgent` default to off, and neither creates an `Agent`. Check the chart's `values.yaml` for the current default of `internalAgents.enabled`.

## The flow

### Step 1: Gap signal

When the discussion settles with no Tooler agreeing, and there are concerns, stand-asides, or low triage confidence on record, the coordinator publishes a `gap_detected` message. An agent in onboarding mode listens for it.

### Step 2: Proposal

The onboarding agent asks its model to propose an MCP server and publishes the result as a `proposal` message to the discussion thread. The call carries no tools, so the proposal is the model's own text drawn from what it knows, not a live registry search. For example:

> Found **kafka-mcp**. Reply `onboard kafka` to deploy.

Treat the proposal as a suggestion to verify. Proposals do not expire and are not stored between messages.

### Step 3: Consent

The consent parser accepts these forms:

```
onboard <domain>
onboard <domain> <doc-url-1> <doc-url-2>
yes, onboard <domain> <doc-url-1>
```

Documentation URLs in the reply are passed to the model as a hint for the proposed manifest. Nothing stores them today.

### Step 4: Proposed MCPServer manifest

On consent the onboarding agent replies in the thread with a proposed `MCPServer` manifest. The reply is model text, so no resource exists until you apply it. A proposal is asked to look like this:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: kafka-mcp
  labels:
    kubemoot.ai/onboarded: "true"
    kubemoot.ai/domain: kafka
  annotations:
    kubemoot.ai/doc-urls: "https://kafka.apache.org/documentation"
spec:
  image: example/kafka-mcp:latest
  transport: stdio
  replicas: 1
```

Review the image and settings before you apply it.

### Step 5: Agent and documentation (manual)

From here you wire the new server into the crew:

1. Apply the reviewed `MCPServer` manifest.
2. Wait for the `MCPServer` to report `Ready` and its tools to appear in `status.tools`.
3. Create a Tooler `Agent` (`discussRole: tooler`) whose `enabledTools` lists the tools that fit its domain. Keep the set small; see [MCP Tools](../../concepts/mcp-tools/).
4. Optionally create a `RAGSource` for the server's documentation so an Analyst can reason over it. See the [RAGSource guide](../../reference/ragsource-guide/).

## Enabling onboarding mode

Onboarding mode is a property of an `Agent`, set through environment variables in the agent's `spec.env`. The operator does not translate annotations into these variables.

| Env var | Effect |
|---------|--------|
| `KUBEMOOT_ONBOARDING_MODE=true` | Activates `OnboardingSubscriber` |
| `KUBEMOOT_RTFM_MODE=true` | Activates `RtfmSubscriber` |
| `KUBEMOOT_DISCUSS_PRIORITY=low` | Observer priority |

Leave `discussRole` unset (the runtime default is `generic`) so the onboarding agent does not also act as a Tooler.

The operator chart's `internalAgents` values control the shared MCP servers, catalog, and quality policy. The `onboardingAgent` and `rtfmAgent` toggles default to off.

## Troubleshooting

### The onboarding agent does not react to gaps

1. Confirm an `Agent` with `KUBEMOOT_ONBOARDING_MODE` in its `spec.env` exists: `kubectl get agents -A -o yaml | grep ONBOARDING_MODE`.
2. Check that the agent's logs show a NATS connection.
3. Confirm `KUBEMOOT_ONBOARDING_MODE=true` is set on the agent pod.

### Consent is not recognized

The consent parser accepts `onboard <domain> [url ...]` or `yes, onboard <domain>`; the domain is passed to the model as is.

### The onboarded server has no tools

1. Confirm the `MCPServer` pod is running.
2. Confirm the gateway registered the server: `kubectl get mcpgw -o yaml` and check `status.mcpServers`.
3. Check the bridge sidecar logs.
