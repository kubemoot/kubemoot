---
title: "Kubemoot Observability"
weight: 3
---

How to observe what a Kubemoot crew is doing: the data it exposes, the surfaces that render it, and the external observability stack it depends on. For the dashboard UI itself see [dashboard.md](dashboard.md); for the consensus protocol that produces most of this data see [agentic-consensus.md](../architecture/agentic-consensus.md).

## What you can observe

| Domain | Signals | Where it comes from |
|--------|---------|---------------------|
| **Discussions / consensus** | Thread lifecycle, per-agent signals (`triaging`, `evaluating`, `waiting`, `agree`, `concern`, `stand_aside`, `failure`, `block`, `advisory`, `synthesis`), settle decisions, synthesis text | Coordinator + agents publish to NATS `kubemoot.discuss.<namespace>.<crew>.*` |
| **Agent activity** | Heartbeats while GPU-queued, triage vs evaluation phases, per-call provider attribution (which model/provider/GPU served each call) | Agent runtime signals + metadata |
| **GPU & scheduling** | Loaded-model footprints, in-flight tickets, provider readiness, total/used VRAM, scheduling decisions | Operator-published provider state (NATS KV) + DCGM |
| **Quality** | MCP tool quality verdicts (hot/failing/out-of-norm) | MCP quality pipeline → `kubemoot.quality.*` |
| **RAG indexing** | Source indexing progress, chunk/embed status | RAGSource controller |
| **Fitness** | Per-scenario/iteration pass-rates, assertion results, durations, full conversation transcripts | `CrewFitness` / `CrewFitnessSuite` → XLSX in NATS Object Store |

## Surfaces

- **Kubemoot Dashboard**: the primary real-time view (discussion threads & timeline, agent topology, NATS messages, fitness suites, GPU/model activity). Updates push-style via NATS JetStream SSE and k8s watch→SSE. See [dashboard.md](dashboard.md).
- **NATS JetStream**: the event substrate. Streams: `KUBEMOOT_CHRONICLE`, `KUBEMOOT_QUALITY`, `KUBEMOOT_CHAT`, `KUBEMOOT_OPERATOR`, `KUBEMOOT_DISCUSS`. Anything the dashboard shows is reconstructable from these.
- **Prometheus metrics**: the agent runtime exposes Micrometer/Prometheus-format metrics; `DiscussionMetrics` (`ai.kubemoot.agent.nats`) covers inference timings, token usage, signal counts, and thread/synthesis completion.

## The three pillars of observability

Kubemoot leans on the cluster's existing observability stack rather than shipping its own. The three pillars (**metrics, logs, spans**) are at different stages.

### 1. Metrics: Prometheus / Grafana (in use today)

