---
title: "Kubemoot Dashboard"
weight: 2
---

## Overview

The Kubemoot Dashboard is a web UI for looking at a running Kubemoot installation. It lists Kubemoot resources across namespaces, shows GPU node and model state, and streams agent discussions and NATS messages as they happen. It is a SvelteKit application that reads the Kubernetes API and the NATS bus from the server side; the browser never connects to either directly.

Metrics, logs, and traces are not part of the dashboard. For those, see [Observability](../observability/).

## Install

The dashboard ships as a Helm chart. Install it into the namespace of your choice:

```bash
helm upgrade --install kubemoot-dashboard \
  oci://ghcr.io/kubemoot/charts/kubemoot-dashboard \
  --namespace kubemoot --create-namespace
```

The chart exposes the app under the `/dashboard` path prefix. To look at it without configuring routing, port-forward the service:

```bash
kubectl -n kubemoot port-forward svc/kubemoot-dashboard 8080:80
```

Then open `http://localhost:8080/dashboard`.

To publish it on a hostname, the chart renders a Gateway API `HTTPRoute` when `gateway.enabled` is true (see the chart's `values.yaml` for the default) or an `Ingress` when `ingress.enabled` is true:

```yaml
gateway:
  enabled: true
  name: gateway
  namespace: default
  hostnames:
    - kubemoot.example.com
  pathPrefix: /dashboard
```

The dashboard has no login of its own, whether or not the chart publishes the route. Put authentication in front of it at the Gateway or Ingress layer (an OAuth2 proxy or your platform's access policy).

## What it shows

The sidebar groups pages by what they cover. The Topology page exists by URL (`/topology`) without a sidebar entry.

| Section | Pages | Shows |
|---------|-------|-------|
| Top | Overview, Nodes | Counts and health for every resource type; Kubernetes nodes with GPU detection and model-loading state |
| Messaging | Discussions, Messages | Multi-agent discussion threads with a timeline view; a NATS subject explorer with a live message viewer |
| Crews | Crews, Agents, Prompts, Memory, Fitness | Crew and agent configuration, PromptModule contents, crew memory, and fitness suite runs |
| Models | Providers, Models, Embeddings | ModelProvider backends, Model state, and EmbeddingModel configuration |
| MCPs | MCP Servers, MCP Gateways, Quality Policies, Catalogs, Reports | MCPServer instances and tools, gateway routing, quality policies, catalog discovery, and quality verdicts |
| Knowledge | RAG Sources | RAGSource indexing status with document and chunk counts |
| System | Config | The `KubemootConfig` cluster singleton |

### Status colors

| Badge | Meaning |
|-------|---------|
| Green | Ready or loaded |
| Yellow | Pending or pulling |
| Red | Error or failed |
| Gray | Status unavailable |

### Discussions

A two-panel thread viewer. The left panel lists threads, newest first, each tagged with its crew and channel. The right panel shows the message timeline for the selected thread: the question, each agent's contribution, and the synthesis. Recent threads are replayed from the JetStream history on page load, and new messages arrive live.

### Messages

A general NATS subject explorer. Subscribe to any subject pattern (for example `kubemoot.chat.>` or `kubemoot.operator.>`), watch JSON payloads as they arrive, and publish to arbitrary subjects. It is useful for debugging agent communication and operator events.

### Reports

MCPServerReport quality verdicts (`use`, `caution`, `avoid`) with the evaluation history. An administrator can pin a verdict to override the automated result; this needs `patch` on `mcpserverreports`, which the chart's `ClusterRole` does not grant by default.

### Fitness

CrewFitnessSuite runs and their scores. The Fitness page can delete a suite run; the owned CrewFitness resources are removed with it.

## Access the dashboard needs

The chart creates a `ClusterRole` (`kubemoot-dashboard-reader`) with these grants:

| Resources | Verbs | Why |
|-----------|-------|-----|
| `nodes`, `namespaces`, `pods` | get, list, watch | Node and namespace views |
| `pods/log` | get | Log access |
| `configmaps` | get, list | Agent prompt bundles for the Agent detail page |
| `deployments` (apps) | get, list, watch | Operator version in the system-info popover |
| All `kubemoot.ai` resources | get, list, watch | Every Kubemoot page |
| `crewfitnesssuites` | delete | The Fitness page "remove run" action |

The dashboard never reads or displays Secret values. The container runs as a non-root user with a read-only root filesystem.

## HTTP API

The SvelteKit server exposes the JSON and event-stream endpoints the pages use. They carry no authentication, so the same rule applies as for the UI: reach them only through an authenticating proxy. List endpoints accept a `namespace` query parameter; an empty value lists across all namespaces.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/health`, `/api/version` | GET | Health check; dashboard version |
| `/api/namespaces`, `/api/nodes` | GET | Namespaces; Kubernetes nodes with GPU information |
| `/api/kubemoot/<plural>` and `/api/kubemoot/<plural>/<name>` | GET | List and detail for `agents`, `crews`, `models`, `modelproviders`, `embeddingmodels`, `mcpservers`, `mcpgateways`, `mcpqualitypolicies`, `mcpcatalogs`, `mcpserverreports`, `ragsources`, `promptmodules`, `crewfitnesses` |
| `/api/kubemoot/config`, `/api/kubemoot/system-info`, `/api/kubemoot/topology` | GET | Configuration, system information, agent topology graph |
| `/api/kubemoot/crewfitnesssuites` | GET | Fitness suites; sub-paths under `<namespace>/<name>/` serve `scores`, `iterations`, `transcript`, and `artifact`; DELETE on `<namespace>/<name>` removes a run |
| `/api/kubemoot/mcpserverreports/<name>` | PATCH | Pin a verdict |
| `/api/kubemoot/modelproviders/<name>/load`, `/unload`, `/delete` | POST | Model provider actions |
| `/api/kubemoot/crew-memory` | GET, POST, DELETE | Crew memory facts |
| `/api/kubemoot/watch/<plural>`, `/api/sse` | GET | Kubernetes resource updates as server-sent events |
| `/api/nats/subscribe?subject=` | GET | Server-sent events for a NATS subject |
| `/api/nats/history?stream=&subject=&limit=` | GET | JetStream history replay as a JSON array |
| `/api/nats/publish` | POST | Publish `{ "subject", "data" }` to NATS |
| `/api/nats/config`, `/api/nats/kv`, `/api/nats/stream`, `/api/nats/purge` | GET, POST | NATS connection status, KV, stream, and purge helpers |

The browser talks to the SvelteKit server with server-sent events. Only the server holds a NATS connection (the `nats` client over TCP), so the browser never connects to NATS directly.

## Related

- [Observability](../observability/)
- [MCPServer guide](../../reference/mcpserver-guide/)
- [RAGSource guide](../../reference/ragsource-guide/)
- [Agent reference](../../reference/agent/)
