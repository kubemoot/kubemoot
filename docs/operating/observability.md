---
title: "Kubemoot Observability"
description: "What a Kubemoot crew exposes for observation: discussion signals, metrics, logs, and traces, the surfaces that render them, and the external stack they rely on."
weight: 3
---

How to observe what a Kubemoot crew is doing: the data it exposes, the surfaces that render it, and the external observability stack it depends on. For the dashboard UI itself see [dashboard.md](dashboard.md); for the consensus protocol that produces most of this data see [agentic-consensus.md](../architecture/agentic-consensus.md).

## What you can observe

| Domain | Signals | Where it comes from |
|--------|---------|---------------------|
| **Discussions / consensus** | Thread lifecycle, per-agent signals (`triaging`, `evaluating`, `waiting`, `agree`, `concern`, `stand_aside`, `failure`, `block`, `advisory`, `synthesis`), settle decisions, synthesis text | Coordinator + agents publish to NATS `kubemoot.discuss.<namespace>.<crew>.*` |
| **Agent activity** | Heartbeats while GPU-queued, triage vs evaluation phases, per-call server attribution (which model/server/GPU served each call) | Agent runtime signals + metadata |
| **GPU & scheduling** | Loaded-model footprints, in-flight tickets, server readiness, total/used VRAM, scheduling decisions | Operator-published server state (NATS KV) + DCGM |
| **Quality** | MCP tool quality verdicts (hot/failing/out-of-norm) | MCP quality pipeline → `kubemoot.quality.*` |
| **RAG indexing** | Source indexing progress, chunk/embed status | RAGSource controller |
| **Fitness** | Per-scenario/iteration pass-rates, assertion results, durations, full conversation transcripts | `CrewFitness` / `CrewFitnessSuite` → XLSX in NATS Object Store |

## Surfaces

