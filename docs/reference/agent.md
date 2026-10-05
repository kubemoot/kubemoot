---
title: "Agent CRD"
weight: 1
---

## Overview

The Agent CRD declares an LLM-powered agent as a thin, capability-only resource and deploys it as a Kubernetes-native service. The Agent spec carries no model name, no provider, and no GPU hint. Agents declare *what they can do*; the scheduler matches them to a `(model, provider, endpoint)` via `CrewSchedulingPolicy` rules over labeled `Model` CRs.

Agents orchestrate:
- **Models**: LLM inference resolved by the scheduler from `spec.capabilities` + `CrewSchedulingPolicy` (see [scheduler.md](../architecture/scheduler.md))
- **Knowledge**: RAG retrieval via `spec.ragSources`
- **Tools**: MCP tool execution via the MCPGateway, filtered by `spec.enabledTools` / `spec.disabledTools`
- **Collaboration**: Multi-agent discussions via NATS JetStream, configured by `spec.discussRole` / `spec.discussChannels` / `spec.discussKeywords`
- **Prompts**: System prompts composed from `PromptModule` CRs referenced by `spec.promptRefs`

## Supported Types

| Type | Description |
|------|-------------|
| `chat` | Conversational agent with HTTP API |
| `task` | Task-oriented agent (planned) |
| `workflow` | Multi-step workflow orchestration (planned) |

---

## Spec Fields

### `spec.type`
Agent type. Default: `chat`. Only `chat` is implemented today.

### `spec.description`
Human-readable description of the agent's purpose. Shown in the dashboard. Keep it factual and short - one sentence.

### `spec.capabilities[]`
Required. Open-string label set declaring what this agent can do. Matched by `CrewSchedulingPolicy.spec.rules[].require.matchLabels` (as `capability/<name>: "true"`) against the labels on candidate `Model` CRs.

Common values: `tool-calling`, `reasoning`, `kubernetes`, `proxmox`, `observability`, `harbor`, `git`, `helm`, `web-search`.

### `spec.discussRole`
Role this agent plays in a discussion thread.

| Value | Behavior |
|-------|----------|
| `generic` (default) | General contributor. Participates in discussions but does not hold the Tooler raw-output contract. Use when an agent is not a domain Tooler, Analyst, coordinator, or researcher. |
| `tooler` | **Tooler** role: subscribes to channels, calls MCP tools in the EVALUATING phase, participates with signals (`agree`, `concern`, `stand_aside`, `failure`, `block`). Thinking is OFF for deterministic tool selection. |
| `analyst` | **Analyst** role: participates in the REVIEW phase with thinking ON. Carries RAG sources and reasons over the data Toolers gathered. No live MCP tools. |
| `coordinator` | Receives user queries, generates advisory, runs triage, synthesizes responses. `KUBEMOOT_DISCUSS_COORDINATOR=true`, `KUBEMOOT_DISCUSS_TOOLER=false` |
| `researcher` | Contributes to synthesis but excluded from settle triggers, fast path, and gap detection. Used for non-settle-gating agents such as internet search that augment the discussion without owning a definitive answer. |

### `spec.discussChannels[]`
NATS discussion channels the agent subscribes to. The coordinator should list every channel the crew uses (e.g., `[kubernetes, observability, proxmox, general]`); a Tooler or Analyst lists only its domain (e.g., `[kubernetes]`). Channels are metadata, not routing gates - subcommittee selection is the coordinator's job (via the resume model), not channel routing.

### `spec.discussKeywords[]`
Optional. Domain keywords that become part of this agent's **resume** (alongside its description, tools, and role). The coordinator's resume model embeds the resume and matches it semantically against each question to pick the subcommittee, so keywords inform selection without being a literal gate. Set to `["*"]` to opt into every thread (used by researcher agents like internet-search).

### `spec.promptRefs[]`
Required. Ordered list of `PromptModule` names whose `content` is concatenated by `order` to form the agent's system prompt. The composed text is written to a ConfigMap and mounted at `/etc/kubemoot/policy/system.txt`. All prompt text MUST live in PromptModules; inline system prompts are not supported.

