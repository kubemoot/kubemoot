---
title: "Models in Kubemoot"
weight: 3
---

This document covers the model layer end to end: the CRDs (ModelProvider, Model, EmbeddingModel), how Agents reach a `(model, provider)` binding via the scheduler, the load-on-demand principle, the model catalog, and GPU compatibility for a reference deployment.

For the framework behind these choices - why tool-calling fidelity outranks coding benchmarks, how to budget VRAM, and how to match model size to discussion role - see [Choosing a Model](../../concepts/choosing-a-model/).

## CRD Hierarchy

```
ModelProvider (where - a model server endpoint)
    │
    ├── Model (what - labeled inference model on a provider)
    │
    └── EmbeddingModel (what - embedding model for RAGSource indexing)
```

Agents declare **capabilities**; the scheduler matches them to a `Model` whose labels satisfy a `CrewSchedulingPolicy`'s `require` / `prefer` selectors. See [Scheduler](../../architecture/scheduler/) for the full algorithm.

## ModelProvider

Declares an inference endpoint. The operator discovers GPU capacity (VRAM, loaded models, agent count) via DCGM metrics and Ollama APIs.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | enum | Yes | `ollama`, `openai`, `anthropic`. Ollama is the model server today; `openai` and `anthropic` exist on the CRD as stubs. |
| `endpoint` | string | For ollama | API endpoint URL |
| `secretRef` | string | For `openai` and `anthropic` | Secret containing API key (stub types) |

### Status

| Field | Source | Description |
|-------|--------|-------------|
| `ready` | Operator probe | Endpoint reachable + API responding |
| `capacity.vramTotalMiB` | DCGM Prometheus | Total GPU VRAM |
| `capacity.vramUsedMiB` | Ollama `/api/ps` | VRAM consumed by loaded models |
| `capacity.gpuModel` | DCGM metrics | e.g., "NVIDIA GeForce RTX 5090" |
| `capacity.maxParallel` | Pod env `OLLAMA_NUM_PARALLEL` | Concurrent request slots |
| `capacity.contextLength` | Pod env `OLLAMA_CONTEXT_LENGTH` | Per-request context (tokens) each slot gives a model; unknown when the engine chooses its own default |
| `capacity.loadedModels[].contextLength` | Ollama `/api/ps` | Context the loaded model actually runs with; takes precedence over `capacity.contextLength` |
| `capacity.agentCount` | Operator | Agents currently bound to this provider |
| `capacity.loadedModels` | Ollama `/api/ps` | Models currently resident |
| `capacity.availableModels` | Ollama `/api/tags` | Models downloaded on this provider |
| `capacity.nodeName` | Kubernetes | Node hosting the Ollama pod |
| `capacity.lastProbed` | Operator | When capacity was last discovered |

