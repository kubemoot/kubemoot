---
title: "Kubemoot Dashboard"
weight: 2
---

## Overview

The Kubemoot Dashboard is a standalone SvelteKit application providing Kubernetes Dashboard-like observability specifically for Kubemoot CRDs, GPU resources, and real-time agent activity. It enables operators and developers to monitor:

- All Kubemoot custom resources across namespaces
- GPU node status and model loading activity
- MCP server scaling, quality verdicts, and tool discovery
- RAG source indexing progress
- Agent configurations, policies, and multi-agent topology
- Real-time NATS messaging and inter-agent discussions

## Goals

1. **Observability**: Real-time visibility into Kubemoot operator state
2. **Simplicity**: Kubernetes Dashboard-like UX that users already understand
3. **Decoupling**: No knowledge of specific applications (homelab-pilot, etc.)
4. **Performance**: Lightweight, fast-loading dashboard with SSE updates
5. **Real-Time**: Live event streaming via NATS JetStream integration

> For the full observability picture (what data Kubemoot exposes, the Prometheus/Grafana and OpenTelemetry/Tempo dependencies, logs, and the trace-export roadmap) see [observability.md](observability.md). This document covers the dashboard UI itself.

## Non-Goals

- Displaying non-Kubemoot Kubernetes resources (focus on Kubemoot CRDs)
- Infrastructure monitoring (Proxmox, network, storage)
- Log aggregation or metrics visualization (use Grafana/Loki for that)

Note: The dashboard is primarily read-only but has targeted write capabilities for the Verify page (creates/deletes test Jobs) and MCP Reports (admin curation of quality verdicts).

## Architecture

```
┌───────────────────────────────────────────────────────────────────┐
│                        Kubemoot Dashboard                          │
│                      (SvelteKit + Node.js)                        │
├───────────────────────────────────────────────────────────────────┤
│                                                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │   Browser    │  │   Sidebar    │  │    Detail Panel         │  │
│  │   Client     │──│   Nav        │──│    (Resource View)      │  │
│  │   (Svelte)   │  │   (18 items) │  │                        │  │
│  └──────────────┘  └──────────────┘  └────────────────────────┘  │
│          │                                      │                  │
│          │ SSE Connections                       │ API Calls        │
│          ▼                                      ▼                  │
│  ┌───────────────────────────────────────────────────────────────┐│
│  │                    SvelteKit Server                            ││
│  │  /api/sse          /api/kubemoot/*         /api/nats/*          ││
│  │  /api/verify       /api/nodes             /api/health          ││
│  └───────────────────────────────────────────────────────────────┘│
│                    │                          │                     │
│  @kubernetes/client-node              nats (npm TCP client)        │
│                    ▼                          ▼                     │
│  ┌────────────────────────┐  ┌────────────────────────────────┐  │
│  │  Kubernetes API Server │  │      NATS JetStream            │  │
│  │  CustomObjectsApi      │  │  kubemoot.chat.>                │  │
│  │  CoreV1Api, BatchV1Api │  │  kubemoot.discuss.>             │  │
│  └────────────────────────┘  │  kubemoot.operator.>            │  │
│                               └────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────┘
```

## UI Design

### Navigation

The sidebar contains 18 items organized into 6 sections:

**Top-Level**
- **Overview**: Summary cards showing counts and health for all resources
- **Topology**: Agent relationship graph with real-time activity

**Models**
- **Nodes**: Kubernetes nodes with GPU detection and model loading status
- **Providers**: ModelProvider backend configurations
- **Models**: Kubemoot Model CRDs with state indicators
- **Embeddings**: EmbeddingModel configurations and dimensions

**MCP**
- **MCP Servers**: MCPServer instances with sidecar and scaling info
- **MCP Gateways**: MCPGateway routing configuration and registered servers
- **Quality Policies**: MCPQualityPolicy evaluation rules
- **Catalogs**: MCPCatalog registry discovery status
- **Reports**: MCPServerReport quality verdicts with admin curation

**Knowledge**
- **RAG Sources**: RAGSource indexing status, document/chunk counts

**Agents**
- **Agents**: Agent configurations with capabilities, scheduling status (bound model + provider), RAG, and tool status
- **Prompts**: PromptModule contents and reference counts

