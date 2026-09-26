---
title: "Kubemoot Scheduler"
weight: 5
---

> **Audience:** operator and agent-runtime developers; CrewForge authors; open-source consumers.

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
- The **Agent reconciler** computes the candidate `(Model, Provider)` matches via the filter and score machinery described above and records them on the Agent; the per-call winner among those candidates is chosen at the inference boundary.

The agent-runtime grows two new classes:

#### ProviderSelector (agent-side)

`agent-runtime/src/main/java/ai/kubemoot/agent/provider/ProviderSelector.java`

On every call, `ProviderSelector.pickAndClaim(modelName, occupancy)` reads all candidate states from the `kubemoot_provider_state` KV bucket (a 200ms in-memory cache absorbs Tooler fan-out bursts), then runs the feasibility filter and weighted cost below and claims a VRAM ticket against the winner. It returns empty only when no provider is feasible or NATS is unavailable, in which case the caller falls back to its static Quarkus-injected ChatModel.

#### Model occupancy (counted once, KV-inclusive)

A model's VRAM occupancy is fixed when it loads: weights plus the context KV slab (`num_ctx × num_parallel`). Ollama reserves the whole slab at load, not per call, so there is no separate per-call KV term. `occupancy(M)` is the observed `/api/ps` footprint of M (KV-inclusive) from any provider that has loaded it, falling back to its on-disk size plus a slab estimate when it has never been loaded anywhere.

#### The one hard filter

A provider is feasible for model M iff it is ready, its circuit is closed, and `usable(P) ≥ occupancy(M)`, where `usable(P) = totalVram(P) − reserve` (the reserve covers CUDA context, fragmentation, and runtime overhead). This asks only whether M can physically fit the card at all, after any eviction Ollama would perform. A model larger than every card's usable VRAM yields no feasible provider, and the runtime surfaces that rather than spilling silently. Whether a load would evict a resident model is not a feasibility question; it is an eviction cost in the score.

#### The weighted cost (lowest wins)

For each feasible provider the runtime sums these weights and picks the minimum:

| Weight | Meaning | Zero when |
|---|---|---|
| contention | in-flight calls / `maxParallel` | provider idle |
| reliability | learned recent failure rate | provider healthy |
| spill | resident set already exceeds usable VRAM (running on CPU) | not spilling |
| load | cost to load M (scales with its weights) | M already warm here |
| best-fit | excess capacity `(usable − occupancy)/occupancy` - reserves big GPUs for big models so a small model does not squat a big card; sized to dominate contention | the model fits the card tightly |
| eviction | cost to evict resident models to fit M; near-infinite if a victim has in-flight (live) work | warm, or `free(P) ≥ occupancy(M)` |

where `free(P) = usable(P) − Σ resident footprints`. `CrewSchedulingPolicy` require/prefer biases fold in as additional weight terms. There are no categorical tiers; the ordering of warm, cold-onto-free, and eviction emerges from the weights.

#### What falls out

- Reusing a warm model costs ~0 (no load, no new VRAM, regardless of model size), so it almost always wins: by the cost, not by a rule.
- A model is never duplicated or migrated just to dodge a busy slot. Queueing at a warm copy (contention weight) is cheaper than a fresh load (load weight) until the queue is genuinely long, at which point spreading a second copy onto an idle card becomes cheaper and the runtime does it.
- A cold load lands on a card with free room; evicting a resident model is far costlier, and evicting one with live work is effectively forbidden, so distinct models partition across cards while the same model concentrates on its home.
- The aggregate distribution is emergent, like `kube-scheduler` bin-packing Nodes: no anti-affinity rules, no sticky bindings, no rebalancer.

#### ChatModelPool

`agent-runtime/src/main/java/ai/kubemoot/agent/provider/ChatModelPool.java`

