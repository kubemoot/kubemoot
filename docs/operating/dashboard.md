---
title: "Kubemoot Dashboard"
description: "The Kubemoot Dashboard is a web UI that lists resources across namespaces, shows GPU and model state, and streams live agent discussions and NATS messages."
weight: 2
---

## Overview

The Kubemoot Dashboard is a web UI for looking at a running Kubemoot installation. It lists Kubemoot resources across namespaces, shows GPU node and model state, and streams agent discussions and NATS messages as they happen. It is a SvelteKit application that reads the Kubernetes API and the NATS bus from the server side; the browser never connects to either directly.

Metrics, logs, and traces are not part of the dashboard. For those, see [Observability](../observability/).

## Install

The dashboard is an optional part of the `kubemoot-operator` chart, off by default because it has no login and can purge NATS streams and delete models (see [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md)). Turn it on with one flag when you install the operator:

```bash
helm upgrade --install kubemoot-operator \
  oci://ghcr.io/kubemoot/charts/kubemoot-operator \
  --namespace kubemoot --create-namespace \
  --set dashboard.enabled=true
```

Every dashboard setting lives under `dashboard:` in the operator chart's values, with the defaults of the standalone chart. `global.imageRegistry` and `global.imagePullSecrets` apply to the dashboard image as they do to the operator's. The chart exposes the app under the `/dashboard` path prefix and creates no route. To look at it, port-forward the service (named after the release, `kubemoot-operator-dashboard` for the release above):

```bash
kubectl -n kubemoot port-forward svc/kubemoot-operator-dashboard 8080:80
```

Then open `http://localhost:8080/dashboard`.

### Standalone chart

The dashboard chart is also published on its own, for a cluster where the operator is installed another way:

```bash
helm upgrade --install kubemoot-dashboard \
  oci://ghcr.io/kubemoot/charts/kubemoot-dashboard \
  --namespace kubemoot --create-namespace
```

Its service is `kubemoot-dashboard`, so the port-forward target is `svc/kubemoot-dashboard`. Do not run both the standalone release and `dashboard.enabled=true` in one namespace unless you want two dashboards.

### Publishing on a hostname