See the [PromptModule](#promptmodule) section below for the order-range conventions.

### `spec.triageSummary`
Optional short description (≤200 chars) the coordinator's triage LLM call sees when selecting participants. Distinct from `spec.description` (for humans); this string is in the triage prompt and should be optimized for the LLM's pattern matching.

### `spec.enabledTools[]` / `spec.disabledTools[]`
Optional. Whitelist / blacklist of MCP tool names. Resolved by the agent runtime after MCPGateway tool discovery. If both set, `enabledTools` takes precedence.

Set deliberately small. `qwen3:32b` reliably handles ~10-15 tools; beyond that, tool-call reliability drops sharply. The operator injects the resolved list as `KUBEMOOT_ENABLED_TOOLS` / `KUBEMOOT_DISABLED_TOOLS`.

Scheduling tools (`set_reminder`, `schedule_followup`, `list_scheduled`, `cancel_scheduled`) are provided by the `scheduling-mcp` MCP server, see [Agent Self-Scheduling](../architecture/scheduling.md). Add a dedicated Tooler Agent CR for them rather than enabling on every agent.

### `spec.ragSources[]`
Optional. RAGSource references for knowledge retrieval.

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | RAGSource resource name |
| `topK` | int32 | Results to retrieve. Default: 5 - keep low; large RAG payloads degrade tool-calling |
| `priority` | int32 | Higher = retrieved first. Default: 0 |

### `spec.temperature`
String representation of a float (0.0-2.0). Default: `"0.3"` - required for reliable tool selection. The operator writes this to the agent's `application.properties` (Quarkus `BUILD_AND_RUN_TIME_FIXED` constraint; not overridable via env).

### `spec.maxTokens`
Max generation length. Default: 4096. Coordinator typically uses 8192 to fit synthesis output.

### `spec.deployment`
Standard Kubernetes deployment overrides.

| Field | Type | Description |
|-------|------|-------------|
| `replicas` | int32 | Default: 1 |
| `image` | string | Override agent-runtime image. Defaults to `KubemootConfig.spec.images.agentRuntime` |
| `port` | int32 | HTTP API port. Default: 8080 |
| `resources` | ResourceRequirements | CPU/memory limits |
| `env` | []EnvVar | Additional env vars - useful for `KUBEMOOT_GATEWAY_ENABLED=false` on coordinator, longer timeouts, etc. |
| `serviceAccountName` | string | Pod service account |

The operator sets `imagePullSecrets` from `KubemootConfig.spec.defaults.imagePullSecrets` - no per-agent override needed. That field is empty by default (public images need no pull secret); set it on KubemootConfig if you point `global.imageRegistry` at a private mirror.

---

## Status Fields

| Field | Type | Description |
|-------|------|-------------|
| `phase` | string | `Running` once the Deployment and Service are applied (not a pod-readiness signal); `Unschedulable` when no feasible Model or Ready ModelProvider exists |
| `ready` | bool | True when the scheduler bound a Model and the agent is deployed |
| `endpoint` | string | Internal service URL (`http://<name>.<namespace>.svc.cluster.local:<port>`, following `spec.deployment.port`) |
| `availableReplicas` | int32 | Declared, not populated by the controller today |
| `message` | string | The bound models (`mulling=<model>@<provider>; triage=<model>@<provider>`), or the reason the agent is unschedulable |
| `ragSourceStatus[]` | RAGSourceRefStatus | Declared, not populated by the controller today |
| `mcpServerStatus[]` | MCPServerRefStatus | Declared, not populated by the controller today |

---

## Examples

### Coordinator

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: homelab-coordinator
  namespace: crew-homelab-pilot
  labels:
    kubemoot.ai/crew: homelab-pilot
    kubemoot.ai/role: coordinator
spec:
  type: chat
  description: "Homelab coordinator: orchestrates Tooler and Analyst agents via async NATS discussions with vector-based triage pre-filtering"
  discussRole: coordinator
  capabilities:
    - tool-calling
    - reasoning
  discussChannels:
    - kubernetes
    - observability
    - proxmox
    - general
  promptRefs:
    - advisory-prompt
    - coordinator-decision-logic
    - synthesis-prompt
  temperature: "0.1"
  maxTokens: 4096
  deployment:
    replicas: 1
    port: 8080
    env:
      - name: KUBEMOOT_GATEWAY_ENABLED   # coordinator has no MCP tools - only A2A delegation
        value: "false"
      - name: KUBEMOOT_DISCUSS_TOOLER # coordinator facilitates, doesn't participate
        value: "false"
      - name: KUBEMOOT_DISCUSS_SYNTHESIS_TIMEOUT_SECONDS
        value: "180"
```

### Kubernetes Tooler

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: k8s-workloads
  namespace: crew-homelab-pilot
  labels:
    kubemoot.ai/crew: homelab-pilot
spec:
  type: chat
  description: "Kubernetes workload Tooler: pods, deployments, services, replicasets, statefulsets, daemonsets, jobs, cronjobs"
  triageSummary: "Pod, deployment, service, replicaset, statefulset, daemonset, job, cronjob, namespace management via kubectl"
  discussRole: tooler
  capabilities:
    - tool-calling
    - kubernetes
  discussChannels:
    - kubernetes
  discussKeywords:
    - pod
    - deployment
    - service
    - replicaset
    - statefulset
    - daemonset
    - job
    - cronjob
    - namespace
  promptRefs:
    - discussion-protocol
    - response-style
    - k8s-workloads-system
  enabledTools:
    - kubectl_get
    - kubectl_describe
    - kubectl_logs
    - list_api_resources
    - explain_resource
    - kubectl_scale
  ragSources:
    - name: kubernetes-concepts
      topK: 3
      priority: 1
  temperature: "0.3"
  maxTokens: 4096
```

### Researcher (web search)

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: web-search
  namespace: crew-homelab-pilot
spec:
  type: chat
  description: "Internet search researcher - augments discussion with current information"
  triageSummary: "Web search for current events, recent docs, external context"
  discussRole: researcher
  capabilities:
    - tool-calling
    - web-search
  discussChannels:
    - general
    - kubernetes
    - observability
    - proxmox
  discussKeywords:
    - "*"   # opt into every thread
  promptRefs:
    - discussion-protocol
    - response-style
    - web-search-system
  enabledTools:
    - web_search
    - fetch_url
  temperature: "0.3"
```

---

## Multi-Agent Collaboration

Kubemoot agents collaborate asynchronously via NATS JetStream discussions.

### Agent Topology

A typical deployment uses a coordinator + Tooler (+ optional Analyst) pattern:

```
                    +--------------+
                    |  coordinator |  <- Receives user queries
                    +------+-------+
                           | NATS discussions
            +--------------+--------------+
            |              |              |
    +-------v------+ +----v-----+ +------v-------+
    | k8s-workloads| | k8s-nodes| |  k8s-helm    |  <- Toolers, kubernetes channel
    +--------------+ +----------+ +--------------+
    +--------------+ +--------------+
    |  obs-metrics | |proxmox-nodes |  <- Toolers, observability / proxmox channels
    +--------------+ +--------------+
    +--------------+
    |  k8s-analyst |  <- Analyst, REVIEW phase, kubernetes channel
    +--------------+
```

### Discussion Channels

| Channel | Purpose | Subscribing Agents |
|---------|---------|-------------------|
| `kubernetes` | K8s workload, node, and Helm queries | k8s-workloads, k8s-nodes, k8s-helm (Toolers); k8s-analyst (Analyst) |
| `observability` | Metrics and monitoring queries | obs-metrics (Tooler) |
| `proxmox` | Hypervisor and VM queries | proxmox-nodes (Tooler) |
| `general` | Catch-all for unclassified queries | All Toolers and Analysts |

Each agent declares its channels via `Agent.spec.discussChannels`. Coordinators typically list every channel the crew uses; Toolers and Analysts list only their domain. See [agentic-consensus.md](../architecture/agentic-consensus.md) for the full discussion protocol.

### Discussion Flow

1. User sends query to coordinator agent
2. Coordinator queries the crew's **resume model** to select the subcommittee, then publishes `thread_start` (with that `innerCircle`) to `kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>`
3. Only the selected Toolers are woken; the rest are silently excluded (no keyword self-selection)
4. Each selected Tooler runs a per-agent LLM triage (CONTRIBUTE / NOTHING_TO_ADD) for this specific question
5. Contributing Toolers call `directChat()` to generate a contribution (with tool calling); Analysts self-select in the REVIEW phase and reason over the gathered data
6. Agents publish signals (`triaging`, `evaluating`, `waiting`, `agree`, `concern`, `stand_aside`, `failure`, `block`) back to the thread
7. Coordinator settles the discussion based on signals (not a fixed timeout) and synthesizes a response

### Bounded Tool Retries and Failure Signals

The tool-calling loop is bounded along three axes so a misbehaving tool can't hang the discussion forever:

- **Same-tool failure cap** (2): if any individual tool returns an error result twice in the same evaluation, the loop aborts.
- **Total-failure cap** (4): if 4 tool calls across distinct tools all error, the loop aborts - this catches a model spreading its retries across many broken tools rather than one.
- **Iteration cap** (`properties.model().maxToolIterations()`, default 15): if the LLM keeps requesting tools without producing a final answer, the loop aborts.

Any of these aborts publishes a first-class `failure` consensus signal with structured cause metadata (`failureType`, `failedTool`, `lastError`) rather than a generic `stand_aside`. Other failures during mulling (a provider timeout, an out-of-memory error, a messaging hiccup, a runtime error) route through the same failure path with heuristic classification (`model_timeout`, `model_oom`, `provider_unreachable`, `internal_exception`).

Before every turn the loop also checks that the next prompt fits the context window of the provider the call runs on. It uses the prompt size the engine reported for the previous turn plus the tool results added since. When the prompt would not fit, the loop fails with `failureType` `CONTEXT_EXCEEDED` rather than send a prompt the engine would cut. See [Scheduler](../architecture/scheduler.md#the-context-window-is-a-hard-constraint).

A bounded retry policy on the model call itself keeps a slow provider from silently multiplying its own timeout into a multi-minute hang: one retry, then the call surfaces as failed and the failure path takes over. Heartbeats stop the moment an agent publishes any terminal signal, so a finished agent never keeps looking alive after it's done.

The consensus-protocol semantics of `failure` vs `stand_aside` and the three-tier gap classification (`TOOL_GAP` / `INFRASTRUCTURE_GAP` / `SPECIALIST_GAP`) are documented in [agentic-consensus.md](../architecture/agentic-consensus.md#failure-as-a-first-class-signal).

### JIT Provider Selection (Per-Call)

The agent runtime does not use a single, static endpoint frozen into env vars at reconcile time. Each mulling inference call picks the freest provider at the call boundary from live provider state (read from a shared NATS KV bucket), and falls back to the static reconcile-time endpoint only when that live state is unavailable. The picked provider's name flows through to the agent's signal metadata as `metadata.provider`, surfacing in the dashboard's Agent Summary GPU column as the call's real placement. See [scheduler.md](../architecture/scheduler.md#jit-per-call-provider-selection) for the architectural rationale and the `kube-scheduler` analogy.

The call's model is chosen per call too. The operator publishes the phase's ranked candidate models (`KUBEMOOT_MODEL_CANDIDATES_MULLING`); the runtime uses the preferred model when it is loaded with a free slot, otherwise a loaded candidate within the quality tolerance, otherwise the preferred model under the normal cost ranking. The model the call ran is recorded as `metadata.model`.

When no GPU has room, the agent publishes `waiting` once and retries on every provider-state or ticket change until a GPU frees up. It stands aside with `metadata.reason = "gpu-busy"` if the discussion ends or the wait's safety limit passes first, and with `"model-too-large"` at once when no GPU can ever hold the model. It stands aside with `"prompt-too-large"` at once when no GPU gives an acceptable model a context that holds the prompt. When no GPU has room, the scheduler compares waiting for a loaded copy with unloading idle models that no crew is using or about to use; a call that unloaded models records them as `metadata.evicted`. An agent's first call is planned when the coordinator selects it, so a needed load overlaps triage. See [Models & Scheduling](../concepts/models-and-scheduling.md#when-every-gpu-is-busy).

### Rate Limiting

- Max 1 contribution per thread per agent, plus a sliding-window per-minute cap
- Prevents runaway inference costs from chatty discussion threads
- Configured via `KUBEMOOT_DISCUSS_MAX_INFERENCES_PER_MINUTE` env var

### Liveness Heartbeat

Each agent writes a liveness record to the `kubemoot_agent_state` NATS KV bucket, keyed `<namespace>.<agent>`, every `intervalSeconds` (default 60). The entry expires from the bucket after 300 seconds, at which point there is no heartbeat at all.

| Field | Meaning |
|---|---|
| `agent` | Agent name |
| `timestamp` | When the record was written (ISO 8601) |
| `nats`, `ollama` | Whether the agent reached NATS and its model provider |
| `model` | Model the agent is configured with |
| `lastInference` | Time of the last successful inference (omitted until the first one) |
| `intervalSeconds` | Seconds between this agent's heartbeats |

Consumers judge staleness from the record itself: a heartbeat is stale once two of its own `intervalSeconds` pass without a newer one, so changing the interval needs no change in any consumer. A record without `intervalSeconds` (written by an older agent), or one whose value is not a positive number or exceeds one hour, is judged against a 60 second interval. An agent configured with a zero or negative interval publishes 60.

### Operator-Injected Env Vars

| Env Var | Applies To | Source |
|---------|-----------|--------|
| `NATS_URL` | All agents | Operator from NATS service |
| `KUBEMOOT_DISCUSS_CHANNELS` | All agents | `Agent.spec.discussChannels` (comma-separated) |
| `KUBEMOOT_DISCUSS_COORDINATOR` | Coordinators | Set `true` when `discussRole=coordinator` |
| `KUBEMOOT_DISCUSS_TOOLER` | All agents | `false` for coordinators and researcher-role agents |
| `KUBEMOOT_DISCUSS_TIMEOUT_SECONDS` | Coordinator | Optional override via `spec.deployment.env` |
| `KUBEMOOT_MODEL_CANDIDATES_MULLING` | All agents | Ranked candidate models for the mulling phase, JSON `[{"model", "score"}]`, preferred model first |
| `KUBEMOOT_MODEL_CANDIDATES_TRIAGE` | All agents | Ranked candidate models for the triage phase, same shape |
| `KUBEMOOT_MODEL_CANDIDATE_TOLERANCE` | All agents | Optional override via `spec.deployment.env`; quality-score points a warm candidate may trail the preferred model (default 10) |
| `KUBEMOOT_DISCUSS_GPU_WAIT_LIMIT_SECONDS` | All agents | Optional override via `spec.deployment.env`; safety limit on one GPU capacity wait (default: the synthesis timeout, 90 seconds) |

---

## Tool Filtering

Agents connect to MCP tools via the MCPGateway. The number of registered tools significantly affects model behavior - qwen3:32b works reliably with ~10-15 tools but produces empty responses with >20 tools.

### Spec Fields

`Agent.spec.enabledTools[]` is a whitelist; `Agent.spec.disabledTools[]` is a blacklist. The operator injects the resolved set as `KUBEMOOT_ENABLED_TOOLS` / `KUBEMOOT_DISABLED_TOOLS`. If both are set, `enabledTools` takes precedence.

### Tool Categories

| Category | Tools | Use Case |
|----------|-------|----------|
| **Read** | `kubectl_get`, `kubectl_describe`, `kubectl_logs`, `list_api_resources`, `explain_resource` | Cluster inspection |
| **Update** | `kubectl_scale`, `kubectl_patch`, `kubectl_rollout` | Workload management |
| **Helm** | `helm_install`, `helm_upgrade`, `helm_uninstall` | Chart management |

### Guidance

- **Temperature 0.3** (set via `Agent.spec.temperature`) is recommended for reliable tool selection
- Keep total tool count under 15 per agent for reliable behavior
- Specialists should only register tools relevant to their domain (e.g., k8s-helm agent only needs Helm tools + basic read tools)

---

## Agent Design - Cohesion, Coupling, and Question Shape

The mechanics above (tool whitelists, count caps, temperature) are tactical. The strategic question is harder: **how should a crew be decomposed into agents in the first place?** This is the same problem as monolithic-app vs microservices decomposition - and the same heuristics apply. High cohesion within an agent. Loose coupling across agents. The "right" answer is rarely "all in one agent" or "one tool per agent."

### Question shape is the unit of cohesion

A well-designed Tooler answers ONE shape of question:

- "What is X **right now**?" (real-time scalar / instant vector)
- "How did X **trend** over the last hour?" (time series, regressions, spikes)
- "What does X **mean**?" (semantics, units, definitions)
- "Is the **infrastructure** itself healthy?" (meta-observability)
- "**Discover** what's available in domain Y." (introspection)

Each shape pulls a different subset of tools, a different prompt voice ("be concise / show numbers" vs "analyze trend / explain"), and a different per-call latency profile. Mixing shapes inside one agent forces every iteration to carry the union of all tools and all guidance - the LLM pays the cost on every question regardless of which shape it's actually answering.

### When you have a "god agent"

Symptoms:
- 5+ tools spanning several question shapes.
- One-sentence purpose statement is hard to write ("it answers GPU-related questions" - too broad).
- Resume-match scores hover around 0.5-0.7 across many topics - neither clearly relevant nor clearly out-of-scope.
- Latency varies wildly call-to-call because some questions use 1 tool and others use 4 in a loop.
- Per-iteration prompt is large; the model spends tokens choosing among tools that have nothing to do with the current question.

Costs:
- **Slow iterations** - every tool definition is in the prompt, every iteration.
- **Tool-selection accuracy degrades** with too many options (Qwen2.5:32b empirically degrades above ~20 tools - see top of section). qwen3:32b is more forgiving but still benefits from narrowness.
- **Triage ambiguity** - the relevance filter can't cleanly say yes/no.
- **Single failure mode** - when this agent wedges or its model is busy, ALL its question shapes are blocked.
- **Hard to evolve** - adding a tool affects every other capability the agent already covers.

### When you have "atomic" agents

Symptoms:
- 0-1 tools per agent, dozens of agents.
- Many agents triage relevant to the same question; multiple agree on the same answer with the same data.
- Composite questions (need 2+ tools that live in different agents) go unanswered or require multi-agent handoff.
- Triage relevance is precise per agent but the crew listing is large and hard to reason about.
- Each agent adds Kubernetes resources (Deployment, Service, ConfigMap) and an always-on pod.

Costs:
- **Orchestration overhead** dominates per-discussion time.
- **Overlapping capabilities** - when two agents have the same answer, the synthesis layer has to choose; if they disagree, the user has to.
- **Capability gaps** - when a question requires N tools spanning N agents, no single agent can answer.
- **Cost of coordination** vs. cost of within-agent inference. Below a threshold, splitting hurts more than it helps.

### The Goldilocks zone

A well-sized Tooler tends to have:

| Dimension | Target |
|---|---|
| Tools | 1-5, all serving the same question shape |
| Description (for triage) | One sentence, clear yes/no for the question types it owns |
| Per-call iterations | Predictable (usually 1-3) because tool selection is unambiguous |
| Relevance score on its own questions | Consistently > 0.85 |
| Relevance score on out-of-scope questions | Consistently < 0.3 |
| Overlap with other agents | Minimal - at most a shared "read" tool family |

### Heuristics for deciding to split

1. **One-sentence test.** If you can't write "I answer X-shaped questions" without using "and" or "or" listing different shapes, split.
2. **Triage ambiguity test.** If the relevance filter returns scores in the 0.4-0.7 band consistently, the agent's scope is too fuzzy.
3. **Tool-shape mapping.** Group the agent's tools by question shape. If a group has only 1-2 tools that nobody else uses, that's a candidate split.
4. **Latency variance.** If the agent's P50 and P90 differ by more than ~3×, different questions are doing very different amounts of work - likely different shapes.
5. **Failure blast radius.** If one tool failing renders the whole agent useless across all question shapes, consider whether the tool deserves its own narrow agent.

### Heuristics for deciding to merge

1. **Always-co-agreeing pair.** If two agents always end up agreeing in the same discussions with substantively identical answers, they have overlapping cohesion - merge them or differentiate their roles.
2. **Sequential dependency.** If question type Z always requires Agent A to answer first and Agent B to follow up, consider a single agent that owns both steps (or a coordinator-level orchestration if it's really common).
3. **Underused capability.** If a tool sits in its own agent but is invoked < 5% of discussions, it's adding triage overhead without earning it - fold it into a related agent.

### Real-world example: nvidia-gpu

Original (god agent, 5 tools, all Prometheus, but four question shapes):

```yaml
name: nvidia-gpu
enabledTools: [execute_query, execute_range_query, list_metrics, get_metric_metadata, get_targets]
```

For "What's GPU utilization on gpu-a?" the agent reasoned through all 5 tool definitions to choose `execute_query`, and a multi-iteration tool loop took ~3 minutes. For "Is the DCGM scrape target up?" it would similarly reason through irrelevant query tools to choose `get_targets`.

Decomposition (three single-shape Toolers):

```yaml
name: nvidia-gpu-now
description: Real-time GPU state queries: current utilization, temperature, VRAM use.
enabledTools: [execute_query]

name: nvidia-gpu-history
description: GPU trend / time-series analysis: how a metric moved over a window.
enabledTools: [execute_range_query, get_metric_metadata]

name: nvidia-gpu-meta
description: Discovery and Prometheus-scrape-health for GPU monitoring.
enabledTools: [list_metrics, get_targets]
```

Each is fast within its shape because its prompt is small and its tool choice is obvious. The coordinator's triage relevance filter picks the right one based on the question; the others stand aside cleanly. The crew gains all the same capabilities, distributed across narrower Toolers.

### Right-sized models are a paired benefit of cohesion

Decomposing a god-agent into single-shape Toolers doesn't only sharpen relevance and shorten tool loops - it also unlocks **right-sized model selection**. A Tooler whose job is one tool call plus a paragraph of formatting doesn't need the same reasoning model as a coordinator synthesizing across multiple agent contributions. When every agent in a crew defaults to a single large model, small-shape Toolers pay for capability they never use, and under per-provider `num_parallel=1` they can starve each other on that one model's slot.

The mechanism is `Agent.spec.capabilities` + `CrewSchedulingPolicy.spec.qualityBias` (see [scheduler.md - Quality bias](../architecture/scheduler.md#quality-bias)). The agent declares an abstract capability (`tool-calling` for a simple Tooler; `+reasoning` for a coordinator or Analyst); the crew author declares what each capability is worth in quality terms; the scheduler resolves to a concrete Model at reconcile time. No agent names a model - loose coupling preserved across cluster boundaries.

A decomposed nvidia-gpu cohort, for example, can have its three single-shape Toolers each declare `[tool-calling, observability]` and resolve to a small fast model, while the coordinator that synthesizes their findings declares `[tool-calling, reasoning]` and resolves to a larger one. One heavy model and many light ones co-exist on the same GPU pool without slot starvation.

### Caveat

These heuristics are guidance, not law. A small single-cluster crew has different optimal granularity than a 100-agent enterprise crew. The right number of agents for *your* crew is the number where the triage layer can confidently route questions to a clear Tooler or Analyst without the per-discussion orchestration cost overwhelming the per-call inference cost.

---

## MCPGateway Integration

Agents access MCP tools through a centralized MCPGateway rather than connecting to MCPServer pods directly.

### Tool Chain

```
Agent Pod                    MCPGateway Pod               MCPServer Pod
┌──────────────┐            ┌──────────────┐            ┌──────────────────┐
│ Agent Runtime │──HTTP/──→ │  Gateway     │──HTTP/──→ │ MCP Bridge       │
│              │   SSE      │  (discovers  │   SSE      │ (sidecar)        │
│ LangChain4j   │            │   servers    │            │    │              │
│ tool calling │            │   via labels)│            │    ▼ stdio        │
│              │            │              │            │ MCP Server        │
│              │ ←─tools──  │              │ ←─tools──  │ (kubectl, helm..) │
└──────────────┘  response  └──────────────┘  response  └──────────────────┘
```

### How It Works

1. **MCPGateway** discovers MCPServer pods via label selectors
2. Gateway connects to each server's mcp-bridge sidecar HTTP/SSE endpoint
3. Gateway caches tool definitions from all registered servers
4. Agent connects to gateway via `KUBEMOOT_GATEWAY_ENDPOINT` env var
5. Agent runtime registers available tools with LangChain4j's tool calling framework
6. When the LLM generates a `tool_calls` response, LangChain4j executes the tool via the gateway
7. Gateway routes the tool call to the appropriate MCPServer

### Key Details

- Gateway caches failed connections - if a server is unavailable on first registration, the gateway must be restarted to retry
- Agent gets gateway endpoint via `KUBEMOOT_GATEWAY_ENDPOINT` env var (injected by operator)
- Coordinators set `KUBEMOOT_GATEWAY_ENABLED=false` because they have no MCP tools - only A2A delegation via NATS

---

## System Prompt Composition

System prompts are composed from `PromptModule` CRs referenced by `Agent.spec.promptRefs`. The operator assembles their `content` in `order` and writes the result to a ConfigMap mounted at `/etc/kubemoot/policy/system.txt`. The agent runtime reads `system.txt` at startup (`KUBEMOOT_SYSTEM_PROMPT_FILE` env var).

```yaml
spec:
  promptRefs:
    - discussion-protocol    # order 10
    - response-style         # order 20
    - my-agent-system        # order 30
```

---

## NATS Events

Agents publish and subscribe to NATS subjects for real-time event streaming.

### Chat Events

Each agent publishes to `kubemoot.chat.<namespace>.<agent_name>` on every chat response:

```json
{
  "agentName": "k8s-workloads",
  "conversationId": "abc123",
  "userMessage": "List all pods in default namespace",
  "assistantMessage": "Here are the pods...",
  "toolCalls": [{"name": "kubectl_get", "status": "success", "duration": 1200}],
  "timestamp": "2026-02-07T10:30:00Z"
}
```

The dashboard subscribes to `kubemoot.chat.>` for real-time topology node pulsing and the Messages page.

### Discussion Events

Each agent subscribes to `kubemoot.discuss.<namespace>.<crew>.<channel>.>` for inter-agent collaboration (see [agentic-consensus.md](../architecture/agentic-consensus.md)).

### Connection Management

The agent runtime shares one lazy NATS connection across every component in the pod.
If `NATS_URL` is not set, all NATS operations are no-ops - the agent still answers
direct chat requests, it simply doesn't participate in discussions.

---

## PromptModule

PromptModule is a CRD for reusable prompt fragments composed into agent system prompts via `Agent.spec.promptRefs`. Multiple agents can reference the same module. When a module changes, the operator re-reconciles all agents that reference it.

**Short name:** `pm`

### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `content` | string | (required) | The prompt text (ADL format recommended) |
| `order` | int32 | 100 | Ordering when composed (lower = earlier in system prompt) |

### Status

| Field | Type | Description |
|-------|------|-------------|
| `ready` | bool | Module is valid and available |
| `referencedBy` | []string | Agent names using this module |

### Order Conventions

| Order Range | Purpose | Examples |
|-------------|---------|----------|
| 1-9 | Coordinator advisory | `advisory-prompt` (5) |
| 10-19 | Shared protocol | `discussion-protocol` (10) |
| 20-29 | Shared style | `response-style` (20) |
| 30-39 | Per-agent identity | `k8s-workloads-system` (30) |
| 40-59 | Coordinator logic | `coordinator-decision-logic` (45), `synthesis-prompt` (50) |

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: PromptModule
metadata:
  name: discussion-protocol
spec:
  order: 10
  content: |
    DEFINE COMPONENT discussion-protocol
    DESCRIPTION Rules for participating in multi-agent discussions

    WHEN the question is NOT in your domain THEN respond NOTHING_TO_ADD immediately
    WHEN the question IS in your domain THEN USE YOUR TOOLS to gather real data
    ALWAYS use tools for verifiable facts
    NEVER give generic instructions without tool-backed evidence
```

---

## HTTP API

Agents expose an HTTP API at the configured port (`Agent.spec.deployment.port`, default `8080`):

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/chat` | POST | Send message, get response |
| `/health` | GET | Health check (with dependencies) |
| `/ready` | GET | Readiness check (local only) |
| `/tools` | GET | List the agent's tools |
| `/conversations/{id}` | DELETE | Clear a conversation |

### Chat Request

```bash
curl -X POST http://k8s-workloads.crew-homelab-pilot:8080/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What pods are running in the default namespace?"}'
```

### Chat Response

```json
{
  "conversationId": "abc123",
  "message": "Here are the pods running in the default namespace:\n\n| Name | Status | ...",
  "toolCalls": [
    {"name": "kubectl_get", "arguments": {"resource": "pods", "namespace": "default"}}
  ]
}
```

---

## Related

- [scheduler.md](../architecture/scheduler.md) - Scheduler design (CRD surface, filter/score/bind)
- [models.md](models.md) - Model catalog and Model CRD reference
- [agentic-consensus.md](../architecture/agentic-consensus.md) - Discussion protocol semantics
- `chart/kubemoot-operator/templates/mootarchetypes.yaml` - built-in `consent-3` MootArchetype
- `homelab-pilot/charts/homelab-pilot-crew/templates/` - example Agent CRs for the Homelab Pilot crew
