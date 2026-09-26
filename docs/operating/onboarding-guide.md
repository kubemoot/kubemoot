---
title: "Autonomic Onboarding Guide"
weight: 1
---

## Overview

Kubemoot's autonomic onboarding system detects capability gaps and deploys new Tooler agents automatically, with user consent. When no existing agent can answer a question, the system finds an appropriate MCP server, evaluates its quality, deploys it, indexes documentation, and creates a fully functional Tooler agent.

For the design philosophy and consensus signal model, see [agentic-consensus.md](../architecture/agentic-consensus.md). This document covers the operational details: how to enable it, configure it, and troubleshoot it.

## Architecture

```
Gap Detection          Consent & Deploy          Documentation           Auto-Agent
┌──────────────┐     ┌──────────────────┐     ┌───────────────┐     ┌──────────────┐
│ OnboardingSub│     │ MCPServer CR     │     │ RAGSource CR  │     │ Agent CR     │
│ (Java agent) │────►│ onboarded: true  │────►│ domain-ref    │────►│ capabilities │
│              │     │ domain: kafka    │     │ pgvector      │     │ + tools      │
│ GapDetector  │     │                  │     │               │     │ + knowledge  │
└──────────────┘     └──────────────────┘     └───────────────┘     └──────────────┘
  Onboarding Agent     OnboardedMCPServer       RTFM Agent            Operator
  (Java, NATS)         Reconciler (Go)          (Java, NATS)          (Go)
```

### Internal Agents

The onboarding system uses three internal agents, deployed by the Kubemoot operator Helm chart:

| Agent | Annotation | Purpose | MCP Tools |
|-------|-----------|---------|-----------|
| Onboarding Agent | `kubemoot.ai/onboarding-mode: "true"` | Detect gaps, search registries, propose servers | kubernetes-mcp, fetch-mcp, github-mcp |
| RTFM Agent | `kubemoot.ai/rtfm-mode: "true"` | Find and index documentation for new servers | kubernetes-mcp, fetch-mcp, github-mcp |
| Quality Evaluator | (uses quality-eval-model) | Evaluate MCP server trustworthiness | github-mcp |

All internal agents use `qwen2.5:7b` (the `quality-eval-model`) - a smaller model sufficient for tool-oriented tasks.

## Enabling Onboarding

Onboarding is enabled in the Kubemoot operator Helm chart via `internalAgents`:

```yaml
# the Flux HelmRelease values in your GitOps repository (overrides)
internalAgents:
  enabled: true
  mcpServers:
    enabled: true
    kubernetes:
      enabled: true
    fetch:
      enabled: true
    github:
      enabled: true
      secretRef: github-token    # Optional: higher API rate limits
      secretKey: password
  onboardingAgent:
    enabled: true
  rtfmAgent:
    enabled: true
  qualityEvaluator:
    enabled: true
```

This deploys:
- **kubemoot-gateway** - MCPGateway aggregating internal MCP servers
- **kubernetes-mcp**, **fetch-mcp**, **github-mcp** - MCP tool servers
- **Onboarding Agent** - with `discuss-priority: low` (observer)
- **RTFM Agent** - with `discuss-role: observer`
- **Quality Evaluator** - for MCPQualityPolicy evaluation

## The Onboarding Flow

### Step 1: Gap Detection

The `OnboardingSubscriber` (Java) runs at LOW discuss priority, observing all discussion threads:

1. On `thread_start` (broadcast): records the query and thread ID
2. On `agree` from any Tooler: increments agree counter
3. On `stand_aside` from any Tooler: increments stand-aside counter
4. After timeout (default 10s): if agrees == 0 and stand-asides > 0, a gap exists

The `GapDetector` maintains per-thread state with automatic cleanup after 5 minutes.

### Step 2: Registry Search

When a gap is detected, the onboarding agent uses its MCP tools to search:

- **Official MCP Registry**: `registry.modelcontextprotocol.io`
- **Smithery**: `smithery.ai/servers`
- **GitHub**: keyword search for `mcp server <domain>`

