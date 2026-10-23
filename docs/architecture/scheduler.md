---
title: "Kubemoot Scheduler"
description: "The Kubemoot scheduler matches agents to Model and ModelServer bindings by capability, using labels and CrewSchedulingPolicy rules to filter, score, and bind."
weight: 5
---

> **Audience:** operator and agent-runtime developers; developers of the ecosystem tools (CrewForge, kmctl); open-source consumers.

## One-Paragraph Summary

Kubemoot is an LLM orchestrator. Like `kube-scheduler` matches Pods to Nodes based on resource requests, the **Kubemoot scheduler matches agents to `(Model, ModelServer)` bindings based on capability requirements.** Agents declare what they can do (capabilities), Models declare what they are (family, params, capabilities) as labels, ModelServers declare what they have (VRAM, parallelism), and a `CrewSchedulingPolicy` CRD expresses require/prefer rules using Kubernetes-style label selectors. The match is computed in the agent reconciler via filter → score → bind; the resolved `(model, server, endpoint)` is applied to the agent's Deployment.

## Design Principles

1. **Only the Model knows what VRAM it needs; only the ModelServer knows what VRAM it has; only the Scheduler does the match.** Agents and Crews know neither.
2. **No CR below the policy layer mentions a specific model name or a specific server.** If a CR has to name `qwen3:32b` or `ollama-b`, the abstraction is wrong.
3. **State-driven, not timeout-driven.** Scheduler transitions are triggered by CR changes (Model added, ModelServer capacity changed, Policy edited), not by elapsed seconds.
4. **Crew portability.** The same crew should deploy to dev (CPU only), staging (1× small GPU), and prod (multi-GPU) with no agent or policy changes - only the parameterized Model bundle differs per cluster.
5. **JIT server selection at the inference boundary, not at reconcile time.** The decision "which GPU does this call run on?" belongs at the moment the inference is needed, with fresh shared state - not baked into env vars at Agent reconcile. Distribution across servers emerges from many independent local-optimal decisions, the same way `kube-scheduler` produces emergent bin-packing across Nodes.

## JIT Per-Call Server Selection

Operator-side scheduling (above) resolves each agent's candidate models and servers. The final choice of which server serves a given inference call is made by the agent runtime at the moment the call is issued, against fresh shared state. There are no tiers: warm reuse, cold load, and eviction are regions that fall out of a single weighted cost, so the same logic scales from one GPU to many.

### The kube-scheduler analogy

`kube-scheduler` does not pre-assign Pods to Nodes at YAML-write time. At the moment a Pod becomes Pending, the scheduler reads fresh Node state (capacity, conditions, taints), applies a hard feasibility filter, scores the survivors with weighted plugins, and binds the best. Across many Pods the distribution is emergent: no central planner moves Pods around, each placement is locally optimal given the state at that instant, and the aggregate is healthy bin-packing.

