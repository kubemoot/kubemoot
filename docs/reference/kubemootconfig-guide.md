---
title: "KubemootConfig Guide"
weight: 5
---

## Overview

KubemootConfig is a **cluster-scoped singleton** CRD that centralizes default images and configuration for all Kubemoot components. It eliminates hardcoded image versions in the operator source code and enables GitOps-friendly version management.

Every Kubemoot controller reads from KubemootConfig via the ConfigCache - a thread-safe in-memory cache that the KubemootConfigReconciler populates on startup and updates on every change.

## Architecture

```
KubemootConfig CR ("default")
    │
    ▼
KubemootConfigReconciler
    │
    ▼
ConfigCache (thread-safe, in-memory)
    │
    ├── RAGSourceReconciler     → indexer, queryService, doclingServe images
    ├── AgentReconciler         → agentRuntime image
    ├── MCPServerReconciler     → mcpBridge image
    └── MCPGatewayReconciler    → mcpGateway image
```

The singleton must be named `default`. The operator creates it via the Helm chart on initial install.

## Spec Reference

### Images

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `images.indexer` | string | `ghcr.io/kubemoot/indexer:latest` | RAGSource indexer job image |
| `images.queryService` | string | `ghcr.io/kubemoot/query-service:latest` | RAGSource query service image |
| `images.agentRuntime` | string | `ghcr.io/kubemoot/agent-runtime:latest` | Agent deployment image |
| `images.mcpGateway` | string | `ghcr.io/kubemoot/mcp-gateway:latest` | MCPGateway deployment image |
| `images.mcpBridge` | string | `ghcr.io/kubemoot/mcp-bridge:latest` | MCP bridge sidecar image |
| `images.doclingServe` | string | `quay.io/docling-project/docling-serve-cpu:latest` | Docling document converter image |

### Defaults

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `defaults.imagePullSecrets` | []LocalObjectReference | | Default pull secrets for all Kubemoot workloads |
| `defaults.vectorStoreType` | enum | `pgvector` | Default vector store: `pgvector`, `qdrant`, `milvus`, `chroma` |
| `defaults.vectorStoreEndpoint` | string | | Default vector store endpoint for tool indexing. When empty, MCPServer tool indexing is disabled |
| `defaults.embeddingModel` | string | `nomic-embed` | Default EmbeddingModel CR name for RAGSources |
| `defaults.otelCollectorEndpoint` | string | | Default OpenTelemetry collector for all agents |

## Status

| Field | Type | Description |
|-------|------|-------------|
| `ready` | bool | Configuration applied successfully |
| `lastUpdated` | Time | Last configuration update timestamp |
| `message` | string | Additional status information |

## Example

### Reference KubemootConfig

The Helm chart creates this on install:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: KubemootConfig
metadata:
  name: default
spec:
  images:
    indexer: "ghcr.io/kubemoot/indexer:0.3.1"
    queryService: "ghcr.io/kubemoot/query-service:0.2.0"
    agentRuntime: "ghcr.io/kubemoot/agent-runtime:0.8.5"
    mcpGateway: "ghcr.io/kubemoot/mcp-gateway:0.5.2"
    mcpBridge: "ghcr.io/kubemoot/mcp-bridge:0.4.0"
    doclingServe: "quay.io/docling-project/docling-serve-cpu:latest"
  defaults:
    vectorStoreType: pgvector
    embeddingModel: nomic-embed
```

`defaults.imagePullSecrets` is optional and empty by default: public images are published to
`ghcr.io/kubemoot` and need no pull secret. Set it only if a private mirror requires one, for
example:

```yaml
  defaults:
    imagePullSecrets:
      - name: my-registry-pull-secret
```

## Caching

The operator keeps a thread-safe, in-memory cache of the `default` KubemootConfig's
images and defaults, shared across every controller. The cache is populated on startup
and updated whenever the `default` KubemootConfig changes; controllers read from it on
every reconciliation, so no controller needs its own watch on KubemootConfig.

## Image Version Management

### CI/CD Flow

1. CI builds a component (e.g., mcp-bridge) and pushes a versioned image tag
2. The Flux HelmRelease in your GitOps repository overrides chart defaults with specific versions
3. Helm renders KubemootConfig with the pinned versions
4. All controllers pick up the new image via ConfigCache on next reconcile

### Manual Override

After building a new image version outside of the normal CI flow, manually patch KubemootConfig:

```bash
kubectl patch kubemootconfig default --type=merge -p \
  '{"spec":{"images":{"mcpBridge":"ghcr.io/kubemoot/mcp-bridge:0.4.1"}}}'
```

### Why Not `:latest`?

Using `:latest` tags with `IfNotPresent` pull policy causes stale images - Kubernetes caches the image and never pulls a newer version even when the tag is updated. Always use versioned tags. The Flux HelmRelease values file is the correct place to pin versions.

## Troubleshooting

### Controller using wrong image

Check the KubemootConfig:
```bash
kubectl get kubemootconfig default -o yaml
```

If the values are wrong, check the Flux HelmRelease values:
```bash
kubectl get helmrelease -n flux-system kubemoot-operator -o jsonpath='{.spec.values}' | jq .
```

### imagePullSecrets not applied

KubemootConfig's `defaults.imagePullSecrets` must use the secret name that exists in the target namespace. It is empty by default, since public images pull from `ghcr.io/kubemoot` without a secret. Set it to a secret you create in each namespace only if you point `global.imageRegistry` at a private mirror.

### ConfigCache stale

The cache updates on KubemootConfig reconciliation. If a controller seems to use old values, check that the KubemootConfigReconciler is running:
```bash
kubectl logs deploy/kubemoot-operator | grep KubemootConfig
```