For each candidate, it evaluates quality using `github-mcp` tools:

| Signal | Check | Tool |
|--------|-------|------|
| Stars | > 10 (or reasonable for age) | `get_repo` |
| Activity | Commits within 90 days | `list_commits` |
| Author | Known org or active contributor | `get_user` |
| Docs | README with usage instructions | Repository |

### Step 3: User Consent

The agent publishes a `proposal` message to the discussion thread:

> Found **kafka-mcp** (87 stars, last commit 3 days ago). Reply `onboard kafka` to deploy.

**Consent syntax:**
```
onboard <domain>
onboard <domain> <doc-url-1> <doc-url-2>
yes, onboard <domain> <doc-url-1>
```

Users can include documentation URLs directly in the consent message. These URLs are stored on the MCPServer CR as the `kubemoot.ai/doc-urls` annotation and passed to the RTFM agent for indexing.

### Step 4: MCPServer Deployment

On consent, the onboarding agent creates an MCPServer CR via `kubernetes-mcp`:

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

The `kubemoot.ai/onboarded: "true"` label triggers the `OnboardedMCPServerReconciler` in the operator.

### Step 5: Auto-Agent Creation (Operator)

The `OnboardedMCPServerReconciler` (Go) watches MCPServer CRs with the `kubemoot.ai/onboarded` label:

1. Waits for `Status.Ready = true` (pod running, tools discovered)
2. Checks tool count from `Status.Tools`
3. Creates the Agent CR with `discussRole: tooler` (Tooler role), the domain's `capabilities`, `discussChannels`, `promptRefs`, and the MCPServer's tool list in `enabledTools`. The scheduler binds it to a `(model, provider)` via the active CrewSchedulingPolicy.

**Tool-count-aware splitting:** When the MCPServer has >15 tools, the reconciler splits them into groups:

```
22 tools discovered → partitionTools()
  queue_declare, queue_delete, queue_list...    → "queue" group (7)
  exchange_declare, exchange_delete...          → "exchange" group (4)
  binding_list, binding_delete                  → "binding" group (2)
  message_publish, message_get...               → "message" group (5)

Merge small groups to stay under 15:
  Group 1: queue + exchange = 11 → kafka-queue-exchange-tooler
  Group 2: binding + message = 7  → kafka-binding-message-tooler
```

Each group agent gets an `MCPServerRef.EnabledTools` filter so it only sees its subset of tools.

### Step 6: Documentation (RTFM Agent)

When the operator finishes creating agents, it publishes to `kubemoot.operator.onboarding.deployed`:

```json
{
  "serverName": "kafka-mcp",
  "domain": "kafka",
  "docUrls": ["https://kafka.apache.org/documentation"],
  "timestamp": "2026-02-11T..."
}
```

The `RtfmSubscriber` (Java) receives this event and:

1. If user provided doc URLs: creates RAGSource CRs directly from those URLs
2. If no URLs: uses `github-mcp` and `fetch-mcp` to discover documentation
3. Creates a RAGSource CR with collection name using underscores (never hyphens)

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: RAGSource
metadata:
  name: kafka-reference
spec:
  source:
    type: git
    git:
      url: https://github.com/example/kafka-mcp
      paths: ["docs"]
  vectorStore:
    collection: kafka_reference
  embeddingModelRef: nomic-embed
