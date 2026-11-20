---
title: "Kubemoot Dashboard"
description: "The Kubemoot Dashboard is a read-only web UI that lists resources across namespaces, shows GPU and model state, and streams live agent discussions and NATS messages."
weight: 2
---

## Overview

The Kubemoot Dashboard is a read-only web UI for looking at a running Kubemoot installation. It lists Kubemoot resources across namespaces, shows GPU node and model state, and streams agent discussions and NATS messages as they happen. It is a SvelteKit application that reads the Kubernetes API and the NATS bus from the server side; the browser never connects to either directly.

The dashboard never changes anything. It cannot ask a crew a question, delete a discussion, edit memory, load or unload a model, or control a fitness suite. A dashboard that acted for whoever reached it would let that person do anything its own ServiceAccount and NATS connection can, so every change is made with your own credentials instead: with `kmctl`, with CrewForge, or with `kubectl`. Where a control used to be, the page shows the command to run, with a **Copy** button. See [Where changes are made](#where-changes-are-made).

Metrics, logs, and traces are not part of the dashboard. For those, see [Observability](../observability/).

## Install

The dashboard is an optional part of the `kubemoot-operator` chart, off by default because it has no login, so whoever reaches it can read discussions and crew memory (see [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md)). Turn it on with one flag when you install the operator:

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
| Messaging | Discussions, Messages | Multi-agent discussion threads with a timeline view; the conversations people have had with a crew |
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

The thread toolbar copies the discussion as Markdown, copies its thread ID, and shows or hides the span graph and the debug layer. It cannot start, answer, pause, stop, replay, pin or delete a discussion. Below the timeline the page shows the `kmctl conversation ask` command that asks a follow-up in the same conversation (find the conversation ID with `kmctl conversation list CREW`), and an empty list shows the command that starts one.

The span graph draws one bar per agent from its first to its last message. Each bar names the ModelServer that ran the agent's latest call, taken from the placement the messages carry. A server the scheduler weighed but did not choose is not named, and the label of an agent's static endpoint, which a message sent without a placed call carries, is not shown as a server. A thread started straight on the bus, such as a smoke test, may carry messages with no timestamp. The graph draws the agents whose start and end are known and labels each of the others "no timing recorded" instead of drawing a bar.

### Memory

Shows the working memory of the crew selected in the top bar, the same crew picker the Discussions page uses. Working memory is what a crew has learned about the cluster and keeps between discussions, stored as facts with a topic, a key and a value; the least recently used are dropped over time. With All crews selected the page asks you to pick a crew. A crew with no facts shows an empty state that explains this. The page only reads: facts are written by the crew's agents, and the page shows the `kmctl memory export` command that takes a copy.

**Export** (shown whenever a crew is selected) opens a panel that downloads the crew's memory as a zip in the OMPI (Open Memory Protocol Initiative) interchange format, working spec v0.6: a manifest and one memory object per fact. The operator builds the file with the same code as `ompi-export`; the dashboard only relays it and never maps facts to the format itself. The export is always screened: a fact whose topic, key or value holds a credential is left out. Before you download, the panel shows what the export holds and a table of what it left out, each entry named by its key (an opaque id such as `<namespace>.<crew>.#1a2b3c4d` when the key itself held the credential) and the reason, for example the shape of the credential. It never shows a fact's text. The dashboard has no unscreened export: it calls the operator with its own service account, not your identity, so it cannot decide what you may see beyond the namespace scope. To export facts that hold a credential, use `kmctl memory export --allow-unscreened`, which runs with your own credentials. The same namespace scope as the rest of the page applies; a crew outside it answers 404. The dashboard sends its service account token to the operator, which checks it with a TokenReview and allows the export only when that identity may `get` the Crew (a SubjectAccessReview); the dashboard's ClusterRole already grants read on every Kubemoot kind. A caller without a valid token is refused (401) and one without that access is refused (403), so a pod that can reach the operator's report port cannot read a crew's memory. Every other report endpoint is checked the same way; see [The operator report service](#the-operator-report-service).

### Messages

The conversations people have had with a crew. The crew picker is the same **Crew** selector in the top bar that Discussions uses. The left panel lists the crew's conversations, and the right panel shows one as a chat: the questions and the crew's answers (the synthesis on each thread). Nothing of the agents' internal discussion appears here; the **Discussion** link beside an answer opens its thread in Discussions, which shows the consensus messages and statistics. The page cannot send a message: it shows the `kmctl conversation ask` command that asks the crew a question, with `--conversation-id` for a follow-up in the same conversation.

The **Details** toggle is off by default and the dashboard remembers the choice per viewer. When it is on, each answer shows the time from the question to the answer and the tokens used: the agents' tokens plus the coordinator's. A figure the thread did not record is left out. The toolbar copies the conversation to the clipboard. To watch raw NATS subjects, use `kmctl` or the NATS tooling directly. When the dashboard cannot read its own version, the sidebar and an exported discussion say "version unknown".

### Crews

One card per crew, linking to the crew's detail page. The dashboard does not add or remove crews: create them with CrewForge or `kmctl`, and remove one with `kmctl delete crew NAME -n NAMESPACE` (the operator's finalizer and owner references remove its Agents and open discussions). The page shows that command.

