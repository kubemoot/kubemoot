---
title: "Kubemoot Scheduler"
description: "The Kubemoot scheduler matches agents to Model and Provider bindings by capability, using labels and CrewSchedulingPolicy rules to filter, score, and bind."
weight: 5
---

> **Audience:** operator and agent-runtime developers; developers of the ecosystem tools (CrewForge, kmctl); open-source consumers.

## One-Paragraph Summary

Kubemoot is an LLM orchestrator. Like `kube-scheduler` matches Pods to Nodes based on resource requests, the **Kubemoot scheduler matches agents to `(Model, Provider)` bindings based on capability requirements.** Agents declare what they can do (capabilities), Models declare what they are (family, params, capabilities) as labels, ModelProviders declare what they have (VRAM, parallelism), and a `CrewSchedulingPolicy` CRD expresses require/prefer rules using Kubernetes-style label selectors. The match is computed in the agent reconciler via filter → score → bind; the resolved `(model, provider, endpoint)` is applied to the agent's Deployment.

## Design Principles

1. **Only the Model knows what VRAM it needs; only the Provider knows what VRAM it has; only the Scheduler does the match.** Agents and Crews know neither.
2. **No CR below the policy layer mentions a specific model name or a specific provider.** If a CR has to name `qwen3:32b` or `ollama-b`, the abstraction is wrong.
3. **State-driven, not timeout-driven.** Scheduler transitions are triggered by CR changes (Model added, Provider capacity changed, Policy edited), not by elapsed seconds.
4. **Crew portability.** The same crew should deploy to dev (CPU only), staging (1× small GPU), and prod (multi-GPU) with no agent or policy changes - only the parameterized Model bundle differs per cluster.
5. **JIT provider selection at the inference boundary, not at reconcile time.** The decision "which GPU does this call run on?" belongs at the moment the inference is needed, with fresh shared state - not baked into env vars at Agent reconcile. Distribution across providers emerges from many independent local-optimal decisions, the same way `kube-scheduler` produces emergent bin-packing across Nodes.

## JIT Per-Call Provider Selection

Operator-side scheduling (above) resolves each agent's candidate models and providers. The final choice of which provider serves a given inference call is made by the agent runtime at the moment the call is issued, against fresh shared state. There are no tiers: warm reuse, cold load, and eviction are regions that fall out of a single weighted cost, so the same logic scales from one GPU to many.

### The kube-scheduler analogy

`kube-scheduler` does not pre-assign Pods to Nodes at YAML-write time. At the moment a Pod becomes Pending, the scheduler reads fresh Node state (capacity, conditions, taints), applies a hard feasibility filter, scores the survivors with weighted plugins, and binds the best. Across many Pods the distribution is emergent: no central planner moves Pods around, each placement is locally optimal given the state at that instant, and the aggregate is healthy bin-packing.

