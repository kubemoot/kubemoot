---
title: "RAGSource Guide"
description: "RAGSource manages a retrieval knowledge source end to end: cloning documents, chunking, embedding through a model server, storing in pgvector, and verifying the index."
weight: 4
---

## Overview

RAGSource is a Kubemoot CRD that manages the complete lifecycle of Retrieval-Augmented Generation knowledge sources. It automates: cloning source documents, chunking, embedding through a model server, storing vectors in pgvector, deploying a query service, and continuously verifying data integrity.

The operator follows the Kubernetes reconciler contract: compare desired state (the RAGSource spec) against observed state (what's actually in the vector store), and reconcile the difference.

## Architecture

```
RAGSource CR
    │
    ├─── Indexer Job (Java/Spring Boot)
    │       ├── Clone git repo / fetch documents
    │       ├── Chunk documents (TokenTextSplitter)
    │       ├── Generate embeddings (model server, /v1/embeddings)
    │       └── Store vectors in pgvector (Spring AI schema: data_{collection})
    │
    ├─── Query Service (Python/FastAPI)
    │       ├── Deployment + Service (auto-deployed)
    │       ├── POST /query - semantic search
    │       └── GET /info - collection stats (used by verification)
    │
    └─── Docling Serve Sidecar (for document source type)
            └── Converts PDF/DOCX/PPTX/HTML/images → markdown
```

### Component Images

| Component | Image | Language |
|-----------|-------|----------|
| Indexer | `registry.example.com/kubemoot/indexer` | Java (Spring Boot) |
| Query Service | `registry.example.com/kubemoot/query-service` | Python (FastAPI) |
| Docling Serve | `quay.io/docling-project/docling-serve-cpu` | Python |

All image versions are managed centrally via the KubemootConfig singleton - never hardcode image tags.

## Static RAGSource Declarations

Static RAGSources are declared in Helm charts and deployed via GitOps. They define permanent knowledge bases that agents can query.

### Example

The Homelab Pilot crew declares 5 RAGSources in `homelab-pilot/charts/homelab-pilot/values.yaml`:

```yaml
ragSources:
  enabled: true
  sources:
    - name: kubectl-reference
      gitUrl: https://github.com/kubernetes/website
      branch: main
      collection: kubectl_reference
      paths:
        - "content/en/docs/reference/kubectl"

    - name: helm-reference
      gitUrl: https://github.com/helm/helm-www
      branch: main
      collection: helm_reference
      paths:
        - "docs"

    - name: talos-reference
      gitUrl: https://github.com/siderolabs/talos
      branch: main
      collection: talos_reference
      paths:
        - "website/content"

    - name: prometheus-reference
      gitUrl: https://github.com/prometheus/docs
      branch: main
      collection: prometheus_reference
      paths:
        - "docs"

    - name: proxmox-reference
      gitUrl: https://github.com/proxmox/pve-docs
      branch: master
      collection: proxmox_reference
      paths:
        - "."
```

The Helm template renders these into RAGSource CRs:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: RAGSource
metadata:
  name: kubectl-reference
  namespace: homelab-pilot
spec:
  source:
    type: git
    git:
      url: https://github.com/kubernetes/website
      branch: main
      paths:
        - "content/en/docs/reference/kubectl"
  embeddingModelRef: nomic-embed
  vectorStore:
    type: pgvector
    endpoint: pgvector.pgvector:5432/vectors
    collection: kubectl_reference
    secretRef: pgvector-credentials
```

### Key Rules for Static Declarations

- **Collection names must use underscores**, not hyphens. They become PostgreSQL table names (`data_{collection}`), and hyphens are invalid SQL identifiers.
- **Never use glob patterns** (`/**`) in `spec.source.git.paths` - the indexer resolves them as literal directory paths. Use plain directory paths; file matching within directories is handled by `includePatterns`.
- **Always verify repo structure** before configuring paths. For example, `helm/helm-www` has docs at `/docs/` (Docusaurus), NOT `/content/en/docs`.

## RAGSources created by an agent

An agent running in RTFM mode can create RAGSources at runtime. The RTFM subscriber
listens for an onboarding-deployed event and creates a RAGSource that points at the
technology's documentation. The operator chart does not deploy an RTFM agent, and
nothing in the operator publishes that event today, so this path is wired by you. See the
[Onboarding Guide](../../operating/onboarding-guide/) for what ships. To index
documentation now, create the RAGSource yourself as described above.

### RTFM Agent Behavior

The RTFM agent (`RtfmSubscriber.java`) listens for deployed MCP servers with documentation URLs. When triggered, it:

- Uses the fetch-mcp and github-mcp tools to find documentation
- Creates a RAGSource CR in the same namespace as the agent

Dynamic RAGSources follow the same lifecycle as static ones - the controller manages indexing, query service deployment, and verification identically.

## Design

**Indexing is a Job; querying is a Deployment.** Indexing (clone, walk, split, embed,
write to pgvector) is a discrete, long-running operation that needs a filesystem and runs
once per spec or content change. Running it in the controller would tie up the controller
for tens of minutes. The operator therefore creates a Kubernetes `Job` per index run, with
the indexer image from `KubemootConfig.spec.images.indexer`, an `emptyDir` at `/data`,
and all configuration injected as environment variables, so the image carries nothing
environment-specific. Several RAGSources index concurrently, a long index survives an
operator restart, and a failed Job persists (with `ttlSecondsAfterFinished`) while the
failure is surfaced in `status.conditions`. Runtime search is a different shape, a
long-lived service, so each RAGSource also gets a query-service Deployment that scales and
probes like any other workload. Re-indexing is triggered two ways: `observedGeneration`
for spec changes and `lastIndexedChecksum` for content changes.

**Embedding models are their own resource.** Chat and embedding models share a server
but differ in everything else: chat models have load and GPU-residency state and are
selected through `CrewSchedulingPolicy`; embedding models have dimensions and are
referenced by name. A single `Model` resource with a `kind` discriminator would make every
reconciler branch and mix two lifecycles. A dedicated `EmbeddingModel` gives each its own
small controller, makes `RAGSource.spec.embeddingModelRef` type-safe (a RAGSource cannot
point at a chat model), and lets you change the embedding model, and re-index, independently
of inference models. See [Models](../models/).

**An EmbeddingModel names the model, not the server.** Stored vectors match only the
model that made them, so the EmbeddingModel pins the model (and its dimensions). Leave
`serverRef` out and the operator binds it to a ModelServer that has the model; the
indexer Job and the query service take the bound server's endpoint from the
EmbeddingModel's `status.endpoint`. When the cluster owner moves the embedding model to
another server, the EmbeddingModel re-binds and no crew changes. The same model on another
engine can make slightly different vectors, so the RAGSource builds a new index beside the
old one and moves readers over when it is complete (see
[When the embedding model changes](#when-the-embedding-model-changes)).

## When the embedding model changes

`status.index` records what produced the index readers use: the collection it is stored in,
the embedding model, the server that served it, the endpoint and the vector size. The query
service reads that collection and embeds a question through that endpoint, so a reader never
compares vectors from two different embedding models.

When the EmbeddingModel is served another way (another model, another server or another
vector size, for example after a move from an Ollama server to a vLLM server), the operator
treats it like changed source data and rebuilds on its own:

1. It builds a new index in a new collection (`status.building`), indexing everything again,
   beside the current one. Readers stay on the current index and the RAGSource stays `Ready`.
2. When the build is complete, `status.index` becomes the new index and the query service
   moves to it.
3. When the query service has finished replacing its pods and reads only the new collection,
   a cleanup Job deletes the old collection (`status.retiredCollections` lists what waits).

A build that fails leaves the current index serving, says so in `status.message` and is
tried again. If the binding changes again while a build runs, the half-built index is
discarded and a new build starts. A server that only moves to another endpoint, with the
same model and vector size, does not rebuild. The tool index of an MCPGateway is a
RAGSource, so it follows the same rules. Keep the old embedding server running until the
RAGSource reports the new index, because the query service needs it to embed questions for
the old one.

A RAGSource indexed before the status carried `index` is recorded as produced by the
embedding model it uses now.

## Source Types

### Git (`type: git`)

The most common source type. Clones a git repository and indexes files from specified paths.

```yaml
spec:
  source:
    type: git
    git:
      url: https://github.com/kubernetes/website
      branch: main
      paths:
        - "content/en/docs/reference/kubectl"
      secretRef: github-token  # Optional: for private repos
```

The indexer (Java) uses JGit for cloning. Key constraints:
- Indexer runs as uid 1000 (non-root) - needs `/data` emptyDir volume
- JGit requires `HOME=/data` env var for config file writes
- Shallow clone (`--depth 1`) for efficiency

### Document (`type: document`) - Docling Integration

For rich documents (PDF, DOCX, PPTX, HTML, images) that need conversion before indexing. The operator automatically attaches a docling-serve sidecar to the indexer job.

```yaml
spec:
  source:
    type: document
    document:
      urls:
        - "https://example.com/technical-manual.pdf"
        - "https://example.com/architecture.docx"
      disableOcr: false  # Enable OCR for scanned PDFs
```

#### How Docling Works

1. Operator creates the indexer Job with a **docling-serve native sidecar** (init container with `restartPolicy: Always`)
2. Docling serve starts on `http://localhost:5001`
3. The indexer's `DocumentSourceLoader.java` calls docling's `/v1/convert/source` REST API for each URL
4. Docling converts the document to markdown, handling:
   - PDF text extraction + OCR for scanned pages
   - DOCX/PPTX structure preservation
   - HTML cleanup
   - Image OCR
5. The markdown output is then chunked and embedded like any other text source

#### Docling Sidecar Configuration

The docling-serve image (`quay.io/docling-project/docling-serve-cpu`) is managed via KubemootConfig. The operator configures:
- CPU resources (docling is CPU-intensive for OCR)
- Startup probe to gate indexer start until docling is ready
- Shared network (localhost) for HTTP communication

### Other Source Types

- **S3** (`type: s3`): For documents in S3-compatible storage
- **URL** (`type: url`): For web pages
- **MCP Registry** (`type: mcp-registry`): For indexing MCP tool descriptions

## Indexing Pipeline

### Job Lifecycle

```
RAGSource Created/Updated
        │
        ▼
  Resolve EmbeddingModel CR
        │
        ▼
  Check delay period (spec.indexer.delay)
        │ (not yet elapsed → requeue)
        ▼
  Check ObservedGeneration vs metadata.generation
        │ (same → skip to verification)
        ▼
  Create Indexer Job
        │
        ▼
  Phase: "Indexing" - requeue every 10s to check job status
        │
        ▼
  Job completes → handleJobSuccess()
        │ - Update IndexingStats
        │ - Set Phase: "Ready"
        │ - Clear LastJobName
        │ - Deploy/update query service
        │ - Calculate NextIndexTime
        ▼
  Requeue for next reconcile (5 min default or scheduled)
```

### Build slots per embedding server

Several RAGSources often share one embedding server, and a binding change rebuilds all of
them at once. A CPU server cannot embed that much in parallel: requests slow down until they
time out and the rebuilds never finish. The operator therefore runs at most
`spec.defaults.maxIndexBuildsPerServer` index builds at a time against one embedding server
(default `2`, a positive integer in the cluster `KubemootConfig`; a value below 1 is ignored).

A RAGSource that finds every slot taken goes to phase `Queued`, and `status.message` says how
many builds the server is running. A RAGSource that already serves an index stays `Ready`
while its new index waits. Whether a slot is free is decided from the indexing Jobs that
exist (each carries the label `kubemoot.ai/embedding-server`, the namespace and name of the
bound ModelServer): a Queued RAGSource starts as soon as fewer Jobs than the limit are
running on that server. A Job that completed or failed holds no slot. An EmbeddingModel with
no bound server is not limited.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: KubemootConfig
metadata:
  name: default
spec:
  defaults:
    maxIndexBuildsPerServer: 2
```

### Embedding requests

The indexer sends the embedding server at most `batchSize` texts per request, taken from the
EmbeddingModel's `spec.batchSize` (default `8`; the operator passes it as
`KUBEMOOT_EMBEDDING_BATCH_SIZE`, and the indexer uses 8 when it is not set). Small requests keep
each call short on a slow server. The client waits up to `KUBEMOOT_EMBEDDING_TIMEOUT` for one
request (default `300s`, sized for CPU servers); the timeout is a safety net, not flow control.

### Resuming a rebuild

A run that fails starts the source over: the indexer embeds every chunk again, because it
writes all chunks in one transaction and does not check which chunk ids already exist.
Chunk ids are deterministic (`ChunkIds`), so a repeated run overwrites the same rows rather than
adding copies, but it does not skip the chunks already stored. Resuming from the chunks already
embedded is not implemented.

### Indexer Internals (Java)

The indexer (`kubemoot/indexer/`) is a Spring Boot application that:

1. **Clones** the git repository using JGit
2. **Loads** documents from configured paths, filtering by `includePatterns`
3. **Chunks** text using Spring AI's `TokenTextSplitter` (512 chars, 50 overlap default)
4. **Embeds** chunks by calling the OpenAI-compatible `/v1/embeddings` API of the EmbeddingModel's server (Ollama and vLLM both serve it) and scales each vector to unit length
5. **Stores** vectors in pgvector using Spring AI's `PgVectorStore` (table: `data_{collection}`)

For document source types, step 2 includes calling the docling-serve sidecar to convert rich documents to markdown first.

### Environment Variables

The controller passes configuration to the indexer job via environment variables:

| Variable | Description |
|----------|-------------|
| `KUBEMOOT_SOURCE_TYPE` | Source type (git, document, etc.) |
| `KUBEMOOT_GIT_URL` | Git URL (git sources) |
| `KUBEMOOT_GIT_BRANCH` | Git branch (git sources) |
| `KUBEMOOT_GIT_PATHS` | Comma-separated paths to index (git sources) |
| `KUBEMOOT_URL` | Document URL (URL sources) |
| `KUBEMOOT_VECTORSTORE_TYPE` | Always `pgvector` currently |
| `KUBEMOOT_VECTORSTORE_ENDPOINT` | PostgreSQL connection string |
| `KUBEMOOT_VECTORSTORE_COLLECTION` | Collection name (becomes table `data_{name}`) |
| `KUBEMOOT_VECTORSTORE_USER` | DB username (from secret) |
| `KUBEMOOT_VECTORSTORE_PASSWORD` | DB password (from secret) |
| `KUBEMOOT_EMBEDDING_ENDPOINT` | Embedding server base URL (the EmbeddingModel's `status.endpoint`) |
| `KUBEMOOT_EMBEDDING_MODEL` | Embedding model name |
| `KUBEMOOT_EMBEDDING_BATCH_SIZE` | Texts per embedding request (the EmbeddingModel's `spec.batchSize`; indexer default 8) |
| `KUBEMOOT_EMBEDDING_TIMEOUT` | Time one embedding request may take (indexer default `300s`; not set by the operator) |
| `KUBEMOOT_CHUNK_SIZE` | Chunk size in characters |
| `KUBEMOOT_CHUNK_OVERLAP` | Chunk overlap in characters |
| `KUBEMOOT_FORCE_REINDEX` | Bypass checksum check |
| `KUBEMOOT_DROP_COLLECTIONS` | Collections to delete instead of indexing (the cleanup Job, see below) |

## Query Service

The controller automatically deploys a query service (Python FastAPI) for each RAGSource. This provides the HTTP API that agents use for semantic search.

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/query` | POST | Semantic search. Body: `{"query": "...", "top_k": 5}` |
| `/info` | GET | Collection stats including `document_count` |
| `/health` | GET | Health check |

### Status Integration

The query service URL is stored in `status.queryEndpoint`:

```yaml
status:
  phase: Ready
  ready: true
  queryEndpoint: http://kubectl-reference-query.homelab-pilot:8000
  indexingStats:
    documentCount: 112
    chunkCount: 823
    lastIndexed: "2026-02-07T15:30:00Z"
```

Agents reference RAGSources by name. The agent runtime resolves the name → `queryEndpoint` → calls `/query` with the user's question → injects results into the system prompt.

## Vector Store Verification

The controller probes the query service every 5 minutes to verify that pgvector actually contains the expected data. This detects scenarios like database wipes, table drops, or storage failures that the controller wouldn't otherwise notice.

### How It Works

During each reconciliation of a Ready RAGSource:

1. Controller calls `GET {queryEndpoint}/info`
2. Parses `stats.document_count` from the JSON response
3. If `document_count` is 0 (or null) but the RAGSource was previously indexed:
   - Logs: `"Vector store data loss detected, triggering re-index"`
   - Resets `status.indexingStats.lastIndexed` to nil
   - Resets `status.observedGeneration` to 0
   - Triggers a new indexing job
4. If the query service is unreachable or returns an error, verification is **skipped** (fail-open to avoid false re-indexes)

### Why This Matters

Without verification, the controller only checks its own status fields (`LastIndexed`, `ObservedGeneration`). If pgvector data is lost externally, the controller thinks everything is fine because the status says "Ready" with a recent `LastIndexed` timestamp. This violates the Kubernetes reconciler contract of comparing desired state vs. observed state.

### Example: Recovery from Data Loss

```bash
# Drop a pgvector collection manually
kubectl exec -n pgvector deploy/pgvector -- \
  psql -U clusteragent -d vectors -c "DROP TABLE data_kubectl_reference;"

# Within 5 minutes, the controller detects the loss:
# "Vector store data loss detected for kubectl-reference, triggering re-index"

# A new indexer job is created, re-indexes the data, and the RAGSource
# returns to Ready with all documents restored.
```

### Verification Timing

- Default reconcile interval: **5 minutes** (when no schedule is set)
- Scheduled RAGSources: verified at each scheduled reconcile
- HTTP timeout: **5 seconds** (fast fail if query service is down)
- Fail-open: network errors skip verification rather than triggering unnecessary re-indexes

## Scheduling and Re-indexing

### Cron Schedule

```yaml
spec:
  indexer:
    schedule: "0 2 * * *"  # Daily at 2am
```

### Staggered Startup

Use `spec.indexer.delay` to prevent all RAGSources from indexing simultaneously:

```yaml
# First source: immediate
- name: kubectl-reference
  indexer:
    delay: "0s"

# Second source: wait 5 minutes
- name: helm-reference
  indexer:
    delay: "5m"
```

### Force Re-index

```yaml
spec:
  indexer:
    forceReindex: true  # Bypass checksum, always re-index
```

### Generation Tracking

The controller tracks `status.observedGeneration` against `metadata.generation`. When the spec changes (e.g., paths updated), the generation increments, triggering automatic re-indexing without needing `forceReindex`.

## Agent Integration

Agents consume RAGSources via the `ragSources` field in their spec:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: k8s-workloads
spec:
  ragSources:
    - name: kubectl-reference
      topK: 3
    - name: helm-reference
      topK: 2
```

The agent runtime:
1. Resolves each RAGSource name → `status.queryEndpoint`
2. On each chat message, queries all RAG sources with the user's question
3. Injects retrieved context into the system prompt before calling the LLM
4. Skips sources with `topK <= 0`

**Important:** Keep total RAG context manageable. With 3 sources at `topK=5` each, the system prompt can bloat to 25K characters, which degrades tool calling reliability with models like qwen2.5:32b.

For guidance on which RAGSources to assign to which agents, the split between coordinator-wide and Analyst-specific knowledge, and how to size `topK` in the context of a full crew design, see [Compose a Crew](../../user-guides/compose-a-crew/).

## Troubleshooting

### RAGSource stuck in "Indexing"

Check the indexer job and pod logs:
```bash
kubectl get jobs -l kubemoot.ai/ragsource=kubectl-reference
kubectl logs job/kubectl-reference-indexer-<timestamp>
```

Common causes:
- Indexer image not pullable (check `imagePullSecrets`)
- Git clone fails (check URL, branch, credentials)
- Embedding server unreachable (check the EmbeddingModel's `status.endpoint`)
- Phase `Queued` is not stuck: the embedding server is running its limit of index builds (see Build slots per embedding server)
- pgvector connection refused (check endpoint, credentials)

### Query service returns null documents

The `/info` endpoint returning `"document_count": null` means the pgvector table doesn't exist. The vector store verification will detect this and trigger re-indexing automatically within 5 minutes.

### Collection name errors

If the collection name contains hyphens, pgvector will fail because hyphens are invalid in SQL identifiers. Always use underscores:

```yaml
# Wrong: collection: kubectl-reference
# Right:
collection: kubectl_reference
```

### Indexer JGit HOME error

If you see `Creating directories for /.config/jgit failed`, the indexer container needs `HOME=/data`. The operator sets this automatically, but custom indexer images must ensure a writable home directory.

## Example Deployment

Measured on the reference homelab, which runs 5 active RAGSources. Document and chunk
counts are a live snapshot of that cluster's indexed sources at time of writing and
will drift as those upstream docs change and get re-indexed; treat the shape (how many
sources, roughly how dense the chunking is) as the useful signal, not the exact counts.

| Name | Source | Documents | Chunks |
|------|--------|-----------|--------|
| kubectl-reference | kubernetes/website | 112 | 823 |
| helm-reference | helm/helm-www | 96 | 235 |
| talos-reference | siderolabs/talos | 54 | 142 |
| prometheus-reference | prometheus/docs | ~100 | 334 |
| proxmox-reference | proxmox/pve-docs | ~10 | 5 |

All use the `nomic-embed-text` embedding model and pgvector for storage, with vector store verification running every 5 minutes.