- **Kubemoot Dashboard**: the primary real-time view (discussion threads & timeline, agent topology, NATS messages, fitness suites, accelerator and model activity). Updates push-style via NATS JetStream SSE and k8s watch→SSE. See [dashboard.md](dashboard.md).
- **NATS JetStream**: the event substrate. Streams: `KUBEMOOT_CHRONICLE`, `KUBEMOOT_QUALITY`, `KUBEMOOT_CHAT`, `KUBEMOOT_OPERATOR`, `KUBEMOOT_DISCUSS`. Anything the dashboard shows is reconstructable from these.
- **Prometheus metrics and Grafana dashboards** (optional): every Kubemoot component serves Prometheus metrics, and Kubemoot ships Grafana dashboards for them. See [Metrics: Prometheus and Grafana](#1-metrics-prometheus-and-grafana-optional).

## The three pillars of observability

Kubemoot leans on the cluster's existing observability stack rather than shipping its own. The three pillars (**metrics, logs, spans**) are at different stages.

### 1. Metrics: Prometheus and Grafana (optional)

Kubemoot runs without Prometheus or Grafana. They are recommended: with them you can
watch the scheduler place calls, discussions settle, and RAG sources answer, over time.

**What serves metrics.** Each component serves Prometheus metrics, and each metric
carries a description (its `# HELP` line):

| Component | Where | How |
|---|---|---|
| Agents (every crew) | port `http` (8080), `/q/metrics` | plain HTTP |
| RAG query services | port `http` (8000), `/metrics` | plain HTTP |
| Operator | port `metrics` (8080), `/metrics` | HTTPS; the caller needs permission to `get` the non-resource URL `/metrics` |

**Seeing the metrics and what they mean.** Metrics change as Kubemoot grows, so read
the live list rather than a copy. Kubemoot's own metrics start with `kubemoot_`.

```bash
# From an agent, with descriptions: the # HELP lines
kubectl get --raw "/api/v1/namespaces/<crew-namespace>/pods/<agent-pod>:8080/proxy/q/metrics" \
  | grep '^# HELP kubemoot_'
```

With Prometheus scraping them, the same descriptions come from its metadata API
(`/api/v1/metadata?metric=<name>`), and Grafana's metrics browser shows them next to
each metric name. Every Kubemoot metric has a description. The operator's metrics
are served over HTTPS and need a token, so read their descriptions through
Prometheus or Grafana.

**Scraping.** With the Prometheus Operator (for example kube-prometheus-stack), the
operator chart creates PodMonitors for the agents, the query services, and the
operator (`metrics.podMonitor`, on by default). They render only when the cluster
serves the PodMonitor API, so a cluster without it gets nothing. Give them the labels
your Prometheus selects monitors by; kube-prometheus-stack selects
`release: <its Helm release name>` unless it is configured otherwise:

```yaml
metrics:
  podMonitor:
    labels:
      release: kube-prometheus-stack
```

The agents' PodMonitor adds a `crew` and an `agent` label to every series, from the
pod's `kubemoot.ai/crew` and `kubemoot.ai/agent` labels; the `namespace` label comes
from the scrape.

For the operator's HTTPS endpoint the chart also creates a metrics reader: a
ServiceAccount allowed to read `/metrics` only, whose token the operator's PodMonitor
presents. The operator serves its metrics with a self-signed certificate, so that
PodMonitor skips certificate verification; the token still has to pass the operator's
authentication and authorization. Without the Prometheus Operator, scrape the same
endpoints with your own scrape configuration.

Accelerator metrics (utilization, memory, temperature) come from the accelerator's own
exporter where one runs, such as NVIDIA's DCGM exporter; Kubemoot does not install it.

### Grafana dashboards

Kubemoot ships three dashboards, in a `Kubemoot` folder by default:

- **Kubemoot Overview**: model calls and their duration, agents evaluating, RAG query rates and timings, memory and CPU.
- **Kubemoot Discussions**: discussion threads, signals, phases, and tokens.
- **Kubemoot Scheduler**: where agents' model calls run (by server, model, and kind), cold starts, waits for capacity, generation speed, the placement policy (and, in shadow mode, how often the learned policy agrees), and each server's calibration, speeds, and drift.

They are agnostic of any one cluster: every panel picks its Prometheus through a
data source variable, and namespaces, crews, and servers are variables filled from
the metrics, so nothing names a cluster, crew, server, or model. The Overview and
Discussions dashboards filter their agent panels by crew. Install them the way your
Grafana loads dashboards:

| Your Grafana | How |
|---|---|
| Runs the dashboard sidecar (the Grafana Helm chart, kube-prometheus-stack) | The operator chart creates a ConfigMap per dashboard that the sidecar loads (`grafana.dashboards`, on by default). Set `label`, `labelValue`, `folder`, and `namespace` to match the sidecar's configuration. |
| Is run by the Grafana Operator | Set `grafana.dashboards.grafanaOperator.enabled: true` and `instanceSelector` to match your Grafana; the chart creates a GrafanaDashboard per dashboard. |
| Anything else | Import each dashboard with Grafana's Import dashboard and pick your Prometheus data source. The JSON files are in the chart's `dashboards/` folder, and releases that list them carry them as assets of the [GitHub Release](https://github.com/kubemoot/kubemoot/releases), byte for byte the chart's files, which are signed with the chart. |

The dashboards fill as the metrics are scraped; the scheduler's server panels need
the operator's metrics, and its placement panels need the agents'.

### 2. Logs: structured stdout today; aggregation is a gap

- The coordinator, agents, and operator emit **structured JSON logs** to stdout (Quarkus JSON logging): phase transitions, signal publishes, `failure` causes, tool calls, scheduling decisions.
- **No log-aggregation backend is deployed** (no Loki/Alloy/Promtail). Logs are reachable only per-pod via `kubectl logs` / Headlamp, no central search, retention, or correlation.
- **What it lets you observe (today):** per-pod debugging only. The dashboard deliberately defers aggregation to a logs backend.
- Closing this gap (Grafana Loki + a log collector, ideally with trace-correlated structured logs) is in [Future work](#future-work).

### 3. Spans / traces: OTel + Tempo deployed, not yet emitted to

- The reference cluster runs an **otel-collector** (OTLP `4317`/`4318` → `tempo.observability:4317`) and **Grafana Tempo** as the trace backend.
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

- **Produce** a real OTel trace per discussion: thread = root span; each agent evaluation or mulling step and each tool call = child spans with true start/end, carrying GenAI semantic-convention attributes (`gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`/`output_tokens`) plus Kubemoot extras (crew, thread, signal, server, GPU label).
- **Consume**: the dashboard span graph renders OTel-shaped spans rather than reconstructing from signals; the same trace data also flows to the existing otel-collector → Tempo, and to any OTel-compatible profiler.
- **Two transports**: live (NATS, keeping the dashboard real-time) plus OTLP batch (for Tempo and the wider ecosystem). Unlocks replaying historical discussions from the trace backend, not just live ones.

The infrastructure (otel-collector + Tempo) already exists; the remaining work is Kubemoot-side emission/rendering plus minor config (Grafana Tempo datasource, Tempo retention). Aligning with OTel conventions is table stakes.