Kubemoot's provider selection works the same way. Each inference call is the equivalent "Pod becoming Pending" moment, the agent runtime's [`ProviderSelector`](#providerselector-agent-side) is the equivalent scheduler, and NATS KV provider state is the equivalent of the Node status `kube-scheduler` reads. The decision is one feasibility filter followed by a weighted cost, computed per call. The principle: decide which GPU serves a call at the exact point the call is needed, with fresh state, not at a previous time.

### What the operator publishes

The operator's job is to publish state and candidates, not to pick the call's provider:

- The **`ModelProvider` reconciler** probes each provider (Ollama `/api/ps`, DCGM) and publishes per-provider `{totalVramMiB, loadedModelFootprints, ready, lastProbedAt}` to NATS KV bucket `kubemoot_provider_state`. The agent runtime reads this on every inference call.
- The **Agent reconciler** computes the candidate `(Model, Provider)` matches via the filter and score machinery described above, binds the best as the agent's preferred model (`KUBEMOOT_MODEL_MODEL`), and publishes each phase's ranked candidate models as `KUBEMOOT_MODEL_CANDIDATES_MULLING` and `KUBEMOOT_MODEL_CANDIDATES_TRIAGE`. The per-call model and provider are chosen at the inference boundary (see [Candidate models per call](#candidate-models-per-call)).

The agent runtime contains two classes for this:

#### ProviderSelector (agent-side)

`agent-runtime/src/main/java/ai/kubemoot/agent/provider/ProviderSelector.java`

On every call, `ProviderSelector.pickAndClaim(modelName, occupancy)` reads all candidate states from the `kubemoot_provider_state` KV bucket (a 200ms in-memory cache absorbs Tooler fan-out bursts), then runs the feasibility filter and weighted cost below and claims a VRAM ticket against the winner. `pickAndClaimWarm` does the same restricted to providers where the model is already warm (resident or loading) with a free slot, which is how the runtime tries a candidate model without loading it. Both return empty when no provider has room; the mulling path then waits for capacity (see [Waiting for GPU capacity](#waiting-for-gpu-capacity)). When NATS has no provider state at all, the runtime probes the providers directly and falls back to its static endpoint only when no prober is available.

#### Model occupancy (counted once, KV-inclusive)

A model's VRAM occupancy is fixed when it loads: weights plus the context KV slab (`num_ctx × num_parallel`). Ollama reserves the whole slab at load, not per call, so there is no separate per-call KV term. `occupancy(M)` is the observed `/api/ps` footprint of M (KV-inclusive) from any provider that has loaded it, falling back to its on-disk size plus a slab estimate when it has never been loaded anywhere.

#### The one hard filter

A provider where M is warm (resident, or being loaded by an in-flight call) is always feasible when it is ready and its circuit is closed: the call shares the loaded copy and queues for a slot. For a cold load, a provider is feasible iff it is ready, its circuit is closed, `usable(P) ≥ occupancy(M)`, and M fits beside the models already resident (`occupancy(M) + resident(P) ≤ usable(P)`), where `usable(P) = totalVram(P) - reserve` (the reserve covers CUDA context, fragmentation, and runtime overhead). A model larger than every card's usable VRAM yields no feasible provider, and the runtime reports it as `model-too-large` rather than spilling silently. A card that could hold M but is full of other resident models is busy, not too small: the runtime waits for it.

#### The context window is a hard constraint

VRAM is not the only limit on where a call can run. An inference engine does not reject a prompt larger than its per-request context. It drops the oldest messages, the question among them, and the model answers without it. Nothing reports the loss. The scheduler therefore treats the context window as a second hard filter, judged for the same provider and model as the VRAM fit: a provider whose context for that model cannot hold the call's prompt is never chosen.

The context for a (provider, model) pair comes from, in order:

1. The model's observed context once it is loaded (`status.capacity.loadedModels[].contextLength`, read from Ollama `/api/ps`).
2. The engine's configured per-request context (`status.capacity.contextLength`, read from `OLLAMA_CONTEXT_LENGTH`).

An unknown context never refuses a call. An engine that picks its own default context reports nothing until a model loads, and a provider with no published context is treated as able to hold the prompt. The check covers the prompt only, not the reply: a reply that runs past the context is bounded by the call's output cap and ends early without losing the question.

Every placement path applies the filter: claiming a provider, queueing on a loaded copy, unloading idle models to make room, triage, and a plan held from selection. When no provider can run an acceptable model with a context that holds the prompt, waiting cannot help, so the refusal is not treated as busy:

- A specialist stands aside at once with `metadata.reason = "prompt-too-large"` (and `metadata.model`).
- A coordinator call fails visibly. It does not fall back to a static endpoint that would cut the prompt.

The tool loop applies the same rule before every model call. It takes the prompt size the engine reported for the previous call, adds the tool results appended since, and compares the total with the context of the provider the call runs on. When the next prompt would not fit, the loop stops with a `failure` signal whose `failureType` is `CONTEXT_EXCEEDED` instead of sending a prompt the engine would cut. Prompt size counts messages, tool results, tool-call arguments, and tool specifications. Before the engine has reported a size, the runtime estimates tokens from characters using a ratio learned from the engine's reports (an exponentially weighted average, starting at 3.5 characters per token, since prose runs near 4 and JSON or code nearer 3).

On Ollama, `OLLAMA_CONTEXT_LENGTH` is the per-request context of each parallel slot, and the total KV cache scales with `OLLAMA_NUM_PARALLEL` times that value. A GPU's memory therefore buys either more parallel slots or a larger per-request context. Operators choose the balance for each provider: more slots serve more concurrent calls, a larger context admits longer prompts. Mixed providers can make different choices, and the scheduler routes each call to one whose context holds it.

#### The weighted cost (lowest wins)

For each feasible provider the runtime sums these weights and picks the minimum:

| Weight | Meaning | Zero when |
|---|---|---|
| contention | in-flight calls / `maxParallel` | provider idle |
| reliability | learned recent failure rate | provider healthy |
| spill | resident set already exceeds usable VRAM (running on CPU) | not spilling |
| load | cost to load M (scales with its weights) | M already warm here |
| best-fit | excess capacity `(usable - occupancy)/occupancy` - reserves big GPUs for big models so a small model does not squat a big card; sized to dominate contention | the model fits the card tightly |
| eviction | cost to evict resident models to fit M; near-infinite if a victim has in-flight (live) work | warm, or `free(P) ≥ occupancy(M)` |

where `free(P) = usable(P) - Σ resident footprints`. `CrewSchedulingPolicy` require/prefer biases fold in as additional weight terms. There are no categorical tiers; the ordering of warm, cold-onto-free, and eviction emerges from the weights.

#### What falls out

- Reusing a warm model costs ~0 (no load, no new VRAM, regardless of model size), so it almost always wins: by the cost, not by a rule.
- A model is never duplicated or migrated just to dodge a busy slot. Queueing at a warm copy (contention weight) is cheaper than a fresh load (load weight) until the queue is genuinely long, at which point spreading a second copy onto an idle card becomes cheaper and the runtime does it.
- A cold load lands on a card with free room; evicting a resident model is far costlier, and evicting one with live work is effectively forbidden, so distinct models partition across cards while the same model concentrates on its home.
- The aggregate distribution is emergent, like `kube-scheduler` bin-packing Nodes: no anti-affinity rules, no sticky bindings, no rebalancer.

#### Candidate models per call

The operator publishes each phase's candidate models as a JSON list, preferred model first:

```json
[{"model":"qwen3:32b","score":70},{"model":"qwen3:14b","score":60},{"model":"qwen3:8b","score":30}]
```

The list holds every Ready `Model` the phase rule's `require` selector admits, one entry per model identifier. `score` is the model's **quality score**: the sum of the rule's matching `prefer` weights, or the `qualityBias` / `latencyClass` contribution when the rule has no `prefer` block. Locality, provider weight, and load penalties are left out, so the score compares models rather than placements. After the preferred model, entries follow by score, then name. Provider readiness and VRAM are not filtered here; the per-call pick reads them live, and the list (and so the Deployment) stays stable while providers come and go.

`CallPlanner.place` makes each placement. It tries, in order:

1. `pickAndClaimWarm` for the preferred model.
2. `pickAndClaimWarm` for each other candidate whose score is at least `preferredScore - tolerance`, highest score first. A warm candidate with a free slot costs no load and no eviction, so it wins over cold-loading the preferred model.
3. `pickAndClaimLoading` for each in-tolerance candidate: a provider where another call's ticket is loading that model right now. The call converges on that copy instead of loading a second model.
4. `pickAndClaim` for the preferred model: the full weighted cost (queue at a warm copy, cold load onto free room).
5. When all of these are empty, the queue-or-unload decision below.

A warm copy another call plans to unload (see below) does not count as warm.

The tolerance is `KUBEMOOT_MODEL_CANDIDATE_TOLERANCE`, default 10 points. Under `qualityBias`, one `latencyClass` tier is `|bias - 0.5| × 100` points from its neighbour at the extremes (the `medium` tent peaks at bias 0.5), so a balanced crew may use a warm neighbouring tier while a crew with a strong bias keeps its tier. Explicit `prefer` weights usually sit further apart than the tolerance. A crew keeps an agent on one model size by narrowing `require`, widening the `prefer` gap, or setting the tolerance to 0. The runtime reads the mulling list; the triage list is published alongside it, and the triage path places only its bound model.

The ChatModel for the call is built for the model actually picked, and the signal records it as `metadata.model`.

#### Waiting for GPU capacity

#### Queue or unload

When steps 1 to 4 find no room, `PlacementCostModel.decide` compares two options, in seconds of expected delay:

- **Queue** (`ProviderSelector.queueOption`): for each ready provider with the preferred model or an in-tolerance candidate resident or loading, the runtime estimates when a new call could start there: the soonest in-flight call's remaining time (from the start times of the provider's in-flight tickets and its recent call latency, the selector's per-endpoint EMA, 30 seconds when none was observed) plus one call latency per call already queued beyond the provider's parallel slots, spread across the slots. The cheapest copy is the queue option.
- **Unload** (`ProviderSelector.planEviction`): on each ready provider whose usable VRAM can hold the model, `EvictionPlanner` chooses idle residents to release (below). The cost is the requested model's load time plus, per victim, the time to unload it and the time to load it again multiplied by its expected near-future requests (`waiters + intents + 0.5 × (recent use + prediction)`). On Ollama a model loads at about 1 GiB per second, an unloaded model reloads from disk at the same speed, and an unload takes about a second. The cheapest provider's plan is the unload option.

Queue wins when its wait is no longer than the unload cost; unload wins when it is cheaper or the only option; with neither, the call waits for memory. Queue and wait-for-memory both return empty, so the call waits for capacity and retries on the next change. Example: a `qwen3:32b` (27 GiB) warm on the 5090 with a recent-use rate of 1 costs about 26 seconds to unload for a `qwen3:14b`, while the 4090's `qwen3:14b` copy with one call 5 seconds into a typical 20-second call frees in 15 seconds, so the call queues. Five calls queued there (95 seconds) against an idle, unwanted `qwen3:8b` (13 seconds) unloads the `qwen3:8b`.

#### Choosing victims

`EvictionPlanner.chooseVictims` only considers idle residents: models with no in-flight ticket on the provider and not already planned for release by another ticket. It returns the fewest victims that make room; among equally few, the least needed by `ModelDemand.LEAST_NEEDED_FIRST`: fewer waiting agents, then fewer intents, then lower recent use plus prediction, then least recently used. It first searches without models that have waiting agents and includes them only when no other choice makes room.

#### Claim first, then release

`TicketManager.claimWithEvictions` claims the ticket before anything is released. The ticket carries the planned victims (`"evicts": {model: footprintMiB}`), and the budget is `active ticket footprints + this load ≤ headroom + credit`, where the credit counts each distinct victim planned by any active ticket on the provider once, and only while the provider still reports it resident. Two planners that pick the same victim therefore cannot both spend its memory: the second one's check sees both loads against one victim's credit and releases its ticket. The claim is also released when a victim picked up an in-flight ticket in the meantime. After a successful claim the planner unloads each victim (`POST /api/generate {"model": victim, "keep_alive": 0}`), drops the victim's residency entry, and makes the call. The victims are logged and recorded on the call's signal as `metadata.evicted`. Other waiters wake on the ticket and provider-state changes as usual.

#### The demand view

`DemandBoard` keeps the shared demand in the NATS KV bucket `kubemoot_model_demand` (created by the operator's streams job, TTL 6h). Model and agent names are encoded into KV-safe tokens; model keys carry no namespace because models are shared across crews, and agent ids are `<namespace>/<agent>`.

| Key | Written | Cleared |
|---|---|---|
| `wait.<model>.<agent>` | when a capacity wait starts | when it ends; entry expires after 10 minutes |
| `intent.<model>.<agent>` | when the agent is selected for a thread, one per candidate model | on the agent's first call or when the thread ends; entry expires after 3 minutes |
| `use.<model>` | on every scheduled call start: a decaying count (half-life 10 minutes) and the last-use time, last writer wins | bucket TTL |
| `sel.<crew>.<agent>` | when the crew selects the agent: a decaying selection frequency and the agent's candidate models | bucket TTL |

Every value carries `expiresAt`, and readers ignore expired entries. One key per model and agent keeps writes free of compare-and-set. The `sel` entries are the forecast: a model's predicted demand is the sum of the selection frequencies of agents that name it. The forecast only orders victims and weights their cost; it never causes a load.

#### Planning at selection

When the coordinator selects an agent for a live thread, the agent's discussion subscriber calls `ChatService.commitToThread`, which publishes the agent's intents, records the selection, and runs `CallPlanner.place` for the first mulling call right away (with an assumed prompt size). A plan that claims a ticket is held for that thread; if the model is not resident on the planned provider, the runtime starts loading it (`POST /api/generate {"model": m}`), so the load overlaps triage and prompt building. The first mulling call takes the held plan when its provider is still ready, and otherwise releases it and places afresh. A held plan is released when the agent's evaluation ends without using it (it stood aside or failed), when the thread closes, or after three minutes as a safety net. Plans made for agents selected together, in one thread or across crews, see each other's tickets and intents, so agents with overlapping candidates converge on one warm or loading copy. Nothing is loaded without a selected agent behind it.

#### Waiting for GPU capacity

When placement comes back empty, the runtime classifies the refusal:

- **Too large:** no known provider has `usable(P) ≥ occupancy(M)` for the preferred model. The agent stands aside at once with `metadata.reason = "model-too-large"` and `metadata.model`. Providers that have not published their VRAM total never prove a model too large.
- **Prompt too large:** some provider could hold the model in VRAM, but none gives it a context that holds the prompt (see [The context window is a hard constraint](#the-context-window-is-a-hard-constraint)). The agent stands aside at once with `metadata.reason = "prompt-too-large"`.
- **Busy:** some provider could hold the model. The agent waits.

The wait is state-driven. `NatsCapacityWatch` holds KV watches on `kubemoot_provider_state` and `kubemoot_provider_tickets` (updates only, metadata only). Every probe publish, ticket claim, and ticket release advances a change counter; the waiter records the counter, retries the placement against fresh state, and blocks until the counter moves. While it waits, its `wait.<model>.<agent>` entry tells other agents' planners not to unload that model. It publishes `waiting` (metadata `model`, `reason: "gpu-busy"`) once, before its first block, and `evaluating` again when a retry claims a GPU. The agent's discussion heartbeat keeps running, so the coordinator keeps its deadline alive.

Two exits give up with `stand_aside` and `metadata.reason = "gpu-busy"`:

- the discussion ends (synthesis or thread close), which wakes the waiter at once; or
- the safety limit passes: `KUBEMOOT_DISCUSS_GPU_WAIT_LIMIT_SECONDS`, defaulting to the discussion synthesis timeout (`KUBEMOOT_DISCUSS_SYNTHESIS_TIMEOUT_SECONDS`, 90 seconds).

The limit is a safety net for a lost wake-up, not the normal exit. If the KV watch cannot be established, the agent stands aside with `gpu-busy` at once rather than wait without a wake source. Calls that cannot wait do not: a coordinator's advisory and synthesis calls fall back to the static endpoint, and triage calls use the static triage endpoint.

With no provider state in NATS, the direct prober checks each endpoint; when none fits, the runtime cannot tell busy from too large, so it waits, and the provider-state watch wakes it when state returns.

A cold load onto free memory needs room beside the models already resident. When idle residents stand in the way, the queue-or-unload decision releases them on demand, so a wait no longer depends on the provider's keep-alive.

#### The crew's answer when agents could not get a GPU

The coordinator records each `waiting` signal and each `stand_aside` carrying `gpu-busy`, `model-too-large`, or `prompt-too-large`. When the discussion settles with no `agree` and no `concern`, and at least one agent stood aside for capacity or is still waiting, the coordinator skips the synthesis call and answers:

- `gpu-busy` or still waiting: "The crew's agents could not get a GPU: every GPU was busy with other work, so none of them could answer in time. This is the cluster's capacity, not the crew's design; ask again in a moment."
- `model-too-large`: "No GPU in this cluster can hold the model `<model>` the agents need; add a smaller Model or a larger GPU."
- `prompt-too-large` is a distinct reason: no provider gives an acceptable model a context that holds the prompt. Raise the per-request context on a provider (`OLLAMA_CONTEXT_LENGTH`) or shorten what the crew sends.

Both sentences appear when both reasons occurred. Otherwise the ordinary synthesis and no-contribution text apply.

#### ChatModelPool

`agent-runtime/src/main/java/ai/kubemoot/agent/provider/ChatModelPool.java`

The Quarkus-injected `ChatModel` is fixed to one base-url at startup - wrong shape for JIT selection. `ChatModelPool` caches `OllamaChatModel` instances keyed by endpoint, model, and call settings (temperature, max tokens, timeout, think), built lazily on first use. A call that picks a candidate model on an endpoint the pool already serves gets its own instance for that model. Built with `.maxRetries(1)` - see [Bounded retries](#bounded-retries-and-failure-attribution) below.

### Bounded retries and failure attribution

Two follow-on protections were essential once selection moved to call-time:

- **`OllamaChatModel.builder().maxRetries(1)`** in `ChatModelPool`. LangChain4j's default `RetryUtils` would have silently retried a slow Ollama call up to 3 times, multiplying the 120s Quarkus/Vertx HTTP timeout into a 6+ minute silent hang. With maxRetries(1), a timeout surfaces as a single failed call in 120s and the agent's failure-signal path takes over.
- **`ChatService.callWithToolLoop` bounded tool retries** via `ToolCallFailure`. Caps same-tool failures at 2 and total tool failures at 4 per loop iteration. When a tool consistently errors (MCP server down, etc.), the loop aborts early instead of grinding through 15 iterations of error responses.

When these protections trip, the agent publishes a **first-class `failure` consensus signal** (not a `stand_aside`) carrying `failureType` + `failedTool` + `lastError` metadata. See `agentic-consensus.md` for the consensus-protocol implications and `agent.md` for the runtime mechanics.

### Per-call attribution

Each `agree` / `concern` signal from a scheduled call carries `metadata.provider = <provider-name>` (the ModelProvider the call actually ran against) and `metadata.model` (the model it ran, which differs from the preferred model when a warm candidate was used). When the call unloaded models to make room, `metadata.evicted` lists them. The dashboard's Agent Summary GPU column shows this real per-call attribution instead of the reconcile-time static label. Confirms emergent bin-packing visually: two co-triaged agents in the same discussion show as `ollama-a` and `ollama-b` when both have capacity, not both pointing at whatever the operator labeled them.

### Ticket claim and crash-safe release

The winning provider's VRAM is reserved with a ticket so concurrent calls account for each other. Each call atomically claims one ticket in NATS KV bucket `kubemoot_provider_tickets` before issuing the inference:

```
{ ticketId: UUID, provider, modelFootprintMiB, holderId, expiresAt }
```

Multiple tickets coexist on one provider as long as their footprints fit its free VRAM; that coexistence is the bin-pack. The claim uses CAS: NATS KV versions every key and commits a write only if the key is still at the expected revision, so two agents racing for the same budget cannot both win. The contended object is the VRAM budget (recomputed on each retry), not the unique ticketId, so the race-safe step is the read-decide-create loop, bounded to a few attempts before falling back to the static endpoint.

Releases are guaranteed by two independent layers:

| Layer | Mechanism | What it covers |
|---|---|---|
| Happy path | `try { call } finally { delete ticket }` | Normal completion, exceptions, tool timeouts, graceful shutdown |
| Safety net | TTL on every ticket (= `callTimeout + 30s`) | Agent pod killed mid-call, JVM crash/OOMKill, NATS partition during release, node failure |

The `finally` delete is the fast reclaim (sub-second); the TTL is the unconditional reclaim, so no dead agent permanently owns VRAM and no reaper process is needed. Both layers must exist; either alone has a failure mode.

### Model footprint source

A model's occupancy is observed first, declared second:

1. **Ollama `/api/ps`** reports `size_vram` per loaded model (weights plus its KV slab). The operator probe writes the observed footprint into `ModelProvider.status.capacity.loadedModelFootprints` and into NATS KV, caching the first-observed value. This KV-inclusive number is the occupancy the selector uses.
2. **`Model.spec.vramMib`** is an optional declared override on the Model CR, used for a model never loaded anywhere so the selector still has a number; it is replaced by the observed value the first time the model loads.

## Quality bias

### Purpose

`CrewSchedulingPolicy.spec.qualityBias` is a per-capability scalar in `[0.0, 1.0]` that biases model selection between speed (low values) and quality (high values) for `SchedulingRule` entries that omit an explicit `prefer` block. It lets crew authors express *"which kind of work benefits from quality and which benefits from speed"* once, in human terms, instead of hand-weighting selectors for every rule.

Agents declare abstract `capabilities` (e.g. `tool-calling`, `reasoning`); the crew author declares what each capability is worth in quality terms; the scheduler resolves to a concrete `Model` at reconcile time. No agent CR names a model size - the same agent definition deploys against any cluster's `Model` bundle.

### CSP shape

```yaml
spec:
  qualityBias:                 # per-capability scalar in [0.0, 1.0]
    reasoning:     "0.7"       # quality-leaning
    tool-calling:  "0.3"       # speed-leaning
    observability: "0.4"
    default:       "0.4"       # fallback when none of an agent's caps map
  rules:
    - phase: mulling
      require: { matchLabels: { capability/tool-calling: "true" } }
      # no explicit prefer block - scheduler auto-derives from qualityBias
```

Values are strings (the CRD field is `map[string]string`) to avoid CRD float-precision issues. Values that don't parse as floats in `[0.0, 1.0]` are ignored.

### Per-agent effective bias

For each Agent, the scheduler computes:

```
effective_bias = max(qualityBias[c] for c in agent.spec.capabilities)
              ↓ (none of the agent's caps mapped)
              qualityBias["default"]
              ↓ (no default either)
              skip - no bias contribution to score
```

**Max combiner**, deliberately conservative - if any of an agent's declared needs says quality matters, the agent gets the quality push.

### Auto-derived score from `latencyClass`

When a rule has no explicit `prefer` block and the policy has a non-empty `qualityBias` map, the scheduler reads each candidate Model's `latencyClass` label and contributes:

| Model `latencyClass` | Contribution to score |
|---|---|
| `high` | `round(bias × 100)` |
| `medium` | `50` (neutral pivot) |
| `low` | `round((1 - bias) × 100)` |
| (no `latencyClass` label) | `0` (no contribution) |

So `bias = 0.7` ranks `high (70) > medium (50) > low (30)`; `bias = 0.3` flips it to `low (70) > medium (50) > high (30)`. On equal scores the Model with the smaller declared `vramMib` wins (a Model that declares none comes after those that do), then the Model name, so a tie never depends on how Models are spelled.

### Override semantics

Explicit `prefer` blocks on a rule always win. The scheduler skips auto-derivation entirely when `len(rule.Prefer) > 0`. Crews with edge-case rules (e.g. "for this phase, force the qwen2.5 family") add a `prefer` block and that rule is unaffected by `qualityBias`.

### Preference, not requirement

Capability declarations on Agents are **preference-shaping**, never hard gates. An agent declaring a capability that no Model can satisfy still schedules against the best available Model. This trades strict capability matching for observability: misconfigurations surface as visibly wrong model bindings on the dashboard, not as scheduling failures, and the system continues to operate.

### Example

A crew of one synthesizing coordinator plus several Toolers (and optionally Analysts), declaring capabilities thoughtfully, distributes naturally across model sizes:

| Agent role | Declared capabilities | effective_bias | Likely binding |
|---|---|---|---|
| Synthesizing coordinator | `[tool-calling, reasoning]` | max(0.3, 0.7) = 0.7 | `latencyClass: high` Model |
| Tooler with light analytical scope | `[tool-calling, observability]` | max(0.3, 0.4) = 0.4 | `latencyClass: low` or `medium` Model |
| Single-shape Tooler | `[tool-calling]` | 0.3 | `latencyClass: low` Model |
| Analyst | `[reasoning]` | 0.7 | `latencyClass: medium` or `high` Model |

The synthesizer holds the larger model because *it* declared `reasoning`. Toolers stay on smaller, faster models because they didn't. One heavy model and many light ones co-exist on the same GPU pool without slot starvation under per-provider `num_parallel=1`.

## CRD Surface

Five CRs participate.

### Model - labeled

Labels declare what the model *is*; `spec.vramMib` declares what it *needs*; `spec.providerRef` declares where it *lives*.

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
  model: qwen3:32b              # the model identifier on the provider
  providerRef: ollama-a       # where this Model is provisioned
  vramMib: 20480                # capacity request - used by the filter step
```

Labels are an open string set. New capabilities can be introduced without changing the CRD schema. The scheduler validates capability strings against the active `CrewSchedulingPolicy` references but does not constrain the vocabulary.

### ModelProvider - capacity-only

`status.capacity` is the source of truth for VRAM and parallelism.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-a
spec:
  type: ollama
  endpoint: http://ollama.ollama-a:11434
status:
  ready: true
  capacity:
    vramTotalMiB: 32768
    vramUsedMiB: 18432
    maxParallel: 1
    agentCount: 3
    loadedModels:
      - name: qwen3:32b
        sizeMiB: 18432
```

### Agent - capability-only

Agents declare capabilities and discussion role. No model name, no provider, no GPU hint.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: kubectl-agent
spec:
  capabilities: [tool-calling, kubernetes]
  discussRole: tooler
  promptRefs: [discussion-protocol, response-style, kubectl-agent-system]
  enabledTools: [...]
status:
  ready: true
  scheduling:
    model: qwen3-32b
    provider: ollama-a
    endpoint: http://ollama.ollama-a:11434
```

See [agent.md](../reference/agent.md) for the full Agent spec.

### CrewSchedulingPolicy - the affinity-class CRD

Expresses require/prefer per discussion phase. Pure Kubernetes affinity idiom. Phases are an open string set whose vocabulary is defined by the active `MootArchetype`.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewSchedulingPolicy
metadata:
  name: homelab-pilot-default
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  archetypeRef: consent-3
  rules:
    - phase: mulling
      require:
        matchLabels:
          capability/tool-calling: "true"
        matchExpressions:
          - key: params
            operator: In
            values: ["14B", "32B"]
      prefer:
        - weight: 100
          selector:
            matchLabels: { family: qwen3, params: "32B" }
        - weight: 50
          selector:
            matchLabels: { family: qwen3, params: "14B" }
    - phase: triage
      require:
        matchLabels:
          capability/tool-calling: "true"
      prefer:
        - weight: 100
          selector:
            matchLabels: { latencyClass: low }
  topologySpread:
    whenUnsatisfiable: ScheduleAnyway
    topologyKey: provider
    phaseKeys: [mulling, triage]
```

`require` is hard (no match → Unschedulable). `prefer` is soft (weighted hint to the scorer). Mirrors `nodeSelector` vs `preferredDuringSchedulingIgnoredDuringExecution`.

### MootArchetype - coordination model + phase vocabulary

Cluster-scoped. Declares the scheduling phases (see the [reference](../../reference/mootarchetype/)) that `CrewSchedulingPolicy.spec.rules[*].phase` references. The operator chart installs `consent-3` (sociocracy 3.0), the one archetype that ships. The operator validates `CrewSchedulingPolicy` phase names against the archetype; the scheduler and the agent runtime use phases fixed in code, and the orchestration is fixed in code today (see the [Roadmap](../../introduction/roadmap/)).

## Scheduling Algorithm

Three stages, mirroring `kube-scheduler`'s structure.

### 1. Filter (feasibility)

A candidate `Model` is feasible iff:

- The Model's labels match `rule.require.matchLabels` and all `matchExpressions`
- The Model's `providerRef` resolves to a Ready ModelProvider
- The Provider has VRAM headroom: `Model.spec.vramMib + eviction_overhead <= Provider.status.capacity.vramTotalMiB`
- The Provider has parallelism headroom: `Provider.status.capacity.agentCount + 1 <= maxParallel * concurrency_factor`

Filter is pure. No side effects.

### 2. Score (rank feasible candidates)

Score is the sum of:

- `prefer.matchLabels` weights - for each `prefer` rule that matches the candidate's labels, add `prefer.weight`
- **Provider weight (phase-aware)** - `ModelProvider.spec.scheduling.weight` (default 100) contributes per phase: mulling adds `+weight` (favors heavy providers); triage adds `-weight` (favors lighter providers). This lets two rigs carrying identically-labeled Models split phase work - a 5090 (`weight=100`) takes mulling, a 4090 (`weight=25`) takes triage - without forcing per-rig labels onto the Models.

The score reads only spec-level inputs. Live provider state (loaded models, agent counts) never enters it, because the winner becomes the agent's default endpoint in the pod template and a pick that followed live state would roll the pod whenever a model loaded or unloaded. Where each inference call actually runs is decided per call by the runtime (see [JIT provider selection](#jit-per-call-provider-selection)).

### 3. Topology spread

If `topologySpread` is configured, distribute bindings so that no single `topologyKey` value (e.g., `provider`) hosts multiple `phaseKeys` for the same discussion. `whenUnsatisfiable: ScheduleAnyway` falls back to the highest-scoring binding even if the spread is violated.

### 4. Bind

Write the chosen `(model, provider, endpoint)` into `Agent.status.scheduling` and template the agent's Deployment env vars (`KUBEMOOT_MODEL_MODEL`, `KUBEMOOT_MODEL_ENDPOINT`, `KUBEMOOT_TRIAGE_MODEL_MODEL_ID`, `KUBEMOOT_TRIAGE_MODEL_ENDPOINT`) as the agent's stable default, plus the ranked candidate lists `KUBEMOOT_MODEL_CANDIDATES_MULLING` and `KUBEMOOT_MODEL_CANDIDATES_TRIAGE` described in [Candidate models per call](#candidate-models-per-call).

## Helm-Parameterized Model Bundle

The same crew chart deploys to clusters with different hardware. The pattern:

| Layer | Per-cluster? | Carries |
|---|---|---|
| Crew chart `values-prod.yaml` | yes | which Models to provision, ModelProvider endpoints, vramMib targets |
| Crew chart `values-dev.yaml` | yes | smaller Models, CPU-only providers |
| Crew chart templates | no - same everywhere | Agent CRs, CrewSchedulingPolicy, capability declarations |

Dev bundle:
```yaml
models:
  - name: phi3-mini
    model: phi3:mini
    providerRef: cpu-ollama
    vramMib: 0
    labels:
      family: phi3
      params: "3.8B"
      capability/tool-calling: "true"
      latencyClass: low
```

Prod bundle:
```yaml
models:
  - name: qwen3-32b
    model: qwen3:32b
    providerRef: ollama-a
    vramMib: 20480
    labels: { family: qwen3, params: "32B", capability/tool-calling: "true", capability/reasoning: "true" }
  - name: qwen3-14b
    model: qwen3:14b
    providerRef: ollama-a
    vramMib: 9216
    labels: { family: qwen3, params: "14B", capability/tool-calling: "true" }
```

The Agents and the `CrewSchedulingPolicy` are identical across clusters. The capability layer carries through; only the concrete fixtures change.

## Zero-Match Surface

Mirror `FailedScheduling` from kube-scheduler.

When `rule.require` has no matching feasible Model for a phase:

- The scheduler writes a status condition on the Agent and on the CrewSchedulingPolicy
- A Kubernetes Event `reason: FailedScheduling` is emitted on the CrewSchedulingPolicy
- The Agent's `status.scheduling.lastUnschedulable` reflects the most recent result:

```yaml
status:
  scheduling:
    lastUnschedulable:
      phase: mulling
      reason: "no Model matches require selectors {capability/tool-calling: true, params: 14B|32B}"
      observedAt: "2026-05-15T15:30:00Z"
      crewSchedulingPolicy: homelab-pilot-default
```

No LLM call is attempted. The discussion either degrades to a documented fallback (e.g., the coordinator runs a single-agent path) or surfaces the failure to the user.

## Future direction - per-discussion NATS binding

Today, binding happens in the agent reconciler and is applied via the Deployment template. The same filter + score + bind algorithm could run inside a NATS-subscribing scheduler component, publishing bindings per discussion phase on `kubemoot.scheduling.<threadId>.<phase>` and letting the agent runtime resolve its endpoint per request. That decouples pod identity from routing entirely (no pod restart on rebind) and lets the scheduler re-evaluate as capacity changes mid-discussion.

This becomes valuable when capacity changes within a discussion (a new ModelProvider joins, a Provider drops, a Model evicts under VRAM pressure). It is not required in a deployment where cluster capacity is static.

## Related

- [agent.md](../reference/agent.md) - Agent CRD spec and operational guide
- [models.md](../reference/models.md) - Model catalog
- [agentic-consensus.md](agentic-consensus.md) - discussion phase semantics
- `chart/kubemoot-operator/templates/mootarchetypes.yaml` - built-in `consent-3` MootArchetype
