---
title: "8. RAGSource Job-Based Indexing"
weight: 8
---

Date: 2026-02-05

## Status

Accepted

## Context

A RAGSource declares "what to index and where to put it" - typically a git repository, a set of path globs, an embedding model, a target pgvector collection, and chunking parameters. The act of indexing - cloning the repo, walking the paths, splitting text, calling the embedding model, writing to pgvector - is a discrete, long-running operation. It runs once when the RAGSource is created and again whenever the source content changes (cron-based or `forceReindex: true`).

The indexer needs filesystem access (`HOME=/data`, git clone), embedding-model calls (HTTP to Ollama), and database writes (JDBC to pgvector). It must run with `uid 1000` (the indexer image's user) and needs an `emptyDir` at `/data` for the clone. Running this in a long-lived controller pod would tie up controller resources for tens of minutes during a re-index; running it inline in a reconcile loop would block reconciliation.

The query service (semantic search at runtime) has a different shape entirely: a long-lived Python service exposing a `/query` endpoint, connecting to the same pgvector collection.

## Decision

We will run indexing as a Kubernetes `Job` per RAGSource reconcile cycle. The controller creates a Job with the indexer image (resolved from `KubemootConfig.spec.images.indexer`), an `emptyDir` mounted at `/data`, environment variables for vector store JDBC URL, embedding endpoint, and source git URL/paths. The Job runs to completion, writes `documentCount` and `chunkCount` back via the controller, and the controller deploys a long-lived query-service Deployment per RAGSource for runtime semantic search. Re-indexing is two-layer: `observedGeneration` triggers on spec changes, `lastIndexedChecksum` triggers on content changes.

## Consequences

- Indexing scales horizontally - multiple RAGSources can index concurrently in their own Jobs without blocking the controller.
- Long-running indexes survive operator pod restarts; the Job continues, the controller picks up the result on next reconcile.
- The query service runs as a normal Deployment, scaling and probing like any other workload; the dashboard and Agents call it via standard HTTP.
- Two pod-shaped resources per RAGSource (Job + Deployment) is more moving parts than a monolithic indexer would be. The separation is intentional - different lifecycles, different scaling concerns.
- The indexer Job's `emptyDir` needs to be sized for the repository clone; large repos may need ephemeral storage tuning.
- Failed Jobs persist in the cluster with `ttlSecondsAfterFinished`; the controller surfaces failure via `RAGSource.status.conditions`.
- All configuration is operator-injected: `KUBEMOOT_VECTORSTORE_ENDPOINT`, `KUBEMOOT_VECTORSTORE_USER` / `KUBEMOOT_VECTORSTORE_PASSWORD` (from Secret), `KUBEMOOT_EMBEDDING_ENDPOINT`, `KUBEMOOT_EMBEDDING_MODEL`, `HOME=/data`. The indexer image carries no environment-specific configuration.

## References

- `kubemoot/operator/internal/controller/ragsource_controller.go`
- `kubemoot/indexer/` - indexer image source
- `kubemoot/query-service/` - query-service image source
- [ragsource-guide.md](../reference/ragsource-guide.md) - RAGSource spec and operational guide