**Scheduling**
- **Crew Policies**: CrewSchedulingPolicy require/prefer rules per phase
- **Archetypes**: MootArchetype phase vocabulary and state machine

**Messaging**
- **Messages**: NATS subject explorer with real-time message viewer
- **Discussions**: Multi-agent discussion threads with timeline view

**System**
- **Verify**: Test runner for smoke tests and CRD validation
- **Config**: KubemootConfig cluster singleton (images, defaults)

### Status Indicators

| Status | Badge Color | Meaning |
|--------|-------------|---------|
| Ready/Loaded | Green | Resource operational |
| Pending/Pulling | Yellow | Resource initializing |
| Error/Failed | Red | Resource has issues |
| Unknown | Gray | Status unavailable |

## Page Details

### Topology

Interactive agent relationship graph built with Cytoscape.js and dagre hierarchical layout.

- Coordinator node at top, Tooler and Analyst nodes below, connected by edges
- Edges derived from coordinator's `discoverySelector.matchLabels` vs agent labels
- Real-time node pulsing via SSE from `kubemoot.chat.>`: nodes pulse when agents process messages
- Sticky layout position via `sessionStorage` survives page navigation
- Click-to-navigate: clicking a node navigates to the agent detail page

### Discussions

Two-panel thread viewer for multi-agent NATS discussions.

- **Left panel**: Thread list grouped by channel (kubernetes, observability, proxmox, general)
- **Right panel**: Message timeline showing THREAD_START → CONTRIBUTION(s) → SYNTHESIS
- JetStream history replay on page load fetches recent threads from `KUBEMOOT_DISCUSS` stream
- Live SSE updates for new threads and contributions in real-time
- Each message shows agent name, timestamp, and content

### Messages

General-purpose NATS subject explorer for debugging and monitoring.

- Subscribe to any NATS subject pattern (e.g., `kubemoot.chat.>`, `kubemoot.operator.>`)
- 7 preset channel buttons for common subjects
- View JSON payloads in real-time as they arrive
- Supports publishing messages to arbitrary subjects
- Useful for debugging agent communication and operator events

### Verify

Test runner page for Kubemoot operator smoke tests and CRD validation.

- Triggers K8s Jobs via the dashboard API
- Shows job status (running, succeeded, failed), logs, and duration
- Can delete completed jobs
- Uses a separate verify-runner ServiceAccount with elevated permissions
- Job image version configured via `VERIFY_IMAGE` env var (from chart values)

### MCP Reports

Quality verdict management for MCP servers.

- Table view with verdicts: use (green), caution (yellow), avoid (red)
- Trial history showing evaluation phases and phase-by-phase breakdown
- Admin curation: pin verdicts to override automated evaluations
- Cross-namespace listing via `listClusterCustomObject`

### Crew Scheduling Policies

List and detail views for CrewSchedulingPolicy CRDs.

- Shows `require` / `prefer` selectors per phase
- Lists candidate Models that satisfy each rule
- Surfaces `FailedScheduling` events when no Model matches a `require` selector

### Moot Archetypes

List and detail views for MootArchetype CRDs (cluster-scoped).

- Shows declared phase vocabulary (e.g., `consent-3`: `triaging`, `mulling`, `triage`, `evaluating`, `synthesis`)
- Shows the state machine governing phase transitions

## Kubemoot CRDs

### Summary Table