- **kube-prometheus-stack** (Prometheus + Grafana) scrapes cluster, node, pod, and agent JVM/Micrometer metrics.
- **DCGM exporter** (namespace `observability`, `ServiceMonitor` labelled `release: kube-prometheus-stack`) provides per-GPU metrics: utilization %, VRAM used/total, temperature, power, per GPU (e.g. RTX 5090 / RTX 4090).
- **Agent runtime** exposes Micrometer/Prometheus metrics; `DiscussionMetrics` covers inference timings, token usage, signal counts, and thread/synthesis completion.
- **What it lets you observe:** GPU saturation and VRAM headroom (the scheduler's inputs), inference latency/throughput and token cost, cluster health.

### 2. Logs: structured stdout today; aggregation is a gap

- The coordinator, agents, and operator emit **structured JSON logs** to stdout (Quarkus JSON logging): phase transitions, signal publishes, `failure` causes, tool calls, scheduling decisions.
- **No log-aggregation backend is deployed** (no Loki/Alloy/Promtail). Logs are reachable only per-pod via `kubectl logs` / Headlamp, no central search, retention, or correlation.
- **What it lets you observe (today):** per-pod debugging only. The dashboard deliberately defers aggregation to a logs backend.
- Closing this gap (Grafana Loki + a log collector, ideally with trace-correlated structured logs) is in [Future work](#future-work).

### 3. Spans / traces: OTel + Tempo deployed, not yet emitted to

- The cluster already runs an **otel-collector** (OTLP `4317`/`4318` → `tempo.observability:4317`) and **Grafana Tempo** as the trace backend.
- **Today Kubemoot does not emit OTel traces**: discussion timing is *reconstructed* from NATS signal messages (see the Agent Span Graph below), which is an approximation, not a true distributed trace.
- Wiring true trace emission to this existing backend is in [Future work](#future-work).

Two of the three pillars (logs, spans) have gaps; see [Future work](#future-work).

## Agent Span Graph

The dashboard's span graph visualizes each agent's participation timeline with split bars showing overhead (queue/triage) vs inference time. GPU colors are assigned dynamically from message metadata. Legend:

| Element | Description |
|---------|-------------|
| **Agree** | Agent ran tools and found relevant data to contribute |
| **Advisory** | Coordinator identified relevant technologies and context for the query |
| **Concern** | Agent found a potential risk or caveat worth highlighting |
| **Block** | Agent raised a serious objection; synthesis is halted |
| **Stand Aside** | Query is outside this agent's domain; no contribution |
| **Proposal** | Onboarding agent proposed a new tool to fill a capability gap |
| **Synthesis** | Coordinator synthesized all agent contributions into a final answer |
| **Triaging** | Agent is queued on the triage GPU deciding whether to contribute |
| **Evaluating** | Agent passed triage and is running full tool-calling inference |
| **Heartbeat** | Agent is alive, queued for or running LLM inference |
| **Inference** | Saturated bar portion: actual LLM inference time |
| **Overhead** | Faded bar portion: GPU queue wait and triage time before inference |
| **Queue Depth** | Red circle on bar: number of other agents running inference on the same GPU concurrently |

Signal markers (vertical ticks) appear on bars when an agent has multiple signal types, e.g., the coordinator bar shows purple advisory ticks within its amber synthesis bar. Bars start at each agent's actual first message timestamp, not at t=0.

The topology page (`/topology`) shows agent relationships as a graph, with real-time node pulsing when agents receive chat messages via `kubemoot.chat.>` SSE.

> The span graph today **infers** spans from NATS signal timestamps. The future work below replaces this with true OpenTelemetry spans, rendered by the same graph and exportable to the trace backend.

## Future work

Two items complete the three pillars.

### Logs: a log aggregation backend

Stand up a log backend so the already-structured JSON logs are centrally searchable and retained, instead of per-pod `kubectl logs`.

- **Decision: Grafana Loki.** Completes the existing Grafana "LGTM" stack (Loki/Grafana/Tempo/Prometheus): one pane of glass, label-based indexing, lightweight (object-storage backed), and native **trace↔log correlation** (jump from a discussion span to that agent's logs by `trace_id` once traces land).
- *Considered and rejected:* Elasticsearch/ELK: stronger full-text search and analytics, but heavy (JVM, RAM/disk), a separate Kibana UI, and weaker trace correlation. A poor footprint trade on GPU-saturated hardware.
- **Collection** is backend-agnostic: the existing otel-collector can ship pod logs via its `loki` or `elasticsearch` exporter (filelog receiver), so the choice is only the store. Do **not** route logs through NATS; keep app-events and logs separate, on the standard path.
- **Kubemoot side:** ensure structured logs carry `trace_id`/`thread_id` so logs correlate with the OTel spans below.

### Spans: OpenTelemetry agent trace export

Make OpenTelemetry the canonical span model for discussions instead of the bespoke signal-reconstructed span graph:

- **Produce** a real OTel trace per discussion: thread = root span; each agent evaluation/mulling turn and each tool call = child spans with true start/end, carrying GenAI semantic-convention attributes (`gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`/`output_tokens`) plus Kubemoot extras (crew, thread, signal, provider, GPU label).
- **Consume**: the dashboard span graph renders OTel-shaped spans rather than reconstructing from signals; the same trace data also flows to the existing otel-collector → Tempo, and to any OTel-compatible profiler.
- **Two transports**: live (NATS, keeping the dashboard real-time) plus OTLP batch (for Tempo and the wider ecosystem). Unlocks replaying historical discussions from the trace backend, not just live ones.

The infrastructure (otel-collector + Tempo) already exists; the remaining work is Kubemoot-side emission/rendering plus minor config (Grafana Tempo datasource, Tempo retention). Aligning with OTel conventions is table stakes.
