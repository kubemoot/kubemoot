---
title: "7. Embedding Models as a Separate CRD"
weight: 7
---

Date: 2026-01-25

## Status

Accepted

## Context

Kubemoot supports two distinct uses of LLMs: **chat/inference** (an agent responding to a user query, calling tools, generating natural language) and **embedding** (turning text into a vector for similarity search in pgvector).

Both can be served by the same provider (Ollama serves both `qwen3:32b` and `nomic-embed-text`), but they differ in everything else:

- Status fields: chat models have load/unload state and GPU-residency tracking; embedding models have dimensions and supported distance metrics
- Lifecycle: chat models swap in and out of VRAM under demand; embedding models are essentially always "available" once pulled
- Consumers: chat models are referenced by Agents (via CrewSchedulingPolicy); embedding models are referenced by RAGSources for the indexer

Modeling them as one CRD (`Model`) with a `kind: chat | embedding` discriminator would force every reconciler to branch on the discriminator and would conflate two lifecycle stories. Modeling them as separate CRDs keeps each one focused.

## Decision

We will use a dedicated `EmbeddingModel` CRD, distinct from `Model`, with its own controller and status semantics. `RAGSource.spec.embedding.modelRef` references an `EmbeddingModel` by name. `Agent.spec` references `Model` indirectly via `CrewSchedulingPolicy` selectors over labeled Models.

## Consequences

- Two simple controllers instead of one complex one; each has a focused state machine.
- Type-safe references in `RAGSource.spec.embedding.modelRef` - the schema makes it impossible to point a RAGSource at a chat model.
- Operators can upgrade embedding models independently from inference models (relevant when re-indexing is needed).
- Two CRDs in the API instead of one. The cognitive overhead is small because the use cases are also distinct.
- Both CRDs share a common `ModelProvider` reference. If we ever introduce a third model type (image generation, speech), the same pattern repeats.

## References

- `kubemoot/operator/api/v1alpha1/embeddingmodel_types.go`
- `kubemoot/operator/internal/controller/embeddingmodel_controller.go`
- [models.md](../reference/models.md) - embedding model usage
- [ragsource-guide.md](../reference/ragsource-guide.md) - RAGSource → EmbeddingModel reference
