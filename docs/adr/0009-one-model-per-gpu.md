---
title: "9. One Model Per GPU"
weight: 9
---

Date: 2026-02-15

## Status

Superseded

The scheduler now bin-packs several models onto a GPU and evicts idle ones on demand (see [Scheduler](../../architecture/scheduler/)). The one-model-per-GPU convention no longer holds.

## Context

Ollama serves one model at a time from VRAM. When a different model is requested, Ollama unloads the current one and loads the new one from disk - a cold swap costing 5 to 15 seconds depending on model size. In a multi-agent discussion where each turn might call a different model on the same GPU, swap overhead accumulates fast. A typical round-trip with mixed coordinator (reasoning model) and Tooler (chat model) calls on the same GPU can spend 20-30 seconds in swap before any useful work happens.

`OLLAMA_MAX_LOADED_MODELS` allows multiple models in VRAM concurrently if space permits. In practice on a 32 GB GPU, a 32B Q4 model (~20 GB) plus a 14B Q4 model (~9 GB) leaves under 3 GB headroom for KV cache - fragile, with unpredictable evictions under memory pressure.

An example two-GPU deployment: an RTX 5090 (32 GB, `ollama-a`) and an RTX 4090 (24 GB, `ollama-b`). Each can host one resident model with comfortable headroom.

## Decision

We will enforce **one model per GPU** by convention through the ModelProvider/Model hierarchy. Each ModelProvider points to a dedicated GPU endpoint; each GPU hosts one resident Model. The crew's `CrewSchedulingPolicy` directs each phase (mulling, triage) to the appropriate GPU via labeled Models. Cross-GPU model selection happens at the scheduler boundary, never inside a single Ollama instance.

## Consequences

- Zero swap overhead within a discussion phase; the resident model stays resident.
- `OLLAMA_KEEP_ALIVE=1h` keeps the model warm across the session, so cold-load cost is paid once.
- Distinct phases can use distinct models on distinct GPUs in parallel (mulling on `ollama-a`, triage on `ollama-b`) without contention.
- Capacity scaling means adding a GPU + a ModelProvider + Models, not running multiple models on one GPU. This trades flexibility for predictability.
- VRAM is a hard upper bound on which models can serve which phases; the scheduler's filter step uses `Model.spec.vramMib` against `Provider.status.capacity.vramTotalMiB`.
- RTX 4090 has a known constraint: `num_parallel=1` is required, since `num_parallel=2` causes page-cache OOM that prevents model reload. This is encoded as Helm chart configuration for `ollama-b`.
- Future GPU topology (more than two GPUs, dedicated cards per phase or per crew) lands naturally as additional ModelProvider/Model pairs without changing the scheduler.

## References

- [Scheduler](../architecture/scheduler.md) - filter step uses `Model.spec.vramMib`
- [Models in Kubemoot](../reference/models.md) - GPU compatibility matrix
- `homelab-pilot/charts/homelab-pilot-crew/templates/models.yaml` - per-GPU Model declarations