### Reports

MCPServerReport quality verdicts (`use`, `caution`, `avoid`) with the evaluation history, and any verdict an administrator pinned. The page shows the `kubectl patch mcpserverreport` command that pins one; it runs with your credentials, so it needs `patch` on `mcpserverreports`.

### Fitness

CrewFitnessSuite runs and their scores. Opening a run shows the commands that control it: `kubectl patch` to set `spec.suspend` (pause and resume) or `spec.cancel` (stop; see the [CrewFitnessSuite reference](../../reference/crewfitnesssuite/)), and `kmctl delete crewfitnesssuite` to remove the run with its owned CrewFitness resources. The status badge reads `Pausing` while a paused suite's last iteration finishes and `Stopping` until the operator applies a stop.

### Models

The Models and Model servers pages show state only. Loading, unloading and deleting a model on a model server are not available from the dashboard.

## Where changes are made

| What the dashboard once did | Where to do it now |
|-----------------------------|--------------------|
| Ask a crew a question, or answer in a discussion | `kmctl conversation ask CREW "question" [--conversation-id ID]`, or **Ask** in CrewForge |
| Delete a discussion, pause, stop or replay one, pin one | Removed from the dashboard; no kmctl or CrewForge command yet |
| Add, edit, delete or clear a crew memory fact | Removed from the dashboard; no kmctl or CrewForge command yet. Read memory with `kmctl memory export CREW` |
| Remove a crew | `kmctl delete crew NAME -n NAMESPACE` |
| Pause, resume or stop a fitness suite | `kubectl patch crewfitnesssuite NAME --type=merge -p '{"spec":{"suspend":true}}'` (`"cancel":true` to stop), or the fitness controls in CrewForge |
| Delete a fitness suite run | `kmctl delete crewfitnesssuite NAME -n NAMESPACE` |
| Pin an MCP server report verdict | `kubectl patch mcpserverreport NAME --type=merge -p '{"spec":{"adminVerdict":"use","adminNotes":"why","adminAuthor":"you"}}'` |
| Load, unload or delete a model on a model server | Removed from the dashboard; no kmctl or CrewForge command yet |

A later release signs in the person and makes every dashboard call with their own token, which would let the dashboard offer these again under the caller's rights.

## Access the dashboard needs

The chart creates a `ClusterRole` (`kubemoot-dashboard-reader`) with these grants, all read-only:

| Resources | Verbs | Why |
|-----------|-------|-----|
| `nodes`, `namespaces`, `pods` | get, list, watch | Node and namespace views |
| `pods/log` | get | Log access |
| `configmaps` | get, list | Agent prompt bundles for the Agent detail page |
| `deployments` (apps) | get, list, watch | Operator version in the Kubemoot components panel |
| All `kubemoot.ai` resources | get, list, watch | Every Kubemoot page |