Kubemoot's server selection works the same way. Each inference call is the equivalent "Pod becoming Pending" moment, the agent runtime's [`ServerSelector`](#serverselector-agent-side) is the equivalent scheduler, and NATS KV server state is the equivalent of the Node status `kube-scheduler` reads. The decision is one feasibility filter followed by a weighted cost, computed per call. The principle: decide which GPU serves a call at the exact point the call is needed, with fresh state, not at a previous time.

### What the operator publishes

The operator's job is to publish state and candidates, not to pick the call's server:

- The **`ModelServer` reconciler** probes each server (Ollama `/api/ps`, DCGM) and publishes per-server `{engine, totalVramMiB, loadedModelFootprints, ready, lastProbedAt}` to NATS KV bucket `kubemoot_server_state`. `engine` is the server's `spec.type`; when it is absent the agent treats the server as Ollama. The agent runtime reads this on every inference call.
- The **Agent reconciler** computes the candidate `(Model, ModelServer)` matches via the filter and score machinery described above, binds the best as the agent's preferred model (`KUBEMOOT_MODEL_MODEL`), and publishes each phase's ranked candidate models as `KUBEMOOT_MODEL_CANDIDATES_MULLING` and `KUBEMOOT_MODEL_CANDIDATES_TRIAGE`. The per-call model and server are chosen at the inference boundary (see [Candidate models per call](#candidate-models-per-call)).

The agent runtime contains two classes for this:

#### ServerSelector (agent-side)

`agent-runtime/src/main/java/ai/kubemoot/agent/modelserver/ServerSelector.java`

On every call, `ServerSelector.pickAndClaim(modelName, occupancy)` reads all candidate states from the `kubemoot_server_state` KV bucket (a 200ms in-memory cache absorbs Tooler fan-out bursts), then runs the feasibility filter and weighted cost below and claims a VRAM ticket against the winner. `pickAndClaimWarm` does the same restricted to servers where the model is already warm (resident or loading) with a free slot, which is how the runtime tries a candidate model without loading it. Both return empty when no server has room; the mulling path then waits for capacity (see [Waiting for GPU capacity](#waiting-for-gpu-capacity)). When NATS has no server state at all, the runtime probes the servers directly and falls back to its static endpoint only when no prober is available.

#### Model occupancy (counted once, KV-inclusive)

A model's VRAM occupancy is fixed when it loads: weights plus the context KV slab (`num_ctx × num_parallel`). Ollama reserves the whole slab at load, not per call, so there is no separate per-call KV term. `occupancy(M)` is the observed `/api/ps` footprint of M (KV-inclusive) from any server that has loaded it, falling back to its on-disk size plus a slab estimate when it has never been loaded anywhere.

#### The one hard filter

A server is a candidate for M only when it has M: loaded, downloaded, or being loaded by an in-flight call. The operator publishes each server's downloaded models from the engine's own list (`availableModelFootprintsMiB`, with the loaded ones in `loadedModels`). An engine loads only what it has, so a server without M cannot serve the call however much room it has, and unloading models there makes no room for it. Both placement policies and the eviction planner use this rule. Ollama lists an untagged model as `name:latest`, so `llama3` matches `llama3:latest`. Models get onto servers because a [Model](../reference/models.md) resource declares them (`spec.model` on `spec.serverRef`): the operator pulls each declared model onto its server, and the Model's status moves from `Pulling` to `Available`, then `Loaded`. Placement never pulls a model at call time; declare the model to make a server a candidate. When no ready server has the model or any in-tolerance candidate, the call fails at once with a failure signal naming the models: no capacity change could place it, so it does not wait.

A server where M is warm (resident, or being loaded by an in-flight call) is always feasible when it is ready and its circuit is closed: the call shares the loaded copy and queues for a slot. For a cold load, a server is feasible iff it is ready, its circuit is closed, `usable(P) ≥ occupancy(M)`, and M fits beside the models already resident (`occupancy(M) + resident(P) ≤ usable(P)`), where `usable(P) = totalVram(P) - reserve` (the reserve covers CUDA context, fragmentation, and runtime overhead). A model larger than every card's usable VRAM yields no feasible server, and the runtime reports it as `model-too-large` rather than spilling silently. A card that could hold M but is full of other resident models is busy, not too small: the runtime waits for it.

#### The context window is a hard constraint

VRAM is not the only limit on where a call can run. An inference engine does not reject a prompt larger than its per-request context. It drops the oldest messages, the question among them, and the model answers without it. Nothing reports the loss. The scheduler therefore treats the context window as a second hard filter, judged for the same server and model as the VRAM fit: a server whose context for that model cannot hold the call's prompt is never chosen.

The context for a (server, model) pair comes from, in order:

1. The model's own window. For Ollama this is the observed context once the model is loaded (`status.capacity.loadedModels[].contextLength`, read from `/api/ps`). For vLLM it is the `max_model_len` the server lists for the model on `/v1/models`, read on every probe, so it is known before the model wakes when the server answers; a server that lists a model without it publishes no window for that model, because a calibrated window can be stale. The window calibration measured (`status.calibration.models[].contextLength`) is used only for a model whose server could not be asked at all. The operator publishes these in the server state as `loadedModelContextLengths`, keyed by model name.
2. The server's configured per-request context. For Ollama this is `status.capacity.contextLength`, read from `OLLAMA_CONTEXT_LENGTH`. For vLLM the operator publishes it only when every model the server serves has a window and they are all the same.

An unknown context never refuses a call. An engine that picks its own default context reports nothing until a model loads, and a server with no published context is treated as able to hold the prompt. The check covers the prompt only, not the reply: a reply that runs past the context is bounded by the call's output cap and ends early without losing the question.

Every placement path applies the filter: claiming a server, queueing on a loaded copy, unloading idle models to make room, triage, and a plan held from selection. When no server can run an acceptable model with a context that holds the prompt, waiting cannot help, so the refusal is not treated as busy:

- A specialist stands aside at once with `metadata.reason = "prompt-too-large"` (and `metadata.model`).
- A coordinator call fails visibly. It does not fall back to a static endpoint that would cut the prompt.

The tool loop applies the same rule before every model call. It takes the prompt size the engine reported for the previous call, adds the tool results appended since, and compares the total with the context of the server the call runs on. When the next prompt would not fit, the loop stops with a `failure` signal whose `failureType` is `CONTEXT_EXCEEDED` instead of sending a prompt the engine would cut. A vLLM server rejects an oversized prompt with HTTP 400 ("maximum context length") instead of cutting it; if one slips past the estimate, the agent reports it as the same `context_exceeded` failure rather than a generic internal error. Prompt size counts messages, tool results, tool-call arguments, and tool specifications. Before the engine has reported a size, the runtime estimates tokens from characters using a ratio learned from the engine's reports (an exponentially weighted average, starting at 3.5 characters per token, since prose runs near 4 and JSON or code nearer 3).

On Ollama, `OLLAMA_CONTEXT_LENGTH` is the per-request context of each parallel slot, and the total KV cache scales with `OLLAMA_NUM_PARALLEL` times that value. A GPU's memory therefore buys either more parallel slots or a larger per-request context. Operators choose the balance for each server: more slots serve more concurrent calls, a larger context admits longer prompts. Mixed servers can make different choices, and the scheduler routes each call to one whose context holds it.

#### The weighted cost (lowest wins)

For each feasible server the runtime sums these weights and picks the minimum:

| Weight | Meaning | Zero when |
|---|---|---|
| contention | in-flight calls / `maxParallel` | server idle |
| reliability | learned recent failure rate | server healthy |
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

#### How placement treats each engine

The feasibility filter, the weighted cost, and the ticket budget above are written for Ollama, which loads models on demand into accelerator memory. Fit and capacity are an engine decision: the agent runtime picks the rule from the `engine` field of the server state, so each engine is judged by what it actually does.

| Question | Ollama | vLLM (and any other OpenAI-compatible server) |
|---|---|---|
| Can the server serve the model? | Resident, or fits the accelerator's usable memory beside what is resident (above) | It hosts the model, awake or asleep. Memory totals, footprints, and what else is resident play no part: the server reserved its memory when it started |
| Does the server have room now? | In-flight calls are fewer than `maxParallel` | The engine holds no waiting request (`requestsWaiting` is 0) and its key-value cache is below 90 percent (`kvCacheUsage`). Calls in flight do not count: they join the engine's batch |
| How long is the wait when there is no room? | The soonest in-flight call's remaining time, plus a call per queued call, over the slots | One call time per waiting request, spread over the running requests |
| What does the cost add up? | The weights above | Only the engine's crowding (waiting requests plus cache use, plus a light touch per call this agent has in flight), the learned reliability, and the load cost for a hosted model that is asleep. There is no best-fit, spill, or eviction term, because a load fits or evicts nothing |
| What does a ticket budget? | The memory left after the resident models | Nothing. The ticket counts the call and never refuses it |

A load the engine does not report is read as no load. A model that only vLLM servers host is never reported too large for the accelerators: `model-too-large` applies to a model no accelerator can hold, and a hosting server already holds it. The operator publishes `maxParallel` for a vLLM server as the engine's own ceiling on concurrently scheduled sequences, not as one slot, so a reader that still divides by it does not serialize the batch.

The learned policy plans from measured speeds, which calibration publishes for Ollama and vLLM servers alike (see [Calibration](#calibration)). A server with no measured speed, such as a vLLM server before its first calibration, gives the learned policy no opinion, and the algorithm places the call. Because the algorithm judges a vLLM server by the rules above, a hosted, awake model with room is placed on its vLLM server without any measurement; the learned policy takes over once the server is calibrated.

#### Candidate models per call

The operator publishes each phase's candidate models as a JSON list, preferred model first:

```json
[{"model":"qwen3:32b","score":70},{"model":"qwen3:14b","score":60},{"model":"qwen3:8b","score":30}]
```

The list holds every available model the phase rule's `require` constraints admit, one entry per model identifier. `score` is the model's **quality score**: the sum of the rule's matching `prefer` weights, or the `qualityBias` (model size) and ability contribution when the rule has no explicit preference (a `prefer` list or `preferFaster`). `preferFaster` is not part of `score`: the runtime applies it on each call. Locality, server weight, and load penalties are left out, so the score compares models rather than placements. After the preferred model, entries follow by score, then name. ModelServer readiness and VRAM are not filtered here; the per-call pick reads them live, and the list (and so the Deployment) stays stable while servers come and go.

`CallPlanner.place` makes each placement. It tries, in order:

1. `pickAndClaimWarm` for the preferred model: a copy that is resident or still loading, with a free slot.
2. `pickAndClaimWarm` for each other candidate whose score is at least `preferredScore - tolerance`, highest score first. A warm candidate with a free slot costs no load and no eviction, so it wins over cold-loading the preferred model.
3. `pickAndClaimRoom` for the preferred model, then each in-tolerance candidate: a load onto free memory, admitted only when no copy of that model is resident or loading on a server whose context holds the prompt.
4. `pickAndClaimLoading` for each in-tolerance candidate, then `pickAndClaim` for the preferred model under the full weighted cost: these queue, on a copy another call is loading or on a warm copy, or load a second copy when the cost says so.
5. When all of these are empty, the queue-or-unload decision below.

Steps 1 to 3 start the call now; step 4 queues. Two rules meet at a copy that is still loading:

- **Convergence:** step 3 never starts a load of a model while a copy of it is resident or loading on a server whose context holds the prompt, with or without a free slot, so agents selected together share one load. A second copy comes only from the weighted cost in step 4. When two calls start the same load at once, the ticket written first keeps it and the later one releases and joins that copy.
- **Slot check:** every claim that joins a copy for a free slot, resident or loading, re-counts the slots after its ticket is written (see [Ticket claim and crash-safe release](#ticket-claim-and-crash-safe-release)), so no more calls keep a loading copy than its engine has slots.

A call that loses the loading copy's last slot therefore does not load the same model elsewhere, and does not queue while an in-tolerance model can start now: it takes that model warm with a free slot (step 2) or loads it onto free memory (step 3). It queues on the loading copy only when no in-tolerance model can start (step 4). The same order holds for a busy resident copy: an in-tolerance model that can load onto free memory is taken before queueing on the preferred model's copy.

A warm copy another call plans to unload (see below) does not count as warm.

The tolerance is `KUBEMOOT_MODEL_CANDIDATE_TOLERANCE`, default 10 points. Under `qualityBias`, one size tier is `|bias - 0.5| × 100` points from its neighbour at the extremes (the `medium` tent peaks at bias 0.5), so a balanced crew may use a warm neighbouring tier while a crew with a strong bias keeps its tier. Explicit `prefer` weights usually sit further apart than the tolerance. A crew keeps an agent on one model size by narrowing `require`, widening the `prefer` gap, or setting the tolerance to 0. The runtime reads the mulling list for mulling calls and the triage list for triage calls.

The ChatModel for the call is built for the model actually picked, and the signal records it as `metadata.model`.

#### Preferring the faster model

A rule may set `preferFaster`, 0 to 100. Speed cannot be a label a crew names, because the same model is fast on one accelerator and slow on another, so the operator hands the number to the runtime (`KUBEMOOT_MODEL_PREFER_FASTER_MULLING` and `KUBEMOOT_MODEL_PREFER_FASTER_TRIAGE`) and the runtime applies it on each call against the speeds the servers publish. A model's speed is the best generation speed, in tokens per second, of any ready server that has the model: what [calibration](#calibration) measured, replaced by the median of solo crew calls once there are enough of them. For each call the runtime raises every candidate's score by `preferFaster` times its speed over the fastest candidate's speed, so a candidate twice as slow as the fastest earns half the points. The call then prefers the candidate with the highest raised score (the bound model keeps a tie), and the tolerance applies to the raised scores. The placement order above does not change, so a faster candidate that is not loaded and becomes preferred can be loaded instead of a warm, slower one within tolerance; give a rule only as much `preferFaster` as the speed is worth a model load.

A candidate with no measured speed earns no points: it is never guessed fast or slow. With no speed measured for any candidate, or with `preferFaster` unset, the ranking is the operator's, with the bound model preferred. A rule that wants small and quick for triage says `size: small` in `prefer` and a `preferFaster` of, say, 50; the size keeps the choice among small models and the measured speed picks among them.

#### Triage uses the candidate list

A triage call takes the same step as a mulling call, with the triage list: the preferred triage model warm with a free slot, else an in-tolerance candidate that is warm with a free slot or fits free memory now. Triage still never unloads another model to make room, so with no room it runs on the agent's triage endpoint, as before. A bound triage model that moved or went away no longer leaves triage without a model while the cluster has another candidate that qualifies. The signal records the model triage ran on.

#### Waiting for GPU capacity

#### Queue or unload

When steps 1 to 4 find no room, `PlacementCostModel.decide` compares two options, in seconds of expected delay:

- **Queue** (`ServerSelector.queueOption`): for each ready server with the preferred model or an in-tolerance candidate resident or loading, the runtime estimates when a new call could start there: the soonest in-flight call's remaining time (from the start times of the server's in-flight tickets and its recent call latency, the selector's per-endpoint EMA, 30 seconds when none was observed) plus one call latency per call already queued beyond the server's parallel slots, spread across the slots. The cheapest copy is the queue option.
- **Unload** (`ServerSelector.planEviction`): on each ready server whose usable VRAM can hold the model, `EvictionPlanner` chooses idle residents to release (below). The cost is the requested model's load time plus, per victim, the time to unload it and the time to load it again multiplied by its expected near-future requests (`waiters + intents + 0.5 × (recent use + prediction)`). On Ollama a model loads at about 1 GiB per second, an unloaded model reloads from disk at the same speed, and an unload takes about a second. The cheapest server's plan is the unload option.

Queue wins when its wait is no longer than the unload cost; unload wins when it is cheaper or the only option; with neither, the call waits for memory. Queue and wait-for-memory both return empty, so the call waits for capacity and retries on the next change. Example: a `qwen3:32b` (27 GiB) warm on the 5090 with a recent-use rate of 1 costs about 26 seconds to unload for a `qwen3:14b`, while the 4090's `qwen3:14b` copy with one call 5 seconds into a typical 20-second call frees in 15 seconds, so the call queues. Five calls queued there (95 seconds) against an idle, unwanted `qwen3:8b` (13 seconds) unloads the `qwen3:8b`.

#### Choosing victims

`EvictionPlanner.chooseVictims` only considers idle residents: models with no in-flight ticket on the server and not already planned for release by another ticket. It returns the fewest victims that make room; among equally few, the least needed by `ModelDemand.LEAST_NEEDED_FIRST`: fewer waiting agents, then fewer intents, then lower recent use plus prediction, then least recently used. It first searches without models that have waiting agents and includes them only when no other choice makes room.

#### Claim first, then release

`TicketManager.claimWithEvictions` claims the ticket before anything is released. The ticket carries the planned victims (`"evicts": {model: footprintMiB}`), and the budget is `active ticket footprints + this load <= headroom + credit` (each model's footprint counted once, see below), where the credit counts each distinct victim planned by any active ticket on the server once, and only while the server still reports it resident. Two planners that pick the same victim therefore cannot both spend its memory: the second one's check sees both loads against one victim's credit and releases its ticket. The claim is also released when a victim picked up an in-flight ticket in the meantime. After a successful claim the planner unloads each victim (`POST /api/generate {"model": victim, "keep_alive": 0}`), drops the victim's residency entry, and makes the call. The victims are logged and recorded on the call's signal as `metadata.evicted`. Other waiters wake on the ticket and server-state changes as usual.

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

When the coordinator selects an agent for a live thread, the agent's discussion subscriber calls `ChatService.commitToThread`, which publishes the agent's intents, records the selection, and runs `CallPlanner.place` for the first mulling call right away (with an assumed prompt size). A plan that claims a ticket is held for that thread; if the model is not resident on the planned server, the runtime starts loading it (`POST /api/generate {"model": m}`), so the load overlaps triage and prompt building. The first mulling call takes the held plan when its server is still ready, its context holds the real prompt, and the plan owns a call slot or a load it started. A plan that only queued on a busy copy (step 4 of the placement order, joining a copy without a counted slot) is released at the first call and placed again against the state read then, in the usual order, so a slot freed since selection, often by the agents' own triage calls, is used; when there is still no room the call queues as before, and the runtime logs where the re-placed call landed. A plan that owns a slot is kept, since its load overlaps triage. A held plan is released when the agent's evaluation ends without using it (it stood aside or failed), when the thread closes, or after three minutes as a safety net. Plans made for agents selected together, in one thread or across crews, see each other's tickets and intents, so agents with overlapping candidates converge on one warm or loading copy. Nothing is loaded without a selected agent behind it.

#### Waiting for GPU capacity

When placement comes back empty, the runtime classifies the refusal:

- **Too large:** no known server has `usable(P) ≥ occupancy(M)` for the preferred model. The agent stands aside at once with `metadata.reason = "model-too-large"` and `metadata.model`. ModelServers that have not published their VRAM total never prove a model too large.
- **Prompt too large:** some server could hold the model in VRAM, but none gives it a context that holds the prompt (see [The context window is a hard constraint](#the-context-window-is-a-hard-constraint)). The agent stands aside at once with `metadata.reason = "prompt-too-large"`.
- **Busy:** some server could hold the model. The agent waits.

The wait is state-driven. `NatsCapacityWatch` holds KV watches on `kubemoot_server_state` and `kubemoot_server_tickets` (updates only, metadata only). Every probe publish, ticket claim, and ticket release advances a change counter; the waiter records the counter, retries the placement against fresh state, and blocks until the counter moves. While it waits, its `wait.<model>.<agent>` entry tells other agents' planners not to unload that model. It publishes `waiting` (metadata `model`, `reason: "gpu-busy"`) once, before its first block, and `evaluating` again when a retry claims a GPU. The agent's discussion heartbeat keeps running, so the coordinator keeps its deadline alive.

Two exits give up with `stand_aside` and `metadata.reason = "gpu-busy"`:

- the discussion ends (synthesis or thread close), which wakes the waiter at once; or
- the safety limit passes: `KUBEMOOT_DISCUSS_GPU_WAIT_LIMIT_SECONDS`, defaulting to the discussion synthesis timeout (`KUBEMOOT_DISCUSS_SYNTHESIS_TIMEOUT_SECONDS`, 90 seconds).

The limit is a safety net for a lost wake-up, not the normal exit. If the KV watch cannot be established, the agent stands aside with `gpu-busy` at once rather than wait without a wake source. Calls that cannot wait do not: a coordinator's advisory and synthesis calls fall back to the static endpoint, and triage calls use the static triage endpoint.

With no server state in NATS, the direct prober checks each endpoint; when none fits, the runtime cannot tell busy from too large, so it waits, and the server-state watch wakes it when state returns.

A cold load onto free memory needs room beside the models already resident. When idle residents stand in the way, the queue-or-unload decision releases them on demand, so a wait no longer depends on the server's keep-alive.

#### The crew's answer when agents could not get a GPU

The coordinator records each `waiting` signal and each `stand_aside` carrying `gpu-busy`, `model-too-large`, or `prompt-too-large`. When the discussion settles with no `agree` and no `concern`, and at least one agent stood aside for capacity or is still waiting, the coordinator skips the synthesis call and answers:

- `gpu-busy` or still waiting: "The crew's agents could not get a GPU: every GPU was busy with other work, so none of them could answer in time. This is the cluster's capacity, not the crew's design; ask again in a moment."
- `model-too-large`: "No GPU in this cluster can hold the model `<model>` the agents need; add a smaller Model or a larger GPU."
- `prompt-too-large` is a distinct reason: no server gives an acceptable model a context that holds the prompt. Raise the per-request context on a server (`OLLAMA_CONTEXT_LENGTH`) or shorten what the crew sends.

Both sentences appear when both reasons occurred. Otherwise the ordinary synthesis and no-contribution text apply.

#### ChatModelPool and engine adapters

`agent-runtime/src/main/java/ai/kubemoot/agent/modelserver/ChatModelPool.java`

The Quarkus-injected `ChatModel` is fixed to one base-url at startup - wrong shape for JIT selection. `ChatModelPool` caches chat clients keyed by endpoint, model, engine, and call settings (temperature, max tokens, timeout, thinking), built lazily on first use. A call that picks a candidate model on an endpoint the pool already serves gets its own instance for that model. Built with a single retry - see [Bounded retries](#bounded-retries-and-failure-attribution) below.

Every scheduled call reaches the model server through the OpenAI-compatible chat API (`/v1/chat/completions`), so the call path is the same on every engine. The API leaves two things open, and a small per-engine adapter (`ChatEngine`) supplies them:

- **Where the API lives** on the server. Ollama serves it at the server endpoint plus `/v1`.
- **How a request turns thinking off.** Ollama takes `reasoning_effort: none`, because it ignores its native `think` setting on this API. Other OpenAI-compatible servers, such as vLLM and SGLang, take `chat_template_kwargs: {enable_thinking: false}`. Turning thinking on, or leaving the agent's setting unset, sends nothing, because thinking models think by default.

The agent picks the adapter from the `engine` field the operator publishes in the server state; an absent `engine` selects the Ollama adapter. The `ModelServer` CRD admits `type: ollama` and `type: vllm`; a `vllm` ModelServer gets the generic OpenAI-compatible adapter and each of its models is called at that model's own server. Supporting another engine means adding one adapter. Calibration is separate: it measures a server through `accelerator-mcp`, which has a client for each engine (Ollama's own API, and streamed `/v1/chat/completions` requests for vLLM).

The static fallback model, used when the scheduler cannot place a tool-loop call, still comes from the Quarkus Ollama extension and its `quarkus.langchain4j.ollama.*` settings.

### The engine adapter seam

The operator does not know how any one model server works. It reaches each server through an engine adapter, chosen by the server's `spec.type` from one registry. Ollama and vLLM are registered, and the `ModelServer` CRD admits those two types. Supporting another engine means writing one adapter and registering it. The agent runtime has a matching seam for the call path (see [ChatModelPool and engine adapters](#chatmodelpool-and-engine-adapters)).

An adapter does two things:

- **Probe.** It asks the server what it holds and how busy it is, and the `ModelServer` reconciler publishes the answer in the server state. A probe that fails leaves the server's last known capacity as it was and publishes none of the adapter's fields, so the state never shows models for a server that stopped answering. A server that answers only in part publishes what it answered. Whether an Ollama server is ready is decided separately, by its version check; a vLLM ModelServer is ready while at least one of its vLLM servers answers its probe.
- **Act.** It makes a model ready (`ready`) or frees it (`release`). For Ollama, `ready` is the prompt-less load a first request would make, and `release` is `keep_alive: 0`. For vLLM, they are sleep and wake (below). For Ollama the agent runtime still loads on demand when a call arrives and does the unloading itself. For vLLM the agent runtime asks the operator, over NATS, to carry them out (see [Waking and sleeping over NATS](#waking-and-sleeping-over-nats)).

An adapter is handed the server's whole spec, not one address, because a server is not a process. It may be one server, a pool of servers behind one endpoint, or one server per model, and nothing in the server state assumes otherwise.

Beyond the fields described above, the server state carries these optional fields. Each is absent when the server's engine does not report it, and a reader treats absent as unknown, never as zero.

| Field | Meaning | Ollama | vLLM |
|---|---|---|---|
| `modelStates` | Each model by name: `awake` (loaded now), `asleep` (parked by the engine and quick to wake), or `available` (the server has it, not loaded) | Filled from the loaded models (`awake`) and the downloaded ones (`available`); never `asleep` | Each answering vLLM server's `/is_sleeping`: `awake` or `asleep`; never `available`, because a started vLLM server is one or the other |
| `requestsRunning`, `requestsWaiting` | Requests the engine is running and holding in its own queue, across the whole ModelServer | Absent: Ollama exports no metrics, so the agents' tickets keep the live counts | From `/metrics`, summed over the ModelServer's vLLM servers |
| `kvCacheUsage` | Fraction of the engine's key-value cache in use, 0 to 1 | Absent | From `/metrics`, the highest over the ModelServer's vLLM servers |
| `switchCostMs` | Observed time to make each model ready, by model, in milliseconds | Absent; the calibrated load time in `modelSpeeds` is the measure | The last observed switch to each model (sleeping the awake one, then waking it), recorded by the operator when it wakes one for an agent |
| `hostMemoryParkedMiB` | Host memory holding the weights of models the engine put to sleep | Absent | Absent for now |
| `modelEndpoints` | The URL a call for each model goes to, by model, for a ModelServer with one vLLM server per model | Absent: every model is at `endpoint` | The URL of each model's vLLM server; `endpoint` is empty |

#### vLLM: sleep and wake, never restart

vLLM serves one model per server process, so a `vllm` ModelServer lists a vLLM server for each served model ([vLLM servers](../../reference/models/#vllm-servers)), and its sleep mode lets those vLLM servers share one accelerator. The adapter switches models by sleeping and waking vLLM servers and never restarts one: on the measured nodes a restart took 34 to 47 seconds, a level 1 wake 0.7 to 1.3, and a level 2 wake 2.5 to 4.9 (see the [measured results](../../concepts/choosing-a-model/#measured-results)).

- **Probe.** Per vLLM server: `/health` (the vLLM server is up), `/v1/models` (the served name), `/is_sleeping` (`awake` or `asleep`; a vLLM server without the sleep endpoints counts as awake), and `/metrics` for `vllm:num_requests_running`, `vllm:num_requests_waiting`, and the key-value cache gauge. Because the server state describes the whole ModelServer, the request counts are summed over its vLLM servers and the cache use is the highest of them. vLLM has no fixed request slots, so the operator publishes `maxParallel` as the engine's default ceiling on concurrently scheduled sequences instead of one, and the request counts and cache use are what show how busy it is (see [How placement treats each engine](#how-placement-treats-each-engine)).
- **Ready.** The adapter first puts every other awake vLLM server of the ModelServer to sleep at the ModelServer's sleep level, then wakes the model's vLLM server. A vLLM server that is down (it answers neither its sleep state nor `/health`) holds no accelerator memory and is skipped. An awake vLLM server that cannot sleep, or whose sleep state cannot be read while it is up, fails the call, because the accelerator cannot be shown to be free. A model whose own vLLM server has no sleep endpoints is always awake, so there is nothing to do for it. At level 1 the wake is one call. At level 2 it is three, in order: restore the weight buffers, reload the weights from disk, restore the key-value cache.
- **Release.** The adapter puts the model's vLLM server to sleep at the ModelServer's sleep level, level 1 by default, which parks the weights in host memory. A vLLM server already asleep is left alone.

The agent runtime never sends Ollama's load and unload calls to another engine's model server: it picks its model lifecycle driver by the ModelServer's engine. The operator does not run vLLM servers: a `vllm` ModelServer points at the Service of a vLLM install such as the `kubemoot-vllm` chart (`spec.serviceRef`), or lists the vLLM servers you run (`spec.servers`). The chart's launcher starts each vLLM server asleep and never wakes one; the adapter is the one place that wakes and sleeps.

#### Waking and sleeping over NATS

Before any call goes to a model the server's state does not show awake, the agent runtime asks the operator to make it ready, by NATS request and reply on `kubemoot.server.model.<server>` (the server name, escaped like a key token). The request is a `ModelLifecycleRequest` with an `action` of `ready` or `release`, and a `model`. The operator finds the ModelServer, takes a lock for it so two requests on one accelerator never interleave, and calls the adapter: `ready` puts the awake vLLM server to sleep and wakes the model's, `release` puts the model's vLLM server to sleep. A model already in the state asked for is left alone.

Every call path takes this step: the tool loop, evaluation, synthesis, and triage. A request that arrives while the same model is being woken waits on the server's lock, then finds the model awake and returns without waking it again.

The operator replies with a `ModelLifecycleReply` when the server reports the model in the state asked for, read from the server's own `/is_sleeping`, not when the wake was accepted. Waits are on that reported state; the 30 minute limits on the operator and on the agent's request are safety nets that the normal path never reaches. A failure (the model is unknown, a vLLM server cannot sleep, the vLLM server does not answer) is the reply, with the reason, and the call fails with it: the ticket is released and the failure counts against the server, so placement avoids it while it keeps failing. Only engines that put models to sleep take the request; Ollama loads on demand and is never asked.

An agent also asks without waiting when it is selected, so the wake overlaps its triage; the call's own request then returns at once. After a wake the operator publishes the server state straight away, so agents see the model awake without waiting for the next probe, and records how long the switch took, from the request to the ModelServer reporting the model awake (putting the awake model to sleep first is part of it).

That time is `switchCostMs` in the server state. Placement reads it for a hosted model that is asleep: the cost of choosing it is the observed switch over a typical call, not the load penalty used when no wake has been seen, and the learned policy plans with it in place of a calibrated load time. A switch puts the awake vLLM server to sleep whatever it is doing: a call already running on it is not protected from a switch another agent asks for, so several models on one ModelServer suit a crew whose calls for them do not overlap.

### Bounded retries and failure attribution

Two follow-on protections were essential once selection moved to call-time:

- **A single retry** on every client in `ChatModelPool`. LangChain4j's default retries would have silently retried a slow model call up to 3 times, multiplying the 120s Quarkus/Vertx HTTP timeout into a 6+ minute silent hang. With one retry, a timeout surfaces as a single failed call in 120s and the agent's failure-signal path takes over.
- **`ChatService.callWithToolLoop` bounded tool retries** via `ToolCallFailure`. Caps same-tool failures at 2 and total tool failures at 4 per loop iteration. When a tool consistently errors (MCP server down, etc.), the loop aborts early instead of grinding through 15 iterations of error responses.

When these protections trip, the agent publishes a **first-class `failure` consensus signal** (not a `stand_aside`) carrying `failureType` + `failedTool` + `lastError` metadata. See `agentic-consensus.md` for the consensus-protocol implications and `agent.md` for the runtime mechanics.

### Per-call attribution

Each `agree` / `concern` signal from a scheduled call carries `metadata.server = <server-name>` (the ModelServer the call actually ran against) and `metadata.model` (the model it ran, which differs from the preferred model when a warm candidate was used). When the call unloaded models to make room, `metadata.evicted` lists them. The dashboard's Agent Summary GPU column shows this real per-call attribution instead of the reconcile-time static label. Confirms emergent bin-packing visually: two co-triaged agents in the same discussion show as `ollama-a` and `ollama-b` when both have capacity, not both pointing at whatever the operator labeled them.

### Ticket claim and crash-safe release

The winning server's VRAM is reserved with a ticket so concurrent calls account for each other. Each call atomically claims one ticket in NATS KV bucket `kubemoot_server_tickets` before issuing the inference:

```
{ ticketId: UUID, server, modelFootprintMiB, holderId, expiresAt }
```

Multiple tickets coexist on one server as long as their footprints fit its free VRAM; that coexistence is the bin-pack. The contended object is the server's budget, not the unique ticketId, so a claim is written first and verified after: with its own ticket in the bucket, the claimer re-reads the server's tickets and releases its own when the VRAM budget no longer fits.

The budget counts each model once per server, at its largest ticket: an engine loads one copy of a model per server, so several tickets for the same model hold its memory once. This matters while concurrent first loads settle. Each claimer writes a ticket at the model's full footprint, and the ones that lose to an earlier load release theirs; until they do, counting every ticket would make the server look full and turn away calls joining the copy that is loading. A ticket that names no model counts on its own. The rule relies on engines that reserve a model's memory when they load it (Ollama's KV slab, vLLM's preallocated KV pool): a warm call's ticket marks a slot rather than adding memory, so warm calls on a server do not shrink the room a cold load sees. An engine that grows memory per request would need its adapter to report that growth.

A claim made for a free slot on a warm copy (`pickAndClaimWarm`, and `pickAndClaimRoom` joining a copy), resident or still loading, also re-counts the slots. NATS KV gives every write a revision, so the claimer counts the server's tickets written before its own and releases when no slot is left for it. Agents that read the same free slot at the same moment are ordered by revision, so no more of them keep the server than it has slots, and the rest move on to the next candidate: another in-tolerance model with a free slot or one that can load onto free memory, then the weighted cost. A claimer that loses every candidate re-reads the state and retries a few times before the selector reports no room.

Releases are guaranteed by two independent layers:

| Layer | Mechanism | What it covers |
|---|---|---|
| Happy path | `try { call } finally { delete ticket }` | Normal completion, exceptions, tool timeouts, graceful shutdown |
| Safety net | TTL on every ticket (= `callTimeout + 30s`) | Agent pod killed mid-call, JVM crash/OOMKill, NATS partition during release, node failure |

The `finally` delete is the fast reclaim (sub-second); the TTL is the unconditional reclaim, so no dead agent permanently owns VRAM and no reaper process is needed. Both layers must exist; either alone has a failure mode.

The same bucket holds a residency entry for each model a call left loaded, refreshed on every use, so other planners count the model before the server probe reports it. Its key is `residency__<server>__<model>` with both names encoded to legal NATS KV key characters (`qwen3:14b` is stored as `qwen3_3a14b`); the entry's `modelName` field holds the name itself. A residency write that fails is logged at WARN with its key and does not affect the call.

### Model footprint source

A model's occupancy is observed first, declared second:

1. **Ollama `/api/ps`** reports `size_vram` per loaded model (weights plus its KV slab). The operator probe writes the observed footprint into `ModelServer.status.capacity.loadedModelFootprints` and into NATS KV, caching the first-observed value. This KV-inclusive number is the occupancy the selector uses.
2. **`Model.spec.vramMib`** is an optional declared override on the Model CR, used for a model never loaded anywhere so the selector still has a number; it is replaced by the observed value the first time the model loads.

## Placement policy

The decision of where a call runs is a swappable policy inside the agent runtime. Every model call goes through it: tool-loop turns, advisory and synthesis calls, and triage. A triage call is short and waits on nothing, so it never unloads idle models to make room: with no room it runs on the agent's configured triage endpoint. Tickets, slot counting, and residency are shared by every policy, so two policies never overbook the same accelerator; only the decision changes.

| Policy | What it does |
|---|---|
| `algorithm` (default) | The placement order described above under JIT Per-Call Server Selection. |
| `learned` | Places each call where it is predicted to finish soonest (below). With no measured option it has no opinion and places as `algorithm` does, so selecting it is safe on a server that is not calibrated yet. |
| `shadow` | `algorithm` places every call; the learned policy's choice for the same call is logged beside it (INFO when the two differ, with the prediction) and counted in `kubemoot_scheduler_shadow_decisions_total` by `agreement` (`agree`, `differ`, `no_opinion`), once per placement attempt, so a call that waits for capacity counts each retry. Placement never depends on the learned policy. |

### The learned policy: predicted completion time

For each server that can take the call now, the learned policy predicts when the call
would finish:

- the wait for a slot, when the copy there is busy (from the calls in flight and the
  server's recent call time), plus, on a server that runs calls side by side, the
  slowdown from the calls it is already running (below);
- plus the load time, when the model is not warm there;
- plus the prompt tokens over the server's prompt speed for the model;
- plus the output the call is expected to generate over the server's generation speed.

It takes the soonest. A fast accelerator wins by default; a slower server, such as a
CPU, wins only when the fast ones are busy enough that it finishes first, and a long
prompt never goes to a slow server when a fast one finishes first. No rule names a
call type or a server.

The speeds come from each ModelServer's calibration and, once enough crew calls have
run alone on the server since (as many as the drift rule needs), their observed
generation speed replaces the calibrated one; the operator publishes both with the
server's live state (`modelSpeeds`). Observed speed counts only calls whose stream
delivered the tokens it timed (see Performance history), so a model that reasons out
of sight cannot make a server look several times faster than it is.

The expected output is what the agent's own calls of the same kind (`tool-turn`,
`simple` or `triage`) on the same model generated lately: the median of the last 16,
once there are at least 3, kept by the agent runtime and read back from its stored
history after a restart. Until then the call is planned at a default length. Every
option of a call is planned with the same length, so options compare by speed, and
the figure follows the agent: a triage call that produces a few hundred tokens and a
tool loop turn that produces a thousand are planned as such.

**Calls sharing a server.** An engine that batches, such as vLLM, takes a call at once
whatever else it is running, and each call then runs slower. The operator measures
this from the history: it divides each crew call's time by the time its tokens take at
the calibrated speeds, takes the median per number of calls on the server as the call
started (1, 2, 3 to 4, 5 to 8, 9 or more), and publishes each level's median over the
solo median as `loadFactors` in `modelSpeeds`, never below a lower level's factor. A
level needs at least 5 calls, and the solo calls it is compared with need 5 too. The
prediction multiplies the prompt and generation time by the factor of the level the
call would run at and counts the added time in the wait. A server with no free slot
makes the call wait instead, and the wait covers it.

The policy keeps the crew's model choice: it places the preferred model on its soonest
server, and considers an alternative only when the preferred model has no option at
all, so it changes where calls run, not which model answers. A server that has not
downloaded the model is not an option. It has no opinion, and `algorithm` places the
call, when:

- any server that has the model has not measured it (an unmeasured server is never
  guessed fast or slow, so a calibrated CPU never wins over an uncalibrated GPU);
- the soonest option has to wait for a slot (`algorithm` weighs queueing against
  unloading idle models, and queues across models);
- another call takes the chosen slot, or starts a load of the same model, first.

The learned policy claims only a free slot on a warm copy or a cold load onto a
server with a free slot. A cold load yields to a load of the same model already in
progress on any server, so concurrent calls still converge on one copy; a call
running on a resident copy is not a load, so a busy copy on a slow server never stops
a load on a faster one.

Set the realm default in KubemootConfig and override it per crew in its CrewSchedulingPolicy:

```yaml
# KubemootConfig
spec:
  defaults:
    scheduler:
      placementPolicy: shadow     # algorithm | learned | shadow
---
# CrewSchedulingPolicy
spec:
  crewRef: homelab-pilot
  placementPolicy: algorithm     # unset inherits the realm default
```

The operator resolves crew, then realm, then `algorithm`, and passes the result to each agent as `KUBEMOOT_SCHEDULER_PLACEMENT_POLICY`; changing it rolls the crew's agent pods. An unknown value falls back to `algorithm`. Run one policy per measurement arm: crews on different policies at the same time compete for the same accelerators.

## Performance history

Every scheduled model request is recorded so the scheduler can learn from what it observed instead of from configured values. The agent runtime writes the NATS KV bucket `kubemoot_scheduler_performance` (created by the operator's streams job, TTL 30 days); its schema is `SchedulerPerformance` in the bus contract.

- **One key per server, model, and agent:** `<server>.<model>.<agent>`, each token escaped. Each agent writes only its own key, so writes need no compare-and-set; a reader folds every agent's key for a server and model.
- **Each request records:** which path sent it (`tool-turn`, `simple`, `triage`), the outcome, whether the model was cold on the server when the call was placed, prompt and generated tokens, the duration the agent measured, the number of calls holding a ticket on the server when it started, and the call's timings. Triage, advisory, and synthesis calls are streamed and timed on any engine: the time to first token (reading the prompt, plus loading the model when the call was cold) and the generation time after it. Prompt time is recorded only for warm calls, and generation time only for replies of at least 8 tokens, so a cold load or a very short reply does not skew the history. A stream that delivers fewer than four fifths of the tokens the call generated carries a model that reasons out of the stream: its first delivered token comes after the reasoning, and the time after it covers only the answer's tokens. Such a call keeps its duration and token counts, and its prompt time, generation time and first-token time are `-1`. Token usage comes from the stream's final chunk. Other calls carry tokens and duration, and the timings are `-1`.
- **Each wait records:** how long a call waited for capacity before it was placed on that server.
- **Each prediction records:** a placement that predicted a completion time keeps that prediction beside what its first request then measured, as a `predictions` list (below).
- **Bounded:** each key keeps its last 32 requests, 16 waits and 32 predictions, with lifetime request and failure counts.
- **Never in the way:** writes run on one background thread with a bounded queue and are dropped when the queue is full or NATS is down; a request is never slowed or failed by recording it.

### Prediction error

Whichever policy places a call, the scheduler predicts when that call would finish on the server it chose, from the same speeds the learned policy uses, and compares the prediction with what the call measured. This is how you see whether the predictions get better as the history grows, and how the learned policy's predictions compare with the algorithm's placements on the same crew.

- **What is predicted:** the wait for a slot, the load, the prompt and the generation, and their sum, read before the call is claimed. A server whose speeds for the model are not measured has no prediction, so a call placed there is recorded but not scored.
- **What is scored:** the first request the placement sends, the one the prediction describes. A tool loop that takes several turns on the server counts its later requests but scores only the first. A request that failed is not scored. The measured time runs from sending the request to its reply, so a cold load inside the request is part of it.
- **The time to the first token** is scored too for the calls that stream (advisory, synthesis and triage calls), when the stream delivered its tokens: the predicted wait, load and prompt against the measured time to the first token. A tool loop turn is not streamed and a model that reasons out of the stream has no first token that means reading the prompt, so those calls show no first-token figure. Their total is still scored.
- **Where it is kept:** the `predictions` list of the `kubemoot_scheduler_performance` entry for the server and model, newest last, without the reason and the options so the entry stays small; and the discussion receipt, which keeps all of it (below).

The agents export it as Prometheus metrics, by `policy` (the policy that placed the call: `learned`, `algorithm` or `shadow`), `server`, `model` and `part` (`total` or `first_token`):

| Metric | What it is |
|---|---|
| `kubemoot_scheduler_prediction_error_seconds` | Absolute error in seconds, as a histogram. The mean error is `_sum` over `_count`. |
| `kubemoot_scheduler_prediction_relative_error` | The absolute error over the measured time, as a histogram. A value of 0.2 means the prediction was off by a fifth of the call's time. |

Plot `rate(..._sum[1h]) / rate(..._count[1h])` per server and model: a falling line is a prediction that improves with history. Compare it by `policy` to see how well the learned prediction describes the calls the algorithm placed.

### Reading a placement decision

Every model call the scheduler places is explained without reading logs. The coordinator lists the calls of a thread in `metadata.receipt.calls` of its `thread_close` message, the agents report theirs in `metadata.placementCalls` of their signals, and the dashboard and `kmctl conversation placements` show them. One call carries:

| Field | Meaning |
|---|---|
| `agent`, `phase` | Who made the call, and where in the thread: `advisory`, `selection` and `synthesis` are the coordinator's, `triage` is an agent deciding whether it has anything to add, `evaluation` is an agent's work on the question. |
| `model`, `server`, `coldStart` | Where it ran, and whether the model had to be loaded there. |
| `policy` | Who placed it. `learned`: the learned policy chose the server. `algorithm`: the algorithm placed it, because it is the configured policy or because the learned policy had no opinion. `shadow`: the algorithm placed it while the learned policy only recorded its choice. |
| `reason` | The policy's own explanation. For the learned policy, the predicted parts of the chosen server. For the algorithm, the fit explanation of the server it picked, the models it unloaded to make room and whether the call joined a busy copy; under `shadow` and when the learned policy deferred, also what the learned policy thought (it agrees, it would choose another server, its choice has to wait for a slot, or it has no opinion because a server's speeds are not measured). |
| `alternatives` | The other servers that could have run the model, then the other models the agent accepts on the servers that have them (named in `model`), with whether the model was loaded there, whether a slot was free, and their predicted completion time by part. The learned policy keeps the crew's model, so the other models are shown for comparison only. A server with no prediction was not measured. |
| `predicted` | The chosen server's predicted wait (the wait for a slot plus the slowdown from the calls already sharing the server), load, prompt, generation, time to the first token and total, in milliseconds. Absent when its speeds are not measured. |
| `actual` | What the first request measured: whether it completed, the wait for capacity before the call was placed, the time to the first token (streamed calls only), the total, the tokens in and out, and how many requests the placement sent. |
| `error` | The prediction against the measurement: the absolute and signed difference of the total in milliseconds (positive when the call took longer than predicted), and the absolute error over the measured time. For a streamed call, the same for the time to the first token. |

Read a row from the left: the reason says why this server, the alternatives say what else was weighed, and predicted against actual says whether the scheduler was right. The prediction is read just before the call is claimed, from the state of the servers then. A call planned at selection (the plan made so a load overlaps the agent's triage) starts its load and takes its slot at that point, so its prediction leaves out the wait and the load and says so in the reason; the measured time starts when the agent sends its first request.

Two limits to know. A call whose model has no measured speeds on the chosen server carries no prediction, so it appears in the list but not in the error metric. And a receipt written by agents that predate this record has no `calls`, which the dashboard and kmctl say plainly.

## How the scheduler thinks: one decision, four speeds

A placement (which server and model serve this inference call) is made on every call,
and one discussion makes dozens of calls. If a language model chose each placement,
every call would wait seconds for that choice, and the choice would itself be an
inference call needing a placement. So the thinking is split by how fast each decision
must be made, the way a network router works: routing protocols think about the network
and fill a table, and each packet is forwarded by a table lookup.

| Who | When it runs | Uses a model? | What it produces | Today |
|---|---|---|---|---|
| Placement code in each agent runtime | Every inference call, in microseconds | No | A pick: server and model | Runs: `algorithm` by default, or `learned` (predicted completion time) per crew |
| Calibration recipe (`accelerator-mcp`) | A new server, a reset, an engine change, newly bound models | No | `status.calibration`: what each model costs on each server | Runs, started by the operator |
| Drift check (operator) | Every server reconcile, once calibrated | No | `status.calibrationDrift`: models whose crew calls drifted from their calibration | Runs |
| Model server calibrator (scheduling crew) | When the operator reports a drift | Yes | A decision: recalibrate the drifted models now, or wait, with the evidence | Runs |
| Real-time scheduling agent | When the situation changes: a server joins, one is overloaded, new results arrive | Yes | The policy the placement code applies: weights, preferences, limits | Not built yet; the `learned` placement policy (predicted completion time) is the code it will tune |
| Post-analysis learning agent | After each settled discussion, from its receipt | Yes | Learned facts per model and server | Not built yet |
| Deep analysis agent | When the system is quiet | Yes | Tuning changes, with their evidence | Not built yet |

Models reason and write policy and data; code applies them on every call. The scheduling
crew's own calls are placed by the same code, so the agents that tune the scheduler never
depend on themselves to be scheduled.

## The scheduling crew

Kubemoot ships its own crew for scheduling work, `kubemoot-scheduling`, in the operator
chart (`schedulingCrew` values, on by default) and in its own namespace,
`kubemoot-scheduling`. It is built like any crew: a coordinator, a specialist, ADL
PromptModules, a Skill, and an MCPGateway. It ships no Models: its policy requires a model
that calls tools and prefers the measured-faster model in triage, and the
agents select it from the cluster's model catalog like any crew. Its tools come from `accelerator-mcp`, a core MCP
server in the operator's namespace (`acceleratorMcp` values) that the crew's gateway
registers through its `coreServerSelector` (see
[Core MCP servers](../reference/mcpgateway-guide.md#core-mcp-servers)); other crews may
register it too. It can write ModelServer status, so any crew that registers it can
record calibrations: the core opt-in is its only access boundary.

- **model-server-calibrator** (tooler) judges a calibration drift the operator reports:
  whether the cause is lasting (an engine or hardware change), so the drifted models are
  recalibrated, or passing (other load on the accelerator, a short burst), so the
  calibration stands. It calls the `accelerator-mcp` tools `list_servers`,
  `server_state`, `observed_performance`, and `calibrate_server`, following its
  `judge-a-drift` skill. Its answer is the record of the decision and its evidence.
- No prompt or skill names a server, model, or accelerator: what exists is read live
  with the tools.

The crew does scheduling work the operator hands it; it is not a crew to ask questions.
People read calibration where it is recorded: `kmctl model server list` and `kmctl model
server get NAME`, and the server page in the dashboard.

The crew renders when the chart declares at least one ModelServer, and it needs a model
that calls tools in the catalog: a vLLM model served by the `kubemoot-vllm` chart with the
`tool-calling` capability, or a model listed under the ModelServer's `models` in the
chart's `modelServers` values or one the Ollama server already has. Until the catalog has one,
its agents are `Unschedulable`, as any crew's would be.

### Calibration

Calibration measures each server before real requests arrive, so the first crew
question on a fresh install is not placed blind. Measuring is a fixed recipe, so no model
makes decisions in it:

1. The operator sees that a server needs calibrating: it is new, a reset was
   requested, its engine version changed, or Models were bound to it that were never
   measured.
2. Once the `accelerator-mcp` core server and the bus are ready, the operator calls its
   `calibrate_server` tool and records the request in the server's
   `status.calibrationRequest`, so it asks once per need.
3. The tool runs the recipe in the background, one model at a time: load, two short
   requests, a long prompt, and a parallel batch when the server runs requests
   together. It writes the results to the server's `status.calibration`. Each model is
   measured on its own and gets a result: measured, skipped as not applicable (an
   embedding model cannot generate, so it is never sent a request), or failed with its
   reason. One model's failure does not stop the others; the run is `Calibrated` when
   every model that chats was measured, `Failed` when one could not be.
4. If crew calls take the server, or the server stops mid-run, the run is recorded
   as `Waiting`; the operator starts it again once no call holds the server. A run
   that `Failed` is started again once the server has become ready after the
   failure.
5. A scheduler bucket that does not exist yet, or a NATS that cannot be reached, is a
   state to wait out, not a measurement result: a fresh install starts the operator and
   the NATS bootstrap job together. The operator does not start the run (the
   operator sets the `Calibrated` condition: `Unknown`, reason `WaitingForBus`, or `True`,
   reason `RecalibrationWaitingForBus`, when earlier results stand), and a run that meets it
   is recorded as `Waiting` with reason `WaitingForBus`, never `Failed`. Calibration
   writes tickets, so a missing bucket is not read as "no calls hold the server". The
   operator looks again every 30 seconds and continues as soon as both buckets exist. A
   run that `Failed` because the model could not be measured stays `Failed`.

This works before any crew runs, including the scheduling crew. The scheduler does not
read the results yet. Fields and the reset annotation are in the
[ModelServer reference](../reference/models.md).

### Drift

A calibration can stop being true while the hardware and engine look the same. Once a
server is calibrated, the operator compares the crew calls since then (the performance
history) with it. A model has drifted when its warm calls running alone on the server
generated, at the median over enough calls, far slower or far faster than the calibrated
speed; the exact rule is defined once, in `operator/pkg/perf`, and `observed_performance`
states it in its `driftRule`. A call runs alone when it was the only call holding the
server as it started. Calls running alongside others are left out of the rule, since
sharing the server slows each call.

On a drift the operator records it in the server's `status.calibrationDrift` and asks
the scheduling crew, once per drift and once its agents are ready, to judge it. A drift is
the set of drifted models against one calibration: another model drifting, or a drift
against a newer calibration, is a new drift and is asked about again. The
calibrator reads the evidence with `observed_performance` and either recalibrates the
drifted models or decides to wait. The drift clears when the speeds agree again or a new
calibration replaces the one they drifted from.

## Quality bias

### Purpose

`CrewSchedulingPolicy.spec.qualityBias` is a per-capability scalar in `[0.0, 1.0]` that biases model selection between speed (low values) and quality (high values) for `SchedulingRule` entries that state no explicit preference (no `prefer` list and no `preferFaster`). It lets crew authors express *"which kind of work benefits from quality and which benefits from speed"* once, in human terms, instead of hand-weighting selectors for every rule.

Agents declare abstract `capabilities` (e.g. `tool-calling`, `reasoning`); the crew author declares what each capability is worth in quality terms; the scheduler resolves to a concrete model from the [cluster's catalog](#selecting-from-the-model-catalog) at reconcile time. No agent CR names a model size - the same agent definition deploys against any cluster's models.

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
      # no prefer list and no preferFaster - scheduler auto-derives from qualityBias
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

### Auto-derived score from model size

When a rule has no explicit preference (no `prefer` list and no `preferFaster`) and the policy has a non-empty `qualityBias` map, the scheduler reads each candidate model's size class (the `kubemoot.ai/size` label every catalog entry carries) and contributes:

| Model size | Contribution to score |
|---|---|
| `large` | `round(bias × 100)` |
| `medium` | `round(100 - abs(bias - 0.5) × 200)`, a tent that peaks at a bias of `0.5` |
| `small` | `round((1 - bias) × 100)` |
| (no size) | `0` (no contribution) |

So `bias = 0.7` ranks `large (70) > medium (60) > small (30)`; `bias = 0.3` flips it to `small (70) > medium (60) > large (30)`. The tent makes the middle tier reachable: at a bias between about a third and two thirds it is the strict winner. On equal scores the Model with the smaller declared `vramMib` wins (a Model that declares none comes after those that do), then the Model name, so a tie never depends on how Models are spelled.

A crew that still ships its own Models (see [Crew-local Models](#crew-local-models-replace-the-catalog)) has Models that carry the older `latencyClass` label and no size. For those, `high` is read as large, `medium` as medium and `low` as small, with the same curves, so such a crew scores as it did (a Model that carries both labels is scored by its size).

### Abilities

Three of an agent's declared capabilities are also model abilities: `tool-calling`, `reasoning` and `vision`. For a crew that selects from the catalog, each ability the agent declares and the candidate model has adds `abilityWeight` points (default 20, `spec.abilityWeight` on the policy, 0 to 100) to the candidate. Twenty is above the runtime's quality tolerance of ten points, so an agent that declares `reasoning` keeps a reasoning model unless one with an equal score is already loaded. An ability is a preference and never a hard need: a hard need is what the rule's `require` says, and the crew scaffold writes `tool-calling` there. Any declared capability, ability or not (`kubernetes`, `observability`), also keys into `qualityBias`, as above. A rule with an explicit preference (a `prefer` list or `preferFaster`) gets neither the `qualityBias` score nor the ability score, so an explicit rule is exactly what it says.

### Override semantics

Explicit preferences on a rule always win. A rule that has a `prefer` list or sets `preferFaster` states its own preference, and the scheduler skips the `qualityBias` size score and the ability score for it. Crews with edge-case rules (e.g. "for this phase, force the qwen2.5 family") add a `prefer` block and that rule is unaffected by `qualityBias`. A rule that wants just the fastest model sets `preferFaster` and no `prefer`: only the measured speed ranks its candidates. A rule with both behaves as its `prefer` list plus the speed re-rank.

### Preference, not requirement

Capability declarations on Agents are **preference-shaping**, never hard gates. An agent declaring a capability that no model can satisfy still schedules against the best available model. This trades strict capability matching for observability: misconfigurations surface as visibly wrong model bindings on the dashboard, not as scheduling failures, and the system continues to operate.

### Example

A crew of one synthesizing coordinator plus several Toolers (and optionally Analysts), declaring capabilities thoughtfully, distributes naturally across model sizes:

| Agent role | Declared capabilities | effective_bias | Likely binding |
|---|---|---|---|
| Synthesizing coordinator | `[tool-calling, reasoning]` | max(0.3, 0.7) = 0.7 | `large` model |
| Tooler with light analytical scope | `[tool-calling, observability]` | max(0.3, 0.4) = 0.4 | `small` or `medium` model |
| Single-shape Tooler | `[tool-calling]` | 0.3 | `small` model |
| Analyst | `[reasoning]` | 0.7 | `medium` or `large` model |

The synthesizer holds the larger model because *it* declared `reasoning`. Toolers stay on smaller, faster models because they didn't. One heavy model and many light ones co-exist on the same GPU pool without slot starvation under per-server `num_parallel=1`.

## CRD Surface

Five CRs participate.

### Model - labeled

Labels declare what the model *is*; `spec.vramMib` declares what it *needs*; `spec.serverRef` declares where it *lives*.

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
  model: qwen3:32b              # the model identifier on the server
  serverRef: ollama-a       # where this Model is provisioned
  vramMib: 20480                # capacity request - used by the filter step
```

Labels are an open string set. New capabilities can be introduced without changing the CRD schema. The scheduler validates capability strings against the active `CrewSchedulingPolicy` references but does not constrain the vocabulary. The example above is a crew's own Model. A crew that selects from the cluster's catalog ships none: the cluster owner keeps the Models, and the labels a rule matches are the ones the [catalog](#the-model-catalog) produces.

### ModelServer - capacity-only

`status.capacity` is the source of truth for VRAM and parallelism.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
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
  models:                          # the server's part of the model catalog
    - name: qwen3:32b
      present: true
      size: large
      labels: {kubemoot.ai/size: large, capability/tool-calling: "true"}
```

#### The model catalog

`status.models` is the server's part of the cluster's model catalog. The ModelServer
reconciler builds it because it already probes the engine and already knows the Models that
name it: it merges what the engine reports about each model it has with the Model objects in
the ModelServer's namespace, and publishes the result on the server. One merge, one published
truth that tools and the dashboard read.

- **Membership.** Every chat model a server reports joins. A Model with `spec.disabled: true`
  removes its model; a model that only embeds is never admitted.
- **Precedence.** A Model object wins over discovery for every field it sets, because the owner
  is correcting or adding to a probe.
- **Facts from the engine.** Ollama answers `/api/show` once per model digest (parameter count,
  context window, abilities); vLLM reports the context window on `/v1/models`, and the
  parameters and abilities come from the `kubemoot-vllm` chart's annotation on its Service, so
  a vLLM model is declared once, where its server is started.
- **Size is a class, not a number.** Small is under 12 billion total parameters, medium is 12
  billion up to but not including 30 billion, large is 30 billion and over. The boundaries are
  constants so a class means the same on every cluster. Total parameters, not active ones: size
  stands for memory and quality tier, and speed is measured separately by calibration. A model
  whose size is unknown stays in the catalog, gets no size label, and is named on the
  server's `CatalogSizeKnown` condition. It contributes no size score, and a rule that
  requires a size excludes it.
- **Downloads.** A Model in the catalog is also a request to have the model on its server; the
  Model controller pulls it as it does for any Model, with no crew involved.
- **Calibration.** The calibration request covers every present catalog model, so a model the
  cluster adopted by discovery is measured too.

The label vocabulary the catalog produces is in the [model reference](../../reference/models/#the-model-catalog).

### Agent - capability-only

Agents declare capabilities and discussion role. No model name, no server, no GPU hint.

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
    server: ollama-a
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
            matchLabels: { kubemoot.ai/size: small }
      preferFaster: 50            # among the allowed models, favor the one measured faster
  placementPolicy: shadow        # optional; see Placement policy
  topologySpread:
    whenUnsatisfiable: ScheduleAnyway
    topologyKey: server
    phaseKeys: [mulling, triage]
```

`require` is hard (no match → Unschedulable). `prefer` is soft (weighted hint to the scorer). Mirrors `nodeSelector` vs `preferredDuringSchedulingIgnoredDuringExecution`.

#### Comparisons

A `LabelSelector` cannot compare numbers, so a rule has a second field of the standard Kubernetes `NodeSelectorRequirement` type, and a preference term has the same one:

```yaml
rules:
  - phase: mulling
    require:
      matchLabels: { capability/tool-calling: "true" }
    requireExpressions:               # ANDed with require
      - { key: kubemoot.ai/context-tokens, operator: Gt, values: ["16383"] }
    prefer:
      - weight: 100
        selector: { matchLabels: { kubemoot.ai/size: large } }
      - weight: 20
        expressions:                  # a term may use a selector, expressions, or both
          - { key: kubemoot.ai/context-tokens, operator: Gt, values: ["32767"] }
```

The operators are `In`, `NotIn`, `Exists`, `DoesNotExist`, `Gt` and `Lt`. `Gt` and `Lt` are strict and take one integer, so "at least 16K tokens" is `Gt 16383`. A model without the label fails a `Gt` or `Lt`. The admission webhook rejects an unknown operator, a `Gt` or `Lt` without exactly one integer value, `In` or `NotIn` without values, and `Exists` or `DoesNotExist` with values. A preference term needs a selector or expressions.

Speed is deliberately not a label a rule can name: an absolute threshold is a property of one cluster, and it would put the cluster back into the crew. `preferFaster: N` on a rule asks for the measured-faster model relatively instead (see [Preferring the faster model](#preferring-the-faster-model)).

#### Portable by default

A hard `require` should name only what the crew cannot work without: tool-calling, a context floor. Size and reasoning are wishes, so they belong in `prefer` or in `qualityBias`: a cluster with one small model still runs the crew, on that model. A crew that requires `kubemoot.ai/size: large` cannot be scheduled on that cluster, and the message names the missing label (see [Zero-Match Surface](#zero-match-surface)).

#### Conditions

The policy reports on its status where its crew gets its models:

| Condition | Meaning |
|---|---|
| `CatalogMode` | `Cluster` (status `True`) when the crew selects from the catalog; `CrewLocal` (status `False`) while the crew ships Models of its own |
| `LegacyLatencyClass` | `True` when a rule selects on the `latencyClass` label and the crew uses the catalog. The catalog carries no such label, so the rule matches nothing; select on `kubemoot.ai/size` |

### MootArchetype - coordination model + phase vocabulary

Cluster-scoped. Declares the scheduling phases (see the [reference](../../reference/mootarchetype/)) that `CrewSchedulingPolicy.spec.rules[*].phase` references. The operator chart installs `consent-3` (sociocracy 3.0), the one archetype that ships. The operator validates `CrewSchedulingPolicy` phase names against the archetype; the scheduler and the agent runtime use phases fixed in code, and the orchestration is fixed in code today (see the [Roadmap](../../introduction/roadmap/)).

## Scheduling Algorithm

Three stages, mirroring `kube-scheduler`'s structure.

### 1. Filter (feasibility)

A candidate `Model` is feasible iff:

- The Model's labels match `rule.require` (`matchLabels` and `matchExpressions`) and every `rule.requireExpressions`
- The Model's `serverRef` resolves to a Ready ModelServer
- The ModelServer has VRAM headroom: `Model.spec.vramMib + eviction_overhead <= ModelServer.status.capacity.vramTotalMiB`
- The ModelServer has parallelism headroom: `ModelServer.status.capacity.agentCount + 1 <= maxParallel * concurrency_factor`

Filter is pure. No side effects.

### 2. Score (rank feasible candidates)

Score is the sum of:

- `prefer` weights - for each `prefer` term whose selector and expressions match the candidate's labels, add `prefer.weight`
- **Preferred speed** - not scored here: `preferFaster` is applied by the runtime on each call (see [Preferring the faster model](#preferring-the-faster-model))
- **Quality bias and abilities** - the model-size score from `qualityBias` and the points for the abilities the agent declares (see [Quality bias](#quality-bias)), for a rule with no explicit preference (a `prefer` list or `preferFaster`)
- **ModelServer weight (phase-aware)** (for a crew on the model catalog, only a tie-break; see below) - `ModelServer.spec.scheduling.weight` (default 100) contributes per phase: mulling adds `+weight` (favors heavy servers); triage adds `-weight` (favors lighter servers). This lets two rigs carrying identically-labeled Models split phase work - a 5090 (`weight=100`) takes mulling, a 4090 (`weight=25`) takes triage - without forcing per-rig labels onto the Models.

For a crew that selects from the model catalog the order is lexicographic: the model the policy ranks higher wins whatever server carries it, and the server weight decides only among candidates with the same policy score (the same model on two servers, or models the policy ranks equally). A weight says which server to prefer, not how good a model is, and a weight set on one server is not comparable with the policy's points or with servers that set none. A crew that ships its own Models keeps the summed score.

The score reads only spec-level inputs. Live server state (loaded models, agent counts) never enters it, because the winner becomes the agent's default endpoint in the pod template and a pick that followed live state would roll the pod whenever a model loaded or unloaded. Where each inference call actually runs is decided per call by the runtime (see [JIT server selection](#jit-per-call-server-selection)).

### 3. Topology spread

If `topologySpread` is configured, distribute bindings so that no single `topologyKey` value (e.g., `server`) hosts multiple `phaseKeys` for the same discussion. `whenUnsatisfiable: ScheduleAnyway` falls back to the highest-scoring binding even if the spread is violated.

### 4. Bind

Write the chosen `(model, server, endpoint)` into `Agent.status.scheduling` and template the agent's Deployment env vars (`KUBEMOOT_MODEL_MODEL`, `KUBEMOOT_MODEL_ENDPOINT`, `KUBEMOOT_TRIAGE_MODEL_MODEL_ID`, `KUBEMOOT_TRIAGE_MODEL_ENDPOINT`) as the agent's stable default, plus the ranked candidate lists `KUBEMOOT_MODEL_CANDIDATES_MULLING` and `KUBEMOOT_MODEL_CANDIDATES_TRIAGE` described in [Candidate models per call](#candidate-models-per-call).

## Selecting from the model catalog

A crew ships no Model objects and names no server. Its Agents declare abilities, its `CrewSchedulingPolicy` states what each phase needs, and the cluster states what it has: the [model catalog](#the-model-catalog) every ModelServer publishes. The same crew chart deploys to a cluster with one small CPU model and to one with several accelerators, with the same bytes.

| Layer | Per-cluster? | Carries |
|---|---|---|
| The cluster's operator values | yes | the model servers, and the catalog Models the owner wants downloaded or labeled |
| The `kubemoot-vllm` chart values | yes | each vLLM model's parameters and abilities |
| The crew chart | no, same everywhere | Agents, `CrewSchedulingPolicy`, capability declarations |

Each time the scheduler picks for a phase it builds the candidate set from the catalog entries of the servers the crew can use: those in its own namespace and in the operator's. Only models the server has now are candidates, and not a model that [calibration](#calibration) found does not fit the server's memory. A vLLM model whose server is restarting stays a candidate, and a server that is briefly not ready does not re-bind its agents: readiness is judged again at the next pick and, for each call, by the runtime. Each entry is turned into an in-memory Model value, never stored, so one scorer serves the catalog and a crew's own Models. A server's catalog change (a model added, removed or relabeled, or the server deleted) re-evaluates the Agents that can use the server, so bindings follow the cluster with no edit in any crew's namespace. The score reads only labels and declared weights, so the Deployment does not change while models load and unload.

Switching a model server from Ollama to vLLM, adding an accelerator, or moving a model to another accelerator changes the catalog and nothing in the crew's namespace. A catalog change that changes which models a phase may use changes the candidate list in the environment of the affected Agents, which rolls their pods; the crew definition is untouched.

### Crew-local Models replace the catalog

A crew that has any Model in its own namespace keeps using exactly those Models and only those, until it deletes them. Replacing rather than merging keeps an unmigrated crew's behavior, and its measured results, the same on the day the catalog exists, and makes migration one act: delete the crew's Models. The `CatalogMode` condition on the crew's policy says which mode it is in, so a crew that is still coupled to its cluster shows in `kubectl get csp -o yaml`. Models in the operator's namespace are the catalog's, not a crew's, so a crew that runs in the operator's namespace selects from the catalog too. The ability score applies only in catalog mode.

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

Against the catalog the message says what the catalog holds and which constraint nothing met, for example `no model in the cluster catalog (3 available) satisfies the require of phase=mulling (kubemoot.ai/size=large); 1 model(s) have no known size (set spec.parameters on a Model)`. An empty catalog says that no server in the crew's namespace or the operator's reports a model.

No LLM call is attempted. The discussion either degrades to a documented fallback (e.g., the coordinator runs a single-agent path) or surfaces the failure to the user.

## Future direction - per-discussion NATS binding

Today, binding happens in the agent reconciler and is applied via the Deployment template. The same filter + score + bind algorithm could run inside a NATS-subscribing scheduler component, publishing bindings per discussion phase on `kubemoot.scheduling.<threadId>.<phase>` and letting the agent runtime resolve its endpoint per request. That decouples pod identity from routing entirely (no pod restart on rebind) and lets the scheduler re-evaluate as capacity changes mid-discussion.

This becomes valuable when capacity changes within a discussion (a new ModelServer joins, a ModelServer drops, a Model evicts under VRAM pressure). It is not required in a deployment where cluster capacity is static.

## Related

- [agent.md](../reference/agent.md) - Agent CRD spec and operational guide
- [models.md](../reference/models.md) - Model catalog
- [agentic-consensus.md](agentic-consensus.md) - discussion phase semantics
- `chart/kubemoot-operator/templates/mootarchetypes.yaml` - built-in `consent-3` MootArchetype