The scheduler refuses to place a call whose prompt exceeds a provider's context for the
model, because the engine would silently drop the oldest messages instead of rejecting it
(see [Scheduler](../architecture/scheduler.md#the-context-window-is-a-hard-constraint)).
On Ollama the total KV cache scales with `OLLAMA_NUM_PARALLEL` times
`OLLAMA_CONTEXT_LENGTH`, so a GPU trades parallel slots against per-request context.

Discovery needs the scheduler enabled and, for VRAM, DCGM metrics in Prometheus. A
provider the operator cannot measure (a CPU Ollama, or a GPU host without DCGM)
declares its budget instead:

```yaml
spec:
  scheduling:
    memoryMiB: 6144   # VRAM on a GPU host; host RAM the server may use on a CPU host
```

A discovered VRAM total always wins over the declaration. With neither, agents refuse
cold loads on that provider because the fit gate has no budget to check against.

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-a
spec:
  type: ollama
  endpoint: http://ollama.ollama-a:11434
---
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-b
spec:
  type: ollama
  endpoint: http://ollama.ollama-b:11434
```

`kubectl get mdlp` (short name: `mdlp`).

## Model

Declares a specific inference model on a provider, **labeled** so the scheduler can select it. The operator pulls the model if needed and tracks its lifecycle.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `providerRef` | string | Yes | Name of the ModelProvider CR |
| `model` | string | Yes | Model identifier on the provider (e.g., `qwen3:32b`, `gpt-4o`) |
| `vramMib` | int32 | For GPU providers | Capacity request - used by the scheduler's filter step |
| `contextLength` | int | No | Override maximum context window |
| `quantization` | string | No | Quantization method (e.g., `q4_K_M`, `q8_0`) |

### Labels (open string set, selected by `CrewSchedulingPolicy`)

| Label | Purpose | Examples |
|-------|---------|----------|
| `family` | Model family | `qwen3`, `mistral`, `glm`, `deepseek-r1`, `phi3` |
| `params` | Parameter count | `"3B"`, `"8B"`, `"14B"`, `"24B"`, `"32B"` |
| `capability/<name>` | What the model can do | `capability/tool-calling: "true"`, `capability/reasoning: "true"` |
| `contextWindow` | Max context tokens | `"32768"`, `"131072"`, `"200000"` |
| `latencyClass` | Latency profile | `low`, `medium`, `high` |

### Status

| Field | Description |
|-------|-------------|
| `state` | `Pending`, `Pulling`, `Available`, `Loaded`, `Error` |
| `ready` | Model ready for inference |
| `endpoint` | Inference endpoint (from provider) |
| `modelInfo` | Size, parameters, family, quantization, context length, format, digest |

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-32b
  labels:
    family: qwen3
    params: "32B"
    capability/tool-calling: "true"
    capability/reasoning: "true"
    contextWindow: "32768"
    latencyClass: high
spec:
  model: qwen3:32b
  providerRef: ollama-a
  vramMib: 20480
  quantization: q4_K_M
---
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-8b
  labels:
    family: qwen3
    params: "8B"
    capability/tool-calling: "true"
    contextWindow: "131072"
    latencyClass: low
spec:
  model: qwen3:8b
  providerRef: ollama-b
  vramMib: 5120
  quantization: q4_K_M
```

`kubectl get mdl` (short name: `mdl`).

## EmbeddingModel

Declares an embedding model for RAGSource indexing. Not consumed by chat agents; RAGSources reference EmbeddingModels via `spec.embeddingModelRef`.

### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `providerRef` | string | (required) | Name of the ModelProvider CR |
| `model` | string | (required) | Model name (e.g., `nomic-embed-text`, `text-embedding-3-small`) |
| `dimensions` | int32 | 768 | Vector dimension size |
| `batchSize` | int32 | 32 | Max texts per embedding request |

### Status

| Field | Description |
|-------|-------------|
| `state` | `Pending`, `Pulling`, `Available`, `Error` |
| `ready` | Model ready for embedding |
| `endpoint` | Embedding API endpoint (from provider) |
| `modelInfo` | Dimensions, max input tokens, family |

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: nomic-embed
spec:
  model: nomic-embed-text
  providerRef: ollama-a
```

`kubectl get emb` (short name: `emb`).

## How Agents Reach a Model

The Agent CR does **not** name a model or a provider. It declares capabilities:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: kubectl-agent
spec:
  capabilities: [tool-calling, kubernetes]
  discussRole: tooler
```

The crew's `CrewSchedulingPolicy` declares which models satisfy each discussion phase via Kubernetes-style label selectors:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewSchedulingPolicy
metadata:
  name: homelab-pilot-default
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  rules:
    - phase: mulling
      require:
        matchLabels: { capability/tool-calling: "true" }
        matchExpressions:
          - { key: params, operator: In, values: ["14B", "32B"] }
      prefer:
        - weight: 100
          selector:
            matchLabels: { family: qwen3, params: "32B" }
    - phase: triage
      require:
        matchLabels: { capability/tool-calling: "true" }
      prefer:
        - weight: 100
          selector:
            matchLabels: { latencyClass: low }
```

The scheduler computes a `(model, provider, endpoint)` per agent via filter → score → bind and templates the result into the agent's Deployment as `KUBEMOOT_MODEL_MODEL`, `KUBEMOOT_MODEL_ENDPOINT`, `KUBEMOOT_TRIAGE_MODEL_MODEL_ID`, `KUBEMOOT_TRIAGE_MODEL_ENDPOINT`. See [Scheduler](../../architecture/scheduler/) for the algorithm.

### application.properties wiring

The agent-runtime is Quarkus + LangChain4j. The `quarkus.langchain4j.ollama.chat-model.model-id` property is `BUILD_AND_RUN_TIME_FIXED` and cannot be overridden via env vars at runtime. The operator writes resolved values to `application.properties` in the `{agent}-policy` ConfigMap, mounted at `/app/config/application.properties` (SmallRye ordinal 260, overrides in-jar ordinal 250 at startup).

```properties
quarkus.langchain4j.ollama.chat-model.model-id=qwen3:32b
quarkus.langchain4j.ollama.base-url=http://ollama.ollama-a:11434
quarkus.langchain4j.ollama.chat-model.temperature=0.3
quarkus.langchain4j.ollama.chat-model.num-predict=2048
```

`temperature` and `num-predict` come from `Agent.spec.temperature` and `Agent.spec.maxTokens`.

## Load on Demand - We Do Not Preload

Kubemoot loads a model into provider VRAM **when an agent asks for it, not before**. There is no preload field, no warm-up CRD, no eager-loading hook. This is intentional design, not a missing feature.

### The principle

> Load a model when an agent needs it. Do not tie up a GPU with a model it's not needed.

GPU VRAM is shared capacity. Pinning model X in advance for a guess at what crew A will use starves model Y for whichever agent actually fires next. Any system that hand-declares preload intent is making predictions about future agent demand that the scheduler can - and should - make at request time.

### What handles the cold-load case

When the first inference of the session hits a cold provider, Ollama loads the model from disk to VRAM. This takes **30-60s** for a 30B-class model on an RTX 5090 (and longer on the 4090). After that, the model is resident.

Two mechanisms make this cost acceptable:

1. **`OLLAMA_KEEP_ALIVE=1h`** - once loaded, the model stays in VRAM for an hour of inactivity. Every subsequent query within the session is warm.
2. **`CrewSchedulingPolicy` `prefer` weights** - direct the scheduler toward models the provider is likely to already have resident (via image-locality scoring) and toward models that fit the phase's latency profile.

The cold-load tax is paid once per `(model, provider)` per session. If a user complains that "the first response was slow," the answer is **not** to add preload. The answer is to verify that subsequent queries are fast (they will be), and to consider session-shaping if the cold cost is unacceptable for the workload.

### When to push back on preload

Future requests in the form *"can we just preload model X on GPU Y"* should be re-framed:

1. What problem is the preload solving? (Usually: "first query is slow.")
2. Is the slowness on the *first* query only, or on every query? (If only first: `KEEP_ALIVE` handles the rest - accept the cold cost.)
3. If genuinely needed, address at the *infrastructure* layer: increase `KEEP_ALIVE`, tune the scheduler, or admit that the workload pattern is wrong for shared GPU capacity.
4. Pin VRAM only as a last resort, and never via a chart - manual `curl /api/generate keep_alive=-1` is fine for one-off debugging.

The architectural commitment: **the orchestrator decides what to load. The chart declares what's possible (Model CRs with labels), not what's resident.**

## Model Catalog

> Sizes below are approximate and move with each quantization release. Treat them as
> planning figures and validate the real footprint against
> `ModelProvider.status.capacity` after a cold load.

### Tier 1: Frontier Models (Cloud-Routed, Not Locally Hostable)

These mixture-of-experts families are one to two orders of magnitude beyond a single consumer GPU. On local runtimes they generally exist only as cloud-routed tags, which resolve to the vendor's hosted infrastructure rather than your hardware. Reaching them is a deliberate decision to move inference off the premises.

| Model | Org | Total Params | Active | Tool Calling | Ollama Tag | Self-Host Footprint |
|-------|-----|-------------|--------|--------------|------------|---------------------|
| Kimi K2 family (K2.6, K2.7-Code, K3) | Moonshot AI | 1T MoE | ~32B | Native | `kimi-k2.6:cloud`, `kimi-k3`, `kimi-k2.7-code` | ~630 GB full weights; ~240 GB at 1.8-bit dynamic quant; roughly 4x H200 to self-host |
| GLM-5.2 | Z.ai | MoE | - | Native | `glm-5.2:cloud` | ~223 GB at 1-bit; 256 GB+ at 2-bit |
| DeepSeek V4 Pro | DeepSeek | MoE | - | Native | `deepseek-v4-pro:cloud` | Data-centre class |
| MiniMax M3 | MiniMax | MoE | - | Yes | `minimax-m3` | Data-centre class |

For scale: four H200 class accelerators run roughly $120,000 to $160,000 to buy, or about $4 to $18 per hour to rent. That is the price of admission to this tier, which is why the practical homelab question is which 20B to 35B class model calls tools best.

Used through an Ollama-compatible endpoint:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: deepseek-cloud
spec:
  type: ollama
  endpoint: https://ollama.cloud/v1
```

### Tier 2: Locally Hostable Models (20B-35B Class)

The working range for a single GPU with 24 GB to 48 GB of memory, which is roughly a $5,000 card. Comfortable on a 32 GB card at Q4; workable on a 24 GB card at Q4 with less KV cache headroom.

> **MoE caveat:** mixture-of-experts models load **all** expert weights into VRAM, so size follows *total* params. The active-param count buys **speed**, not VRAM savings. Validate the real cold-load footprint against provider capacity, not the active-param number.

| Model | Org | Params | Tool Calling | Ollama Tag | Approx Q4 | Notes |
|-------|-----|--------|--------------|------------|-----------|-------|
| Qwen3.6 35B-A3B | Alibaba | 35B MoE (3B active) | Yes | `qwen3.6:35b` | ~20 GB | Strong reported agentic tool use for its class. MoE speed at 35B footprint. |
| Qwen3.6 27B | Alibaba | 27B dense | Yes | `qwen3.6:27b` | ~17 GB | Long context. Fits a 24 GB card with headroom. |
| Laguna XS 2.1 | - | 33B MoE (3B active) | Yes | `laguna-xs-2.1` | ~20 GB | Agentic-coding tuned. |
| Nemotron 3 | NVIDIA | 33B | Native (agentic) | `nemotron3:33b` | ~20 GB | Agentic-tuned. NVIDIA-Open license. |
| Gemma 4 26B / 31B | Google | 26B / 31B | Yes | `gemma4:26b`, `gemma4:31b` | ~16 / ~19 GB | Large install base, permissive. Reported tool use is weaker than the Qwen3.6 class; prefer elsewhere for Toolers. |
| Granite 4.1 30B | IBM | 30B | Yes | `granite4.1:30b` | ~18 GB | Built around function calling. Permissive license. |
| LFM2 24B | Liquid AI | 24B | Yes | `lfm2:24b` | ~14 GB | Fits both reference GPUs at Q4. |
| MiniMax M2.7 | MiniMax | - | Yes | `minimax-m2.7` | - | Validate footprint before committing a phase to it. |
| Qwen3 32B | Alibaba | 32B dense | Yes | `qwen3:32b` | ~20 GB | Long-standing default in the reference crew. Superseded by the Qwen3.6 class on tool use. |

### Tier 3: Small Models (3B-14B Class)

Suitable for Tooler agents, low-priority discussion participants, or a 24 GB card with room to spare.

| Model | Org | Params | Tool Calling | Ollama Tag | Approx Q4 | Notes |
|-------|-----|--------|--------------|------------|-----------|-------|
| Qwen3.5 9B | Alibaba | 9B | Yes | `qwen3.5:9b` | ~6 GB | Current small Qwen line. |
| Granite 4.1 8B | IBM | 8B | Yes | `granite4.1:8b` | ~5 GB | Function-calling focus at Tooler size. Permissive license. |
| Granite 4.1 3B | IBM | 3B | Yes | `granite4.1:3b` | ~2 GB | Large co-schedule headroom. |
| LFM2.5 8B | Liquid AI | 8B | Yes | `lfm2.5:8b` | ~5 GB | Low-latency Tooler candidate. |
| Gemma 4 12B | Google | 12B | Yes | `gemma4:12b` | ~8 GB | Permissive, widely available. |
| Qwen3 14B | Alibaba | 14B | Yes | `qwen3:14b` | ~9 GB | Dual-mode (thinking / non-thinking). |
| Qwen3 8B | Alibaba | 8B | Yes | `qwen3:8b` | ~5 GB | Dual-mode. Long-standing Tooler default in the reference crew. |

> A model with a toggleable thinking mode can regress badly in a Tooler slot when
> reasoning is disabled, because it stops planning which tool to call. Confirm the mode
> the runtime actually sends rather than assuming the default.

## GPU Compatibility

Measured on the reference homelab: an RTX 5090 node (`ollama-a`) and an RTX 4090 node
(`ollama-b`). The quantization, VRAM, and headroom figures below are specific to that
hardware; treat them as a worked example of the budgeting method in
[Choosing a Model](../../concepts/choosing-a-model/), not as universal numbers for a
different card.

### RTX 5090 - 32 GB VRAM (`ollama-a`)

| Model | Quantization | VRAM | KV Headroom | Fits? | Recommended Labels |
|-------|-------------|------|-------------|-------|--------------------|
| Qwen3.6:35b-a3b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=qwen3.6, params=35B, capability/tool-calling=true, capability/reasoning=true, latencyClass=medium` |
| Qwen3.6:27b | Q4_K_M | ~17 GB | ~15 GB | Yes | `family=qwen3.6, params=27B, capability/tool-calling=true, latencyClass=medium` |
| Qwen3:32b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=qwen3, params=32B, capability/tool-calling=true, capability/reasoning=true, latencyClass=high` |
| Laguna XS 2.1 | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=laguna, params=33B, capability/tool-calling=true, latencyClass=medium` |
| Nemotron3:33b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=nemotron3, params=33B, capability/tool-calling=true, latencyClass=high` |
| Granite4.1:30b | Q4_K_M | ~18 GB | ~14 GB | Yes | `family=granite4.1, params=30B, capability/tool-calling=true, latencyClass=medium` |
| Gemma4:31b | Q4_K_M | ~19 GB | ~13 GB | Yes | `family=gemma4, params=31B, capability/tool-calling=true, latencyClass=medium` |
| LFM2:24b | Q4_K_M | ~14 GB | ~18 GB | Yes | `family=lfm2, params=24B, capability/tool-calling=true, latencyClass=medium` |
| Qwen3:14b | Q4_K_M | ~9 GB | ~23 GB | Yes | `family=qwen3, params=14B, capability/tool-calling=true, latencyClass=medium` |
| Qwen3:8b | Q4_K_M | ~5 GB | ~27 GB | Yes | `family=qwen3, params=8B, capability/tool-calling=true, latencyClass=low` |
| Any 32B class | Q8_0 | ~35 GB | n/a | No | Exceeds VRAM |

### RTX 4090 - 24 GB VRAM (`ollama-b`)

> **Constraint:** `num_parallel=1` required - `num_parallel=2` causes page cache OOM preventing model reload.

| Model | Quantization | VRAM | KV Headroom | Fits? | Recommended Labels |
|-------|-------------|------|-------------|-------|--------------------|
| Qwen3.6:27b | Q4_K_M | ~17 GB | ~7 GB | Yes | `family=qwen3.6, params=27B, capability/tool-calling=true, latencyClass=medium` |
| LFM2:24b | Q4_K_M | ~14 GB | ~10 GB | Yes | `family=lfm2, params=24B, capability/tool-calling=true, latencyClass=medium` |
| Gemma4:12b | Q4_K_M | ~8 GB | ~16 GB | Yes | `family=gemma4, params=12B, capability/tool-calling=true, latencyClass=low` |
| Qwen3:14b | Q4_K_M | ~9 GB | ~15 GB | Yes | `family=qwen3, params=14B, capability/tool-calling=true, latencyClass=medium` |
| Qwen3.5:9b | Q4_K_M | ~6 GB | ~18 GB | Yes | `family=qwen3.5, params=9B, capability/tool-calling=true, latencyClass=low` |
| Granite4.1:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=granite4.1, params=8B, capability/tool-calling=true, latencyClass=low` |
| LFM2.5:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=lfm2.5, params=8B, capability/tool-calling=true, latencyClass=low` |
| Qwen3:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=qwen3, params=8B, capability/tool-calling=true, latencyClass=low` |
| Granite4.1:3b | Q4_K_M | ~2 GB | ~22 GB | Yes | `family=granite4.1, params=3B, capability/tool-calling=true, latencyClass=low` |
| Any 32B class | Q4_K_M | ~20 GB | ~4 GB | Tight | Loads, but too little KV cache for long discussions |
| Any 24B class | Q8_0 | ~25 GB | n/a | No | Exceeds VRAM |

## Choosing Among These

The selection framework lives in [Choosing a Model](../../concepts/choosing-a-model/): rank on tool-call fidelity rather than coding benchmarks, budget VRAM with KV cache headroom, size mixture-of-experts models by total parameters, match model size to discussion role, and decide by fitness measurement rather than by leaderboard.

Prefer permissive licenses (Apache-2.0, MIT, and comparable) and avoid non-commercial terms.

## Troubleshooting

### Model stuck in Pending

The controller checks if the model exists on the provider. For Ollama, it calls the `/api/tags` endpoint. If the model isn't pulled yet, pull it manually:
```bash
kubectl exec -n ollama deploy/ollama -- ollama pull qwen3:32b
```

### Agent not using the expected model

Check the agent's scheduling status:
```bash
kubectl get agent <name> -o jsonpath='{.status.phase}{"\n"}{.status.message}{"\n"}'
```

If the phase is `Unschedulable`, the `CrewSchedulingPolicy` `require` selectors didn't match any feasible Model. Check Model labels:
```bash
kubectl get models --show-labels
```

Either relax the `require` selectors or add a Model that matches them.

### Temperature and other inference settings

`temperature` and `maxTokens` live on the Agent CR. They're written to `application.properties` by the operator and cannot be overridden via environment variables (Quarkus `BUILD_AND_RUN_TIME_FIXED` constraint). Update the Agent CR and wait for the operator to regenerate the ConfigMap.

## Related

- [Scheduler](../../architecture/scheduler/) - Filter / score / bind algorithm and CrewSchedulingPolicy reference
- [Agent CRD](agent.md) - Agent CRD spec
- [KubemootConfig Guide](kubemootconfig-guide.md) - Image versioning and operator config