To publish it on a hostname, the chart renders a Gateway API `HTTPRoute` when `gateway.enabled` is true (`dashboard.gateway.enabled` in the operator chart; see the chart's `values.yaml` for the default) or an `Ingress` when `ingress.enabled` is true:

```yaml
gateway:                      # nest under dashboard: in the operator chart's values
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

The sidebar shows the dashboard's version under the logo. A release candidate (`X.Y.Z-rc.N`) carries an RC badge beside it; a published release shows none.

The info icon next to the version opens the Kubemoot components panel. Its first line, `Kubemoot`, is the release the cluster runs: the version of the operator chart, which the operator writes to the `KubemootConfig` status (`status.release`) and the dashboard reads from there. Below it come the dashboard's own version, the operator version, and the operator's uptime. A cluster whose chart was never stamped with a release reads `development build`. The line reads `not reported` while the operator has not written its status yet, and on an operator too old to report a release. The Copy button puts the panel's lines on the clipboard for a support request.

| Section | Pages | Shows |
|---------|-------|-------|
| Top | Overview, Nodes | Ready-of-total counts for nodes, GPUs, crews, and every other resource type (a tile shows a small spinner until its first count arrives, never a zero); Kubernetes nodes with GPU detection and model-loading state. The Overview has the same **Crew** selector as the other pages: it narrows the counts to one crew, while nodes, model servers, models, and embedding models are shared by every crew and always counted in full |
| Messaging | Discussions, Messages | Multi-agent discussion threads with a timeline view; your chat with a crew |
| Crews | Crews, Agents, Prompts, Memory, Fitness | Crew and agent configuration, PromptModule contents, crew memory, and fitness suite runs |
| Models | Models, Model servers, Embeddings | ModelServer backends with their calibration (phase and, per model, fit, load time, generation, prompt, and parallel speed; an embedding model shows as skipped and a model that could not be measured as failed, each with its reason and no speeds), Model state, and EmbeddingModel configuration. Model servers belong to the cluster, so the Model servers page lists them all whatever crew is selected. The Models page also lists each ModelServer's catalog (the models a crew selects by label), because crews ship no Model resources |
| Tools | MCP servers, MCP gateways, Catalogs, Quality policies, Reports | MCPServer instances and tools, gateway routing, quality policies, catalog discovery, and quality verdicts |
| Knowledge | RAG Sources | RAGSource indexing status with document and chunk counts |
| System | Config | The `KubemootConfig` cluster singleton |

### Refresh and resource views

Pages refresh on a timer. A page shows its loading indicator only until its first data arrives; a later refresh runs in the background and swaps the new values in place, and a refresh that fails keeps the last data on screen.

Every page that shows one custom resource (a crew, agent, PromptModule, model server, MCP server, and so on) ends with a collapsed **YAML** section. It shows the resource read-only, without `managedFields`, with a **Copy** button.

An MCP server's tile and detail page show its `spec.description`, a sentence on what the server is for, when the manifest sets one.

An MCP server reached with a key shows **key from Secret `<name>`** (from `registry.authSecretRef`). The dashboard names the Secret and never reads or shows its value.

The Kubemoot logo and **Powered by Kubemoot** open [kubemoot.org](https://kubemoot.org) in a new tab.

### Status colors

| Badge | Meaning |
|-------|---------|
| Green | Ready or loaded |
| Yellow | Pending or pulling |
| Red | Error or failed |
| Gray | Status unavailable |

### Discussions

A two-panel thread viewer. The left panel lists threads, newest first, each tagged with its crew and channel. The right panel shows the message timeline for the selected thread: the question, each agent's contribution, and the synthesis. Recent threads are replayed from the JetStream history on page load, and new messages arrive live.

The conversation view shows the question, a **subcommittee line** naming the agents the coordinator convened with a short reason for each, every agent's position, and the synthesis. Token counts, timings, the triage result, the thread close, placement, and work after the answer sit in a **debug layer** that is off by default. The bug icon in the thread toolbar turns it on and off; its tooltip says how much more it shows, and the dashboard remembers the choice per viewer. The rest of this section describes what the layer shows.

The timeline shows the date and time once, when the thread started, and each message after that as the time since the start, such as `+1m12s`; hover over it for the exact time. Each agent's contribution shows what its messages carry:

- **triage**: from the agent's `triaging` message to its `evaluating` message, or to its signal when it never evaluated
- **evaluation**: from the agent's `evaluating` message to its signal
- **inference**: the time spent in model calls. The pale prefix of the bar is the time to first token: the model reading the prompt, plus loading the model when the call was cold (the tooltip then says "including the model load"). Older messages that recorded only a model load time show it as a load.
- **tokens** in and out, the **model** the call ran on, and the **accelerator** (the ModelServer) that served it

`kmctl conversation` derives triage and evaluation the same way, so the two agree. A thread ends with the time it was answered and a summary of how the answer was made: total time, time to the first signal, the agents the coordinator convened and those that stood aside, each agent's latest signal by kind, agent and coordinator tokens, and the models and accelerators used. A field a message does not carry is left out rather than shown as zero, so older threads show only what they recorded.

When the coordinator closes a thread, a **Placement** panel follows the summary. It lists every model call the scheduler placed, in call order, and shows what the scheduler predicted against what the call measured. A line above the rows gives the number of calls, how many each policy placed, and the mean absolute error of the predicted totals. Each row shows:

- the agent (or the coordinator), the phase of the thread the call belonged to, and the model and accelerator it ran on as `model@server`
- the policy that placed it as a badge: **learned** (the scheduler's learned history), **algorithm** (the built-in rules), or **shadow** (the algorithm placed it while the learned policy only recorded what it would have chosen)
- a **cold start** badge when the model had to be loaded, and the reason the scheduler gave
- the predicted and measured total time, with the signed error and the relative error; a positive error means the call took longer than predicted. When the call measured a time to first token, that is compared the same way
- "no prediction" for a call the scheduler made no prediction for, such as a shadow call

Select **alternatives considered** on a row to see the other placements the scheduler weighed, each with its predicted total split into waiting, loading, reading the prompt and generating, and whether it was warm and could start at once. A thread whose receipt carries no placement calls shows no panel.

The thread toolbar and the thread list share their actions, and every action's tooltip says what it is for:

- **Delete** is one trash-can icon in both places. It asks Yes or No in the page, then removes the discussion from the timeline and purges its messages; this cannot be undone. In the list the icon sits in the bottom-right corner of a tile on hover, clear of the status text.
- **Pin** marks a discussion you want to find again. The pin is stored in the `kubemoot_pinned_threads` bucket, so it stays after a reload and shows in other browsers. A pin does not stop a delete.
- **New discussion** is enabled only when one crew is selected in the top bar, because a discussion is asked of a single crew. With All crews selected the button is disabled and its tooltip says to pick a crew.

The span graph draws one bar per agent from its first to its last message. Each bar names the ModelServer that ran the agent's latest call, taken from the placement the messages carry. A server the scheduler weighed but did not choose is not named, and the label of an agent's static endpoint, which a message sent without a placed call carries, is not shown as a server. A thread started straight on the bus, such as a smoke test, may carry messages with no timestamp. The graph draws the agents whose start and end are known and labels each of the others "no timing recorded" instead of drawing a bar.

### Memory

Shows the working memory of the crew selected in the top bar, the same crew picker the Discussions page uses. Working memory is what a crew has learned about the cluster and keeps between discussions, stored as facts with a topic, a key and a value; the least recently used are dropped over time. With All crews selected the page asks you to pick a crew. A crew with no facts shows an empty state that explains this. **Add or update a fact** enables Save once a crew is selected and the topic, key and value are filled in; until then the line under the form says what is missing. Saving an existing topic and key updates that fact.

**Export** (shown whenever a crew is selected, also on a read-only dashboard) opens a panel that downloads the crew's memory as a zip in the OMPI (Open Memory Protocol Initiative) interchange format, working spec v0.6: a manifest and one memory object per fact. The operator builds the file with the same code as `ompi-export`; the dashboard only relays it and never maps facts to the format itself. The export is always screened: a fact whose topic, key or value holds a credential is left out. Before you download, the panel shows what the export holds and a table of what it left out, each entry named by its key (an opaque id such as `<namespace>.<crew>.#1a2b3c4d` when the key itself held the credential) and the reason, for example the shape of the credential. It never shows a fact's text. The dashboard has no unscreened export: it calls the operator with its own service account, not your identity, so it cannot decide what you may see beyond the namespace scope. To export facts that hold a credential, use `kmctl memory export --allow-unscreened`, which runs with your own credentials. The same namespace scope as the rest of the page applies; a crew outside it answers 404. The dashboard sends its service account token to the operator, which checks it with a TokenReview and allows the export only when that identity may `get` the Crew (a SubjectAccessReview); the dashboard's ClusterRole already grants read on every Kubemoot kind. A caller without a valid token is refused (401) and one without that access is refused (403), so a pod that can reach the operator's report port cannot read a crew's memory. Every other report endpoint is checked the same way; see [The operator report service](#the-operator-report-service).

### Messages

Your chat with a crew. The crew picker is the same **Crew** selector in the top bar that Discussions uses; sending stays disabled until one crew is selected. The left panel lists your conversations with that crew, and the right panel shows them as a chat: your messages and the crew's answers (the synthesis on each thread). A follow-up goes into the same conversation as a reply on the same thread. Nothing of the agents' internal discussion appears here; the **Discussion** link beside an answer opens its thread in Discussions, which shows the consensus messages and statistics.

A new conversation sends a `thread_start` on the crew's discuss subject, the same message **New discussion** sends.

Discussions and Messages show a message as soon as you send it. If the dashboard server did not accept it, the message stays on screen marked **Not sent** with the reason and a **Retry** button, so a question that never reached the crew does not look sent. The reasons are: your session has ended (sign in again in this tab or another, then press Retry), the dashboard server could not be reached, or the server refused the message. Retry sends the same message again. Unsent messages are kept in the page only, so reloading it drops them. A pause, resume or reply that was not sent does not change what the page shows about the discussion, and a Messages conversation whose first message was not sent takes no reply until that message is sent. The dashboard server answers success once the NATS server has the message (it publishes, then flushes); it does not wait for the discussion stream to store it. When the dashboard cannot read its own version, the sidebar and an exported discussion say "version unknown".

The **Details** toggle is off by default and the dashboard remembers the choice per viewer. When it is on, each answer shows the time from your question to the answer and the tokens used: the agents' tokens plus the coordinator's. A figure the thread did not record is left out.

The toolbar copies the conversation to the clipboard and deletes it (removing its thread and purging its messages) after an in-page confirmation. A read-only dashboard hides sending and deleting. To watch raw NATS subjects, use `kmctl` or the NATS tooling directly.

### Crews

One card per crew, linking to the crew's detail page. The trash icon on a card removes the crew after an in-page confirmation that names the crew and says what goes with it: its Agents and its open discussions, which the operator removes through its finalizer and owner references. The dashboard deletes the Crew object only (`DELETE /api/kubemoot/crews/<name>?namespace=`). The dashboard does not add crews; create them with CrewForge or `kmctl`.

### Reports

MCPServerReport quality verdicts (`use`, `caution`, `avoid`) with the evaluation history. An administrator can pin a verdict to override the automated result; this needs `patch` on `mcpserverreports`, which the chart's `ClusterRole` does not grant by default.

### Fitness

CrewFitnessSuite runs and their scores. A running suite has Pause and Stop buttons, and a paused one has Resume and Stop; they set `spec.suspend` and `spec.cancel` (see the [CrewFitnessSuite reference](../../reference/crewfitnesssuite/)). The status badge reads `Pausing` while a paused suite's last iteration finishes and `Stopping` until the operator applies a stop. The Fitness page can also delete a suite run; the owned CrewFitness resources are removed with it.

## Access the dashboard needs

The chart creates a `ClusterRole` (`kubemoot-dashboard-reader`) with these grants:

| Resources | Verbs | Why |
|-----------|-------|-----|
| `nodes`, `namespaces`, `pods` | get, list, watch | Node and namespace views |
| `pods/log` | get | Log access |
| `configmaps` | get, list | Agent prompt bundles for the Agent detail page |
| `deployments` (apps) | get, list, watch | Operator version in the Kubemoot components panel |
| All `kubemoot.ai` resources | get, list, watch | Every Kubemoot page |
| `crews` | delete | The Crews page "remove crew" action |
| `crewfitnesssuites` | delete, patch | The Fitness page "remove run" action, and its Pause, Resume, and Stop actions |

The dashboard never reads or displays Secret values. The container runs as a non-root user with a read-only root filesystem.

### The operator report service

The dashboard reads the component status, the fitness report download, and the crew memory export from the operator's report service (port 8082). That service is plain HTTP and any pod can reach it, so every endpoint except `/healthz` requires a bearer token. The operator checks it with a TokenReview, then asks the API server with a SubjectAccessReview whether that identity may make the access the endpoint equals:

| Endpoint | Access checked |
|----------|----------------|
| `/report/{namespace}/{suite}` (fitness report) | `get` the CrewFitnessSuite in that namespace |
| `/memory-export/{namespace}/{crew}` | `get` the Crew in that namespace |
| `/componentstatuses` | `list` ModelServers in all namespaces |

The component status reports the operator, NATS, and how many ModelServers are ready. The ModelServer count is the only part that comes from cluster objects and it spans namespaces, so listing ModelServers is the narrowest access that fits it. The dashboard sends its service account token on every call, and its `kubemoot.ai` read grant above covers all three. A call with no valid token answers 401 with the header to send (`Authorization: Bearer <token>`), one whose identity lacks the access answers 403, and a service started without an authorizer answers 503 rather than serving. Any other client of these endpoints, such as a script in a pod, sends its own service account token and needs the same access; `kmctl` and CrewForge do not call them.

## HTTP API

The SvelteKit server exposes the JSON and event-stream endpoints the pages use. They carry no authentication, so the same rule applies as for the UI: reach them only through an authenticating proxy. List endpoints accept a `namespace` query parameter; an empty value lists across all namespaces.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/health`, `/api/version` | GET | Health check; dashboard version |
| `/api/namespaces`, `/api/nodes` | GET | Namespaces; Kubernetes nodes with GPU information |
| `/api/kubemoot/<plural>` and `/api/kubemoot/<plural>/<name>` | GET | List and detail for `agents`, `crews`, `models`, `modelservers`, `embeddingmodels`, `mcpservers`, `mcpgateways`, `mcpqualitypolicies`, `mcpcatalogs`, `mcpserverreports`, `ragsources`, `promptmodules`, `crewfitnesses` |
| `/api/kubemoot/crews/<name>?namespace=` | DELETE | Remove a crew (the Crew object only) |
| `/api/kubemoot/config`, `/api/kubemoot/system-info`, `/api/kubemoot/topology` | GET | Configuration, system information, agent topology graph |
| `/api/kubemoot/crewfitnesssuites` | GET | Fitness suites; sub-paths under `<namespace>/<name>/` serve `scores`, `iterations`, `transcript`, and `artifact`; DELETE on `<namespace>/<name>` removes a run; PATCH on `<namespace>/<name>` with `{"action": "pause" \| "resume" \| "stop"}` controls it |
| `/api/kubemoot/mcpserverreports/<name>` | PATCH | Pin a verdict |
| `/api/kubemoot/modelservers/<name>/load`, `/unload`, `/delete` | POST | Model server actions |
| `/api/kubemoot/crew-memory` | GET, POST, DELETE | Crew memory facts |
| `/api/kubemoot/crew-memory/export?namespace=&crew=` | GET | The crew's memory as an OMPI v0.6 zip, always screened (an `allowUnscreened` parameter is refused); `report=true` returns the selection report as JSON (left-out entries by key or opaque id and reason, never text) |
| `/api/kubemoot/watch/<plural>` | GET | Kubernetes resource updates as server-sent events |
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