Every grant is read-only: a test fails if the chart's `ClusterRole` or `Role` holds any verb but `get`, `list` and `watch`. The server also makes no write to the Kubernetes API or NATS, apart from the short-lived read consumers it opens on the discussion stream. The dashboard never reads or displays Secret values. The container runs as a non-root user with a read-only root filesystem.

### The operator report service

The dashboard reads the component status, the fitness report download, and the crew memory export from the operator's report service (port 8082). That service is plain HTTP and any pod can reach it, so every endpoint except `/healthz` requires a bearer token. The operator checks it with a TokenReview, then asks the API server with a SubjectAccessReview whether that identity may make the access the endpoint equals:

| Endpoint | Access checked |
|----------|----------------|
| `/report/{namespace}/{suite}` (fitness report) | `get` the CrewFitnessSuite in that namespace |
| `/memory-export/{namespace}/{crew}` | `get` the Crew in that namespace |
| `/componentstatuses` | `list` ModelServers in all namespaces |

The component status reports the operator, NATS, and how many ModelServers are ready. The ModelServer count is the only part that comes from cluster objects and it spans namespaces, so listing ModelServers is the narrowest access that fits it. The dashboard sends its service account token on every call, and its `kubemoot.ai` read grant above covers all three. A call with no valid token answers 401 with the header to send (`Authorization: Bearer <token>`), one whose identity lacks the access answers 403, and a service started without an authorizer answers 503 rather than serving. Any other client of these endpoints, such as a script in a pod, sends its own service account token and needs the same access; `kmctl` and CrewForge do not call them.

## HTTP API

The SvelteKit server exposes the JSON and event-stream endpoints the pages use. Every one is a GET; any other method answers 405 with `Allow: GET, HEAD, OPTIONS`. They carry no authentication, so the same rule applies as for the UI: reach them only through an authenticating proxy. List endpoints accept a `namespace` query parameter; an empty value lists across all namespaces.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/api/health`, `/api/version` | GET | Health check; dashboard version |
| `/api/namespaces`, `/api/nodes` | GET | Namespaces; Kubernetes nodes with GPU information |
| `/api/kubemoot/<plural>` and `/api/kubemoot/<plural>/<name>` | GET | List and detail for `agents`, `crews`, `models`, `modelservers`, `embeddingmodels`, `mcpservers`, `mcpgateways`, `mcpqualitypolicies`, `mcpcatalogs`, `mcpserverreports`, `ragsources`, `promptmodules`, `crewfitnesses` |
| `/api/kubemoot/config`, `/api/kubemoot/system-info`, `/api/kubemoot/topology` | GET | Configuration, system information, agent topology graph |
| `/api/kubemoot/crewfitnesssuites` | GET | Fitness suites; sub-paths under `<namespace>/<name>/` serve `scores`, `iterations`, `transcript`, and `artifact` |
| `/api/kubemoot/crew-memory?crew=&namespace=` | GET | Crew memory facts |
| `/api/kubemoot/crew-memory/export?namespace=&crew=` | GET | The crew's memory as an OMPI v0.6 zip, always screened (an `allowUnscreened` parameter is refused); `report=true` returns the selection report as JSON (left-out entries by key or opaque id and reason, never text) |
| `/api/kubemoot/watch/<plural>` | GET | Kubernetes resource updates as server-sent events |
| `/api/nats/subscribe?subject=` | GET | Server-sent events for a NATS subject |
| `/api/nats/history?stream=&subject=&limit=` | GET | JetStream history replay as a JSON array |
| `/api/nats/config`, `/api/nats/kv`, `/api/nats/stream` | GET | NATS connection status, the agent-state key-value bucket, and stream replay |

The browser talks to the SvelteKit server with server-sent events. Only the server holds a NATS connection (the `nats` client over TCP), so the browser never connects to NATS directly, and the server only reads from it.

## Related

- [Observability](../observability/)
- [MCPServer guide](../../reference/mcpserver-guide/)
- [RAGSource guide](../../reference/ragsource-guide/)
- [Agent reference](../../reference/agent/)