| CRD | API Group | Short Name | Scope | Key Status Fields |
|-----|-----------|------------|-------|-------------------|
| ModelProvider | kubemoot.ai/v1alpha1 | mdlp | Namespaced | ready, phase, endpoint |
| Model | kubemoot.ai/v1alpha1 | mdl | Namespaced | state, endpoint, size |
| EmbeddingModel | kubemoot.ai/v1alpha1 | emb | Namespaced | state, dimensions, endpoint |
| MCPServer | kubemoot.ai/v1alpha1 | mcp | Namespaced | phase, replicas, tools |
| MCPGateway | kubemoot.ai/v1alpha1 | mcpgw | Namespaced | phase, registeredServers |
| MCPQualityPolicy | kubemoot.ai/v1alpha1 | mcpqp | Namespaced | phase, evaluatedCount |
| MCPCatalog | kubemoot.ai/v1alpha1 | mcpcat | Namespaced | phase, serverCount |
| MCPServerReport | kubemoot.ai/v1alpha1 | mcprpt | Namespaced | verdict, confidence |
| RAGSource | kubemoot.ai/v1alpha1 | rag | Namespaced | phase, documentCount, chunkCount |
| Agent | kubemoot.ai/v1alpha1 | agent | Namespaced | phase, endpoint, replicas, scheduling |
| CrewSchedulingPolicy | kubemoot.ai/v1alpha1 | csp | Namespaced | phase, candidateCount |
| MootArchetype | kubemoot.ai/v1alpha1 | moot | Cluster | phases, ready |
| PromptModule | kubemoot.ai/v1alpha1 | pm | Namespaced | ready, referencedBy |
| KubemootConfig | kubemoot.ai/v1alpha1 | wc | Cluster | defaultImages (cluster-scoped) |

### Model States

The Model CRD has a rich state machine:

```
     ┌──────────┐
     │ Pending  │ ← Created, waiting for provider
     └────┬─────┘
          │
          ▼
     ┌──────────┐
     │ Pulling  │ ← Downloading model weights
     └────┬─────┘
          │
          ▼
     ┌──────────┐
     │ Available│ ← Model ready, not loaded to GPU
     └────┬─────┘
          │ loadOnDemand: false
          ▼
     ┌──────────┐
     │  Loaded  │ ← Model active on GPU
     └──────────┘

     At any point:
     ┌──────────┐
     │  Error   │ ← Something went wrong
     └──────────┘
```

## API Design

### REST Endpoints

```
# Health & System
GET  /api/health                              → Health check
GET  /api/version                             → { version: "x.y.z" }
GET  /api/namespaces                          → ["kubemoot", "homelab-pilot", ...]

# Nodes
GET  /api/nodes                               → K8s nodes with GPU info

# Kubemoot CRDs (all support ?namespace= parameter)
GET  /api/kubemoot/modelproviders[/:name]      → ModelProvider list/detail
GET  /api/kubemoot/models[/:name]              → Model list/detail
GET  /api/kubemoot/embeddingmodels[/:name]     → EmbeddingModel list/detail
GET  /api/kubemoot/mcpservers[/:name]          → MCPServer list/detail
GET  /api/kubemoot/mcpgateways[/:name]         → MCPGateway list/detail
GET  /api/kubemoot/mcpqualitypolicies[/:name]  → MCPQualityPolicy list/detail
GET  /api/kubemoot/mcpcatalogs[/:name]         → MCPCatalog list/detail
GET  /api/kubemoot/mcpserverreports[/:name]    → MCPServerReport list/detail
GET  /api/kubemoot/ragsources[/:name]          → RAGSource list/detail
GET  /api/kubemoot/agents[/:name]              → Agent list/detail
GET  /api/kubemoot/crewschedulingpolicies[/:name] → CrewSchedulingPolicy list/detail
GET  /api/kubemoot/mootarchetypes[/:name]      → MootArchetype list/detail
GET  /api/kubemoot/promptmodules[/:name]       → PromptModule list/detail
GET  /api/kubemoot/config                      → KubemootConfig singleton
GET  /api/kubemoot/topology                    → Agent topology graph data

# NATS Integration
GET  /api/nats/subscribe?subject=             → SSE proxy for NATS subjects
GET  /api/nats/history?stream=&subject=       → JetStream history replay
POST /api/nats/publish                        → Publish to NATS subject
GET  /api/nats/config                         → NATS connection status

# Verify (Test Runner)
GET  /api/verify                              → List verify jobs
GET  /api/verify/status?job=&namespace=       → Job status with log streaming
POST /api/verify                              → Create verify job
DELETE /api/verify?job=&namespace=            → Delete completed job

# Real-Time
GET  /api/sse?namespace=                      → K8s resource SSE stream
```

### Namespace Handling

- **List endpoints** use `??` (nullish coalescing): empty string passes through for all-namespace listing via `listClusterCustomObject`
- **Detail endpoints** use `||` (logical OR): empty string falls back to `'kubemoot'` since you can't `get` a namespaced resource without specifying the namespace

### SSE Events