```

The indexer job clones the repo, chunks docs, embeds via Ollama, and stores in pgvector. The query service then serves semantic search for the new agent.

## Progress Tracking

Progress is reported on two channels:

### NATS Events (`kubemoot.operator.onboarding.progress`)

| Stage | Publisher | Description |
|-------|----------|-------------|
| `deploying` | OnboardingSubscriber | Setting up MCP server |
| `creating-agent` | OnboardedMCPServerReconciler | Agent + Policy created |
| `indexing-docs` | RtfmSubscriber | Documentation being indexed |
| `ready` | RtfmSubscriber | Tooler fully operational |

### Discussion Thread (user-facing)

Progress is also published as `proposal` messages in the original discussion thread, so the user sees updates in the chat.

## Labels and Annotations

### MCPServer Labels (set by OnboardingSubscriber)

| Label | Description |
|-------|-------------|
| `kubemoot.ai/onboarded: "true"` | Triggers OnboardedMCPServerReconciler |
| `kubemoot.ai/domain: <domain>` | Domain name for agent naming |

### MCPServer Annotations (set by OnboardingSubscriber)

| Annotation | Description |
|------------|-------------|
| `kubemoot.ai/doc-urls` | Comma-separated documentation URLs from user |
| `kubemoot.ai/discuss-channel` | Discussion channel for the new Tooler |
| `kubemoot.ai/github-url` | Source repository URL |

### Agent Annotations (set by Helm, injected as env vars by operator)

| Annotation | Env Var | Description |
|------------|---------|-------------|
| `kubemoot.ai/onboarding-mode: "true"` | `KUBEMOOT_ONBOARDING_MODE=true` | Activates OnboardingSubscriber |
| `kubemoot.ai/rtfm-mode: "true"` | `KUBEMOOT_RTFM_MODE=true` | Activates RtfmSubscriber |
| `kubemoot.ai/discuss-priority: low` | `KUBEMOOT_DISCUSS_PRIORITY=low` | Observer priority |
| `kubemoot.ai/discuss-role: observer` | `KUBEMOOT_DISCUSS_TOOLER=false` | Disables DiscussionSubscriber |

### Agent Labels (set by OnboardedMCPServerReconciler)

| Label | Description |
|-------|-------------|
| `kubemoot.ai/tool-group: <suffix>` | Tool group for split agents |
| `kubemoot.ai/mcp-server: <name>` | Source MCPServer reference |

## Internal MCP Server RBAC

The internal `kubernetes-mcp` server needs broad permissions to create Kubemoot CRs:

```yaml
ClusterRole: kubemoot-mcpserver-creator
rules:
  - apiGroups: ["kubemoot.ai"]
    resources: ["mcpservers", "ragsources", "agents", "agentpolicies"]
    verbs: ["create", "get", "list", "watch", "update", "patch"]
  - apiGroups: [""]
    resources: ["pods", "namespaces", "services", "configmaps", "secrets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "statefulsets", "daemonsets"]
    verbs: ["get", "list", "watch"]
```

## Troubleshooting

### Onboarding agent not detecting gaps

1. Verify the agent is deployed: `kubectl get agents -l kubemoot.ai/onboarding-mode=true`
2. Check NATS connectivity: agent logs should show `Connected to NATS`
3. Verify `KUBEMOOT_ONBOARDING_MODE=true` env var is set
4. Ensure discussions are broadcasting to all channels (not single-channel routing)

### Consent not recognized

The consent parser expects: `onboard <domain>` or `yes, onboard <domain>`. The domain must match a pending proposal in the GapDetector (proposals expire after 10 minutes).

### Auto-created agent has no tools

The OnboardedMCPServerReconciler waits for `Status.Ready = true` AND `Status.Tools` to be populated. Check:
1. MCPServer pod is running
2. Gateway has registered the server: `kubectl get mcpgw -o yaml` (check `status.mcpServers`)
3. Bridge initialized correctly (check bridge sidecar logs)

### RTFM agent not creating RAGSource

1. Check the RTFM agent receives the deployed event: look for `kubemoot.operator.onboarding.deployed` in logs
2. Verify `KUBEMOOT_RTFM_MODE=true` is set
3. Check if `kubernetes-mcp` tools are available (RTFM needs them to create CRs)

### Tool splitting creates too many agents

The default `maxToolsPerAgent` is 15. Tools are grouped by underscore/hyphen prefix, then merged until groups reach the limit. If an MCP server has tools with many different prefixes, you may get several small agents. Consider using `KUBEMOOT_ENABLED_TOOLS` on the MCPServer to reduce the tool set before splitting.