The Quarkus-injected `ChatModel` is fixed to one base-url at startup - wrong shape for JIT selection. `ChatModelPool` is a `Map<endpoint, OllamaChatModel>` built lazily on first use. Each ChatModel reuses its underlying HttpClient. New endpoints get a new instance; existing endpoints reuse. Built with `.maxRetries(1)` - see [Bounded retries](#bounded-retries-and-failure-attribution) below.

### Bounded retries and failure attribution

Two follow-on protections were essential once selection moved to call-time:

- **`OllamaChatModel.builder().maxRetries(1)`** in `ChatModelPool`. LangChain4j's default `RetryUtils` would have silently retried a slow Ollama call up to 3 times, multiplying the 120s Quarkus/Vertx HTTP timeout into a 6+ minute silent hang. With maxRetries(1), a timeout surfaces as a single failed call in 120s and the agent's failure-signal path takes over.
- **`ChatService.callWithToolLoop` bounded tool retries** via `ToolCallFailure`. Caps same-tool failures at 2 and total tool failures at 4 per loop iteration. When a tool consistently errors (MCP server down, etc.), the loop aborts early instead of grinding through 15 iterations of error responses.

When these protections trip, the agent publishes a **first-class `failure` consensus signal** (not a `stand_aside`) carrying `failureType` + `failedTool` + `lastError` metadata. See `agentic-consensus.md` for the consensus-protocol implications and `agent.md` for the runtime mechanics.

### Per-call attribution

Each `agree` / `concern` / `failure` signal carries `metadata.provider = <provider-name>` - the ModelProvider the call actually ran against. The dashboard's Agent Summary GPU column shows this real per-call attribution instead of the reconcile-time static label. Confirms emergent bin-packing visually: two co-triaged agents in the same discussion show as `ollama-a` and `ollama-b` when both have capacity, not both pointing at whatever the operator labeled them.

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

So `bias = 0.7` ranks `high (70) > medium (50) > low (30)`; `bias = 0.3` flips it to `low (70) > medium (50) > high (30)`. Existing tie-break (smallest model name) still applies.

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

### What gets deleted later

The sticky/capacity-aware/per-phase-count machinery exists only to compensate for static binding. Once the operator hands each agent a candidate list instead of a single binding, all of it deletes:

- `applySticky` and `stickyHysteresis` in `agent_controller.go`
- The load-penalty branch in `scoreCandidate`
- `countAssignedAgents` and `MullingAgentCount`/`TriageAgentCount` in `modelprovider_controller.go`
- The deployment-hash short-circuit handling for image-only changes (existed to work around env-var-change rollout fragility)

Net code reduction is expected to be several hundred lines.

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

Cluster-scoped. Declares the phase vocabulary that `CrewSchedulingPolicy.spec.rules[*].phase` references. The operator chart installs `consent-3` (sociocracy 3.0) by default. Additional archetypes (Robert's Rules, debate, expert panel) can be added without touching the scheduler.

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
- **Capacity score** - bias toward providers with more free VRAM (`spread`) or less free VRAM (`binpack`), configurable via `KubemootConfig.scheduler.strategy`
- **Image-locality bonus** - if the model is already loaded on the provider (`status.capacity.loadedModels`), add a fixed bonus (matches kube-scheduler's `ImageLocalityPriority`)
- **Provider weight (phase-aware)** - `ModelProvider.spec.scheduling.weight` (default 100) contributes per phase: mulling adds `+weight` (favors heavy providers); triage adds `-weight` (favors lighter providers). This lets two rigs carrying identically-labeled Models split phase work - a 5090 (`weight=100`) takes mulling, a 4090 (`weight=25`) takes triage - without forcing per-rig labels onto the Models.

### 3. Topology spread

If `topologySpread` is configured, distribute bindings so that no single `topologyKey` value (e.g., `provider`) hosts multiple `phaseKeys` for the same discussion. `whenUnsatisfiable: ScheduleAnyway` falls back to the highest-scoring binding even if the spread is violated.

### 4. Bind

Write the chosen `(model, provider, endpoint)` into `Agent.status.scheduling` and template the agent's Deployment env vars (`KUBEMOOT_MODEL_MODEL`, `KUBEMOOT_MODEL_ENDPOINT`, `KUBEMOOT_TRIAGE_MODEL_MODEL_ID`, `KUBEMOOT_TRIAGE_MODEL_ENDPOINT`).

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
