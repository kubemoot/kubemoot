# Kubemoot Operator

Kubernetes operator for AI workflow orchestration. Manages the lifecycle of models, RAG pipelines, MCP servers, and agents.

## Custom Resources

| CRD | Short Name | Description |
|-----|------------|-------------|
| `ModelProvider` | `mp` | Connection to an LLM backend (Ollama is the only supported type today) |
| `Model` | - | LLM model with automatic pulling |
| `EmbeddingModel` | `em` | Embedding model configuration |
| `MCPServer` | `mcp` | Model Context Protocol server deployment |
| `RAGSource` | `rag` | Document indexing with auto-deployed query service |
| `Agent` | - | Orchestration of models, RAG, and tools |

## Getting Started

### Prerequisites

- Go 1.26+
- Docker 17.03+
- kubectl v1.30+
- Kubernetes v1.30+ cluster

### Install CRDs

```bash
make install
```

### Run Locally

```bash
make run
```

### Deploy to Cluster

```bash
# Build and push image
make docker-build docker-push IMG=<registry>/kubemoot-operator:latest

# Deploy
make deploy IMG=<registry>/kubemoot-operator:latest
```

## RAGSource

RAGSource manages document indexing pipelines with intelligent re-indexing:

- Initial delay for staggering multiple sources
- Checksum-based change detection (skip re-indexing if unchanged)
- Auto-deployed query service for semantic search
- Cron scheduling for periodic re-indexing

**Full specification:** See [RAGSource guide](../docs/reference/ragsource-guide.md). For
the complete CRD list, see the [root README](../README.md#building-blocks-all-kubernetes-resources)
and [docs/reference/](../docs/reference/).

### Quick Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: RAGSource
metadata:
  name: docs
spec:
  source:
    type: git
    git:
      url: https://github.com/example/docs.git
      branch: main
  vectorStore:
    type: pgvector
    endpoint: pgvector:5432/vectors
    collection: docs
  embeddingModelRef: nomic-embed
  indexer:
    delay: "2m"             # Stagger start
    schedule: "0 2 * * *"   # Daily at 2am
```

## Development

### Generate Code

```bash
make generate manifests
```

### Build

```bash
make build
```

### Test

```bash
make test
```

### Run Integration Tests

```bash
cd ../k8s/tests
./test-all.sh
```

## RBAC

The operator requires these cluster permissions:

- `jobs.batch`: Create/manage indexer jobs
- `cronjobs.batch`: Scheduled re-indexing
- `deployments.apps`: Query service and agent deployments
- `services`: Query service exposure
- `secrets`: Credential access
- `configmaps`: Script storage

## Architecture

```
┌────────────────────────────────────────────────────────────────┐
│                    RAGSource Controller                         │
│                                                                  │
│  ┌────────────────┐    ┌────────────────┐    ┌──────────────┐  │
│  │   Reconcile    │───▶│  EnsureIndexer │───▶│  Indexer Job │  │
│  └────────────────┘    └────────────────┘    └──────────────┘  │
│         │                                                       │
│         │              ┌────────────────┐    ┌──────────────┐  │
│         └─────────────▶│ EnsureQuery    │───▶│  Deployment  │  │
│                        │ Service        │    │  + Service   │  │
│                        └────────────────┘    └──────────────┘  │
│                                                      │          │
│                        ┌────────────────┐           │          │
│                        │ Update Status  │◀──────────┘          │
│                        │ QueryEndpoint  │                      │
│                        └────────────────┘                      │
└────────────────────────────────────────────────────────────────┘
```

## License

Copyright 2026. Apache License, Version 2.0.