The `/api/sse` endpoint streams K8s resource updates:

```javascript
{
  type: "models" | "modelproviders" | "agents" | ...,
  namespace: "kubemoot",
  items: [...resources]
}
```

Poll interval: 5 seconds (configurable).

## NATS Integration

### Architecture

The dashboard integrates with NATS JetStream for real-time messaging features that go beyond Kubernetes resource watching.

```
Browser ←─SSE─→ SvelteKit Server ←─TCP─→ NATS JetStream
                  (nats npm client)        (port 4222)
```

- Server-side uses the `nats` npm package (TCP client), not WebSocket
- Browser receives events via Server-Sent Events (SSE) from SvelteKit endpoints
- This two-hop architecture avoids exposing NATS directly to browsers

### SSE Proxy Pattern

The `/api/nats/subscribe` endpoint:
1. Accepts a `subject` query parameter (e.g., `kubemoot.chat.>`)
2. Creates a NATS subscription on the server side
3. Streams each received message as an SSE event to the browser
4. Cleans up the subscription when the client disconnects

### JetStream History Replay

The `/api/nats/history` endpoint:
1. Creates an ephemeral JetStream consumer for the specified stream/subject
2. Replays recent messages (configurable count)
3. Returns them as a JSON array for page-load hydration

This pattern is used by the Discussions page to load recent threads and by Messages to show recent activity.

## RBAC Configuration

### Dashboard ClusterRole

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubemoot-dashboard-reader
rules:
  # Core K8s resources (read-only)
  - apiGroups: [""]
    resources: ["nodes", "namespaces", "pods"]
    verbs: ["get", "list", "watch"]

  # Pod logs for verify job streaming
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]

  # Batch jobs for verify test runner
  - apiGroups: ["batch"]
    resources: ["jobs"]
    verbs: ["get", "list", "watch", "create", "delete"]

  # All Kubemoot CRDs (read-only)
  - apiGroups: ["kubemoot.ai"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
```

RBAC rules are defined in `values.yaml` under `rbac.clusterRole.rules` and templated dynamically in the Helm chart.

## Technology

The dashboard is a SvelteKit application, matching the Homelab Pilot stack for
maintainability, with server-side rendering for a fast initial load. It updates over
Server-Sent Events rather than WebSockets: SSE needs no upgrade negotiation, works
through ordinary HTTP proxies and load balancers, and covers both Kubernetes resource
polling (`/api/sse`) and NATS streaming (`/api/nats/subscribe`) with one mechanism. The
SvelteKit server holds the only NATS connection (over TCP); the browser never connects
to NATS directly, so the SSE proxy is the sole boundary that needs securing. Kubernetes
CRDs are read through `@kubernetes/client-node`'s dynamic `CustomObjectsApi`, which
needs no generated client per CRD.

## Deployment

### Helm Chart

Located at `kubemoot/dashboard/charts/kubemoot-dashboard/`.

```yaml
image:
  registry: registry.example.com
  repository: kubemoot/kubemoot-dashboard
  tag: ""  # Defaults to Chart.AppVersion

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: kubemoot.example.com
      paths:
        - path: /dashboard(/.*)?$
          pathType: ImplementationSpecific

resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 128Mi
```

### Access URL

```
https://kubemoot.example.com/dashboard
```

## Security Considerations

1. **Primarily Read-Only**: Dashboard reads Kubemoot CRDs; write access limited to verify jobs and MCP report curation
2. **RBAC Scoped**: Minimal permissions, only Kubemoot CRDs, nodes, and batch jobs
3. **No Secrets**: Never reads or displays secret values
4. **Auth via Ingress**: authentication is handled at the Ingress/Gateway layer (e.g. an OAuth2 proxy or your cloud provider's access policy), not by the dashboard itself
5. **Non-Root Container**: Runs as UID 1001
6. **NATS Isolation**: Browser never connects to NATS directly; server-side SSE proxy only
7. **CSRF Protection**: SvelteKit's built-in CSRF checks require `Origin` header on POST requests

## Related Documents

- [MCPServer guide](../reference/mcpserver-guide.md)
- [RAGSource guide](../reference/ragsource-guide.md)
- [Agent CRD Reference](../reference/agent.md)
- [Architecture Decisions](../adr/)
