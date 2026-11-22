---
title: "Kubemoot Dashboard"
description: "The Kubemoot Dashboard is a web UI that lists resources across namespaces, shows GPU and model state, streams live agent discussions and NATS messages, and lets a signed-in person ask a crew and manage their own conversations."
weight: 2
---

## Overview

The Kubemoot Dashboard is a web UI for looking at a running Kubemoot installation. It reads, and the few changes it makes (asking a crew, and acting on a conversation) it makes as you. It lists Kubemoot resources across namespaces, shows GPU node and model state, and streams agent discussions and NATS messages as they happen. It is a SvelteKit application that reads the Kubernetes API and the NATS bus from the server side; the browser never connects to either directly.

You sign in with a Kubernetes bearer token, the way you do in Headlamp, and the dashboard then acts as you: every Kubernetes call it makes for you uses your token, so your own RBAC decides what you see, and you can ask a crew a question as the same Kubernetes user `kmctl` acts as. See [Signing in](#signing-in).

The dashboard acts as you and only through the crew's discussion gateway: you can ask a question, follow up, stop, pause or resume a conversation, and delete one, each with your own token, so the gateway's access rules and conversation ownership apply. It cannot edit memory, load or unload a model, or control a fitness suite; those changes are made with `kmctl` or CrewForge (`kubectl` only where `kmctl` has no command), and where a control used to be, the page shows the command to run, with a **Copy** button. See [Where changes are made](#where-changes-are-made).

Metrics, logs, and traces are not part of the dashboard. For those, see [Observability](../observability/).

## Install

The dashboard is an optional part of the `kubemoot-operator` chart, off by default. It needs a sign-in, but the NATS views still read with the dashboard's own connection (see [What runs as you](#what-runs-as-you)), so enable it on a cluster you control and read [SECURITY.md](https://github.com/kubemoot/kubemoot/blob/main/SECURITY.md). Turn it on with one flag when you install the operator:

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

The dashboard asks every visitor to sign in with a token, whether or not the chart publishes the route. Serve it over HTTPS: the session cookie is `Secure`, and a token pasted over plain HTTP crosses the network readable. When a proxy ends TLS in front of the dashboard, make sure the dashboard sees the public origin (adapter-node's `ORIGIN` environment variable, or its protocol and host header variables), because every request that changes something must carry an `Origin` that matches it. Sign-in through your identity provider (OIDC behind an OAuth2 proxy) is planned and will use the same seam.

## Signing in

Every page shows the sign-in page until you sign in. Paste a Kubernetes bearer token and press **Sign in**. The dashboard checks it with the API server (a TokenReview) and refuses one that the cluster does not accept or that has expired. With `kmctl` you can print one for your current context:

```bash
kmctl dashboard token                   # eight hours, for the user the current context acts as
kubectl create token SERVICE_ACCOUNT -n NAMESPACE --duration=8h
```

Once you are signed in the top bar shows **Signed in as** your Kubernetes user name, and **Sign out** forgets the token.

How the token is kept:

- It lives only in the dashboard server's memory, in a session keyed by a random id. The browser holds just that id, in a cookie that is `HttpOnly` (page scripts cannot read it), `Secure`, `SameSite=Strict` and limited to the dashboard's path. The token is never written to disk, a log or a trace, and never reaches the browser's scripts.
- The session ends at the token's own expiry when the token is a JWT with one, and in any case after `auth.sessionMaxMinutes` (8 hours by default). Signing out ends it at once.
- A restart of the dashboard pod signs everyone out. With more than one replica, use session affinity so a person's requests reach the pod that holds their session.
- Every request that changes something (sign-in, sign-out, asking a crew) must come from the dashboard's own origin; a request from another site is refused. Behind a proxy that ends TLS, set `auth.origin` to the public origin (or `auth.trustForwardedHeaders` when the proxy sets `X-Forwarded-Proto` and `X-Forwarded-Host`); the 403 says which origin it expected.
- Open streams (live lists, discussion streams, the answer to a question) end when the session does, whether by sign-out or expiry.
- Signing in again replaces the browser's earlier session. One user may hold 20 sessions at once, and sign-in attempts are limited (10 a minute per client address, 120 a minute in all) because each one costs the API server a TokenReview.
- The cookie is `Secure` except on a plain-http `localhost` port-forward, which Safari would otherwise refuse.

### What runs as you

Every Kubernetes API call the dashboard server makes while you are signed in uses your token, not the dashboard's ServiceAccount: lists, gets and watches of Kubemoot resources, nodes, namespaces and ConfigMaps, the operator's version panel, and the calls to the operator's [report service](#the-operator-report-service), which checks your token with a TokenReview and a SubjectAccessReview. A call your RBAC refuses shows "You do not have permission to see this" in the view, not an error page.

A few things do not run as you:

| What | Why |
|------|-----|
| NATS reads (discussions, history, streams, memory, key-value views, fitness transcripts) | The dashboard has its own read-only NATS connection, which the bus cannot tie to you. So that your RBAC still decides, a NATS-backed view shows a namespace only when your own token may list Crews there (the dashboard asks the API server with your token and rechecks every ten seconds). To see the NATS views, you need `list` on `crews` cluster-wide; without it they show nothing rather than everything. Per-crew NATS accounts, which would let the bus itself check who is reading, are a later step |
| The sign-in check itself | The dashboard's ServiceAccount creates the TokenReview, its only right by default |
| The health probe (`/api/health`) | It carries no data and must answer without a session |
| The namespace scope of a [workshop instance](#a-workshop-instance) | It is the instance's own setting, resolved with that instance's ServiceAccount |

### Asking a crew

On the Conversations page a signed-in person starts a conversation with **+ New** (the crew comes from the top-bar selector; with none selected the page asks which crew) and asks or follows up from one compact field at the bottom of an open conversation (Enter sends, Shift+Enter starts a new line). The question goes through the crew's discussion gateway with your token (`Authorization: Bearer`), exactly as `kmctl conversation ask` sends it, so the gateway's access rules and conversation ownership apply and the conversation belongs to you. The dashboard also needs `get` on the Crew with your token before it sends anything. The gateway's refusals appear next to the field with their status: 401 (the crew did not accept your token), 403 (no permission to use the crew), 404 (the conversation is not yours). The answer streams back through the gateway as you, and the conversation opens as soon as the bus shows it. **Mine** limits the conversation list to the ones the gateway lists for you.

The dashboard reaches a crew's gateway at `http://<crew>-discussion.<namespace>.svc.cluster.local`. That hop is plain HTTP inside the cluster, so your token crosses the pod network unencrypted; set `auth.gatewayUrl` to an `https` address (a service mesh or a gateway with TLS) when your network is not trusted. If you use network policies, allow the dashboard's namespace in the crew's `spec.network.allowedIngress`. Set `auth.gatewayUrl` to reach gateways another way.

### Filtering and searching conversations

The Conversations list and the Moot list share one bar above them: a search field with a **?** tooltip for its syntax, a status menu (active, closed, failed), **Mine** (once you are signed in) and a date range; the crew is the top-bar selector. **+ New** sits beside the search field on Conversations. Each filter set shows as a chip you remove with one click, and all of them live in the page address (`?status=failed&mine=1&from=2026-11-01&to=2026-11-21&q=gpu`), so a link reproduces the view. **Mine** uses the conversations each crew's gateway lists for you, for the selected crew or, with none selected, every crew; a crew you may not use adds none.

The search takes a small syntax, the same on both pages. Words separated by spaces must all match, without regard to case, in the question and the messages of a conversation: `namespace` finds conversations containing "namespace". A leading `-` excludes: `namespace -smoke` keeps those matching "namespace" and drops any whose question or messages contain "smoke". A quoted phrase matches as a phrase, and `-"..."` excludes it. An empty query, or one of only exclusions such as `-smoke`, works too; a stray `-` is ignored.

Search runs in the browser over the discussions the page has loaded from the bus (the newest ones, with older pages loaded on request) and highlights the matching text in the Conversations list. It does not reach conversations the page has not loaded; the bar says how many it is searching and offers **Load older**. It covers only what the bus still holds, which retention limits (see [Data retention](../data-retention/)).

### Acting on a conversation

The **Actions** menu (the hamburger beside the open conversation's status) holds, in this order: **Show answer details**, **Copy conversation**, **Copy conversation id with the first question**, **Download conversation**, **Open in the Moot**, **Copy kmctl command** (a command with the real crew, namespace and conversation id), then **Pause** (or **Resume** while paused) and **Stop** (only while the conversation is running), then **Delete conversation**. Each goes through the crew's discussion gateway with your token, the same routes `kmctl` and CrewForge use: `POST /api/v1/discussions/{crew}/{conversationId}/stop|pause|resume` and `DELETE /api/v1/discussions/{crew}/{conversationId}`.

- **Delete** asks in the page first (Yes, delete or No), never with a browser dialog. The gateway then removes the conversation from every store that holds it, and the page shows its report: what was removed from each store (history, ownership, discussion messages, workspace files, coordinator events, scheduled follow-ups, crew memory facts), and what the gateway cannot remove (a question still queued, logs and backups). When a store could not be cleared the gateway answers 500 with the same report; the page says which stores and offers **Repeat delete**, which removes the rest.
- A conversation that is still running answers 409. The page then offers **Stop and delete**, which asks the gateway to stop it first (`force=true`).
- **Acting on someone else's conversation** needs the operator right the gateway checks (update or delete on the crew's conversations) and a reason of 1 to 200 characters, sent in the `Kubemoot-Access-Reason` header and recorded in the gateway's audit log. The page asks for the reason when the gateway says one is needed. A conversation that is not yours and that you may not operate on answers as not found.

### Anonymous read-only viewing

Set `auth.anonymousReadOnly: true` (`dashboard.auth.anonymousReadOnly` in the operator chart) for a public read-only view. A visitor who has not signed in then sees the read views with the dashboard's own ServiceAccount, which the chart grants the read-only access listed under [Access the dashboard needs](#access-the-dashboard-needs). Such a visitor also reads the fitness reports and component status the operator serves, with the dashboard's ServiceAccount token. Such a visitor can never ask or change anything: the asking routes and the gateway routes need a session, and every method but GET, HEAD and OPTIONS is refused. The default is `false`, and then the chart gives the ServiceAccount only the right to create TokenReviews.

### A workshop instance

The workshop instance (`workshop.enabled`) keeps its port-forward use: it reads with its own scoped, read-only ServiceAccount and has anonymous read-only viewing on. Attendees may still sign in to ask.

## What it shows

The sidebar groups pages by what they cover. The Topology page exists by URL (`/topology`) without a sidebar entry.

The sidebar shows the dashboard's version under the logo. A release candidate (`X.Y.Z-rc.N`) carries an RC badge beside it; a published release shows none.

The info icon next to the version opens the Kubemoot components panel. Its first line, `Kubemoot`, is the release the cluster runs: the version of the operator chart, which the operator writes to the `KubemootConfig` status (`status.release`) and the dashboard reads from there. Below it come the dashboard's own version, the operator version, and the operator's uptime. A cluster whose chart was never stamped with a release reads `development build`. The line reads `not reported` while the operator has not written its status yet, and on an operator too old to report a release. The Copy button puts the panel's lines on the clipboard for a support request.

| Section | Pages | Shows |
|---------|-------|-------|
| Top | Overview, Nodes | Ready-of-total counts for nodes, GPUs, crews, and every other resource type (a tile shows a small spinner until its first count arrives, never a zero); Kubernetes nodes with GPU detection and model-loading state. The first tile is NATS: whether it is connected, its server version, JetStream storage in use against its limit, message and byte totals, and a warning band when a store nears a limit; it opens the NATS page. The Overview has the same **Crew** selector as the other pages: it narrows the counts to one crew, while nodes, model servers, models, and embedding models are shared by every crew and always counted in full |
| Messaging | Conversations, Moot | The conversations people have had with a crew, which you can ask, follow up, stop and delete; the Moot, where the agents deliberate over each question, read-only |
| Crews | Crews, Agents, Prompts, Memory, Fitness | Crew and agent configuration, PromptModule contents, crew memory, and fitness suite runs |
| Models | Models, Model servers, Embeddings | ModelServer backends with their calibration (phase and, per model, fit, load time, generation, prompt, and parallel speed; an embedding model shows as skipped and a model that could not be measured as failed, each with its reason and no speeds), Model state, and EmbeddingModel configuration. Model servers belong to the cluster, so the Model servers page lists them all whatever crew is selected. The Models page also lists each ModelServer's catalog (the models a crew selects by label), because crews ship no Model resources |
| Tools | MCP servers, MCP gateways, Catalogs, Quality policies, Reports | MCPServer instances and tools, gateway routing, quality policies, catalog discovery, and quality verdicts |
| Knowledge | RAG Sources | RAGSource indexing status with document and chunk counts |
| System | Config, NATS | The `KubemootConfig` cluster singleton; what NATS holds against its limits (see [NATS](#nats)) |

### Refresh and resource views

Pages refresh on a timer. A page shows its loading indicator only until its first data arrives; a later refresh runs in the background and swaps the new values in place, and a refresh that fails keeps the last data on screen.

An MCP server's tile and detail page show its `spec.description`, a sentence on what the server is for, when the manifest sets one.

An MCP server reached with a key shows **key from Secret `<name>`** (from `registry.authSecretRef`). The dashboard names the Secret and never reads or shows its value.

The Kubemoot logo and **Powered by Kubemoot** open [kubemoot.org](https://kubemoot.org) in a new tab.

### Overview and YAML tabs

Every page that shows one Kubemoot resource (a crew, agent, PromptModule, model server, MCP server, fitness suite, the `KubemootConfig`, and so on) has two tabs under its title: **Overview**, the page as described above, and **YAML**. The tab is in the URL (`?tab=yaml`), so a link opens the YAML directly; the arrow keys, Home and End move between the tabs.

The YAML tab shows the resource as Kubernetes returned it, read-only, in a monospace block with line numbers and syntax colors. Long lines scroll sideways rather than wrapping, and the block is reachable by keyboard. **Copy** puts exactly the text shown on the clipboard and reads **Copied** for a moment. `metadata.managedFields` is hidden; **Show managed fields** adds it back (the button appears only when the resource has any). Everything else, including `status`, is shown. The values are never changed, except that the contents of a Secret (`data` and `stringData`) are never shown; none of the Kubemoot kinds hold one.

The fitness suite list links each suite's name to its own page with these two tabs. Kubernetes kinds that are not Kubemoot's, such as Nodes, have no YAML tab.

### Status colors

| Badge | Meaning |
|-------|---------|
| Green | Ready or loaded |
| Yellow | Pending or pulling |
| Red | Error or failed |
| Gray | Status unavailable |

### The Moot

The Moot is the read-only page where the agents deliberate over each question; it is the old Discussions page, and `/discussions` redirects to `/moot` (the query string is kept). Each deliberation shown there is a discussion, the term the [concepts docs](../../concepts/conversations-turns-and-threads/) use, and it is carried by a thread. The page is a two-panel thread viewer. The left panel lists threads, newest first, each tagged with its crew and channel. The right panel shows the message timeline for the selected thread: the question, each agent's contribution, and the synthesis. The page loads the newest 200 sequence numbers of the stream to list the newest threads, and new messages arrive live. Opening a thread reads that thread's own subject, so it shows complete even when it began before the loaded history, and scrolling the list to its end (or **Load older threads**) reads the next older page. A thread that began before the loaded page shows its question and counts once its own subject has been read, which happens at once for the threads at the edge of a page.

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

An open discussion shows the question, then the crew's answer, then the span graph (when on) and the messages. The answer comes first: it is the synthesis with the time it took, any "some sources could not be reached" note, and a collapsed **How this answer was made** summary. With no answer yet the panel says so plainly: still working, stopped, or failed with the reason. A small status icon says whether the discussion is active, answered, failed or stopped; its tooltip also gives the crew version (without the build suffix) and who started it. The crew name shows beside it only when the top-bar crew selector is on All crews, and the channel only when it is not `general`. One **Actions** (hamburger) menu holds **Show timeline**, **Debug detail**, **Copy discussion** (as Markdown), **Copy thread id with the question**, **Download conversation** (see [Downloading conversations](#downloading-conversations)), and **Copy kmctl follow-up command** and **Copy kmctl replay command**, which copy the full command with this conversation's id, crew and namespace filled in. The menu works from the keyboard: arrow keys move between items, Escape closes it. The page cannot start, answer, pause, stop, replay or delete a discussion; the copied commands do that from a terminal, and an empty list shows the command that starts a discussion.

The Moot only reads: to ask, follow up, stop, pause, resume or delete, use the Conversations page, which links here with **Open in the Moot**. Its open-thread header is laid out like the Conversations page: the crew, the status, and the toolbar.

The span graph draws one bar per agent from its first to its last message. Each bar names the ModelServer that ran the agent's latest call, taken from the placement the messages carry. A server the scheduler weighed but did not choose is not named, and the label of an agent's static endpoint, which a message sent without a placed call carries, is not shown as a server. A thread started straight on the bus, such as a smoke test, may carry messages with no timestamp. The graph draws the agents whose start and end are known and labels each of the others "no timing recorded" instead of drawing a bar.

### NATS

The tile on the Overview and the **NATS** page under System show what NATS JetStream holds and how close each store is to its limit. Everything is read, nothing changes, and the numbers refresh on the dashboard's refresh interval.

**Tile.** Connected or not (with the reason when not), the server version, file storage and memory in use against the account's limit, the message and byte totals, and a warning band. A store that refuses writes at its limit (a KV bucket, or a stream set to discard new messages) is in the band when its bytes or its messages reach 80% of its limit (95% is marked critical); it is the one to look at first. A stream that drops its oldest messages at its limit (discard old) is meant to sit at its cap, so it is never in the band. When such a stream's cap is full and its oldest message is younger than its max age, the page says how much history it keeps against what it is meant to keep, for example "KUBEMOOT_OPERATOR keeps 6 hours of the intended 7 days, because its message cap is full"; the cap, not the age, then decides how much history there is. KV history is shown on the page as how full each key's revisions are, and is not a warning: a key that is rewritten often sits at its depth by design.

**Page.**

- **Server and account:** version, the JetStream account's storage, memory, stream and consumer limits and use, and the uptime and connection count when the server's monitoring port (`NATS_MONITOR_URL`, else port 8222 of the NATS host) answers. Without it they read as absent.
- **Every store Kubemoot creates:** messages, bytes and the share of the byte limit, max age, max bytes, max messages, KV history, first and last sequence, consumers, who owns the data (a crew, system learning, or Kubemoot), whether it can hold people's data, and the Crew field that shortens how long a crew's data stays. Stores the install does not hold are listed as not created.
- **Per crew:** each crew's share of the stores keyed by crew (`<namespace>.<crew>`): messages, keys and bytes per stream, KV bucket and object store, and the crew's last retention cleanup from its status (`status.retention`: when it ran and what it removed). Counts come from subject-filtered stream info, so they are exact. Bytes in a stream or KV bucket are the crew's share of the messages applied to the store's bytes, shown as "about", because NATS does not report bytes per subject; object store sizes are exact. At most 5000 subjects or objects count per crew and store, and a crew above that shows "more than" its count. The page therefore costs the same however large a bucket is.

On an unscoped dashboard the store figures are cluster-wide totals and the tile says so. In a namespace-scoped dashboard the page shows only the stores that instance serves (the discussion stream and the agent heartbeat bucket), the crews in its namespaces, and for each store only those namespaces' share, read with their subject filters (bytes are an estimate); the whole store's sequences and consumers are not shown, and no store warning is raised from a share. A history page on a scoped dashboard skips older windows that hold none of its namespaces' messages, so a page has messages or paging ends.

**One definition of the stores.** What each store holds, whether it can hold people's data, and who owns it come from one file, `dashboard/src/lib/nats/nats-stores.json`, which the page reads. A test in the dashboard fails when the tables in [Data ownership](../data-ownership/#every-store-kubemoot-creates) or the stores the operator chart's bootstrap job creates (with their size limits) disagree with it, so the page and the docs cannot drift. Change a store in the bootstrap job, the file and the docs table together.

**Cost of reading history.** The dashboard records how long each discussion history read takes: the response carries a `Server-Timing` header, the server logs one line per read with the scope (`stream` or `thread`), message count, response bytes and durations, and never a subject, thread id or message text, and the NATS page shows the last read and the average and slowest of the last 100.

### Memory

Shows the working memory of the crew selected in the top bar, the same crew picker the Moot uses. Working memory is what a crew has learned about the cluster and keeps between discussions, stored as facts with a topic, a key and a value; the least recently used are dropped over time. With All crews selected the page asks you to pick a crew. A crew with no facts shows an empty state that explains this. The page only reads: facts are written by the crew's agents or by you with `kmctl`, and the page shows the commands: `kmctl memory add|edit|delete|clear CREW ...` to change facts and `kmctl memory export CREW` to take a copy.

**Export** (shown whenever a crew is selected) opens a panel that downloads the crew's memory as a zip in the OMPI (Open Memory Protocol Initiative) interchange format, working spec v0.6: a manifest and one memory object per fact. The operator builds the file with the same code as `ompi-export`; the dashboard only relays it and never maps facts to the format itself. The export is always screened: a fact whose topic, key or value holds a credential is left out. Before you download, the panel shows what the export holds and a table of what it left out, each entry named by its key (an opaque id such as `<namespace>.<crew>.#1a2b3c4d` when the key itself held the credential) and the reason, for example the shape of the credential. It never shows a fact's text. The dashboard has no unscreened export: it calls the operator with its own service account, not your identity, so it cannot decide what you may see beyond the namespace scope. To export facts that hold a credential, use `kmctl memory export --allow-unscreened`, which runs with your own credentials. The same namespace scope as the rest of the page applies; a crew outside it answers 404. The dashboard sends its service account token to the operator, which checks it with a TokenReview and allows the export only when that identity may `get` the Crew (a SubjectAccessReview); the dashboard's ClusterRole already grants read on every Kubemoot kind. A caller without a valid token is refused (401) and one without that access is refused (403), so a pod that can reach the operator's report port cannot read a crew's memory. Every other report endpoint is checked the same way; see [The operator report service](#the-operator-report-service).

### Conversations

Your questions and the crew's answers. The page has the same layout as the Moot: the title and a one-line subtitle top-left, the filter bar and the list of conversations on the left (each titled by its first question), and the open conversation on the right under a header with the crew, the status, a copy icon and one **Actions** menu. **+ New** starts a conversation. The open conversation shows every turn of the conversation in order, each your question then the crew's answer (markdown), with one compact field at the bottom to follow up; it updates live as the crew answers. A follow-up is a new thread under the same conversation, and the page merges them, so the list has one entry per conversation. Each list entry and the open conversation's header name the crew. The open conversation is in the page address (`?conversation=<id>`), so a link shares it and a reload keeps it, and opening one never changes the top-bar **Crew** selector, which stays as you set it. Nothing of the agents' internal discussion appears here; **Open in the Moot** shows the consensus messages and statistics. See [Asking a crew](#asking-a-crew) and [Acting on a conversation](#acting-on-a-conversation).

The checkbox beside each conversation in the list (with a crew selected) selects several for one **Download N conversations**; see [Downloading conversations](#downloading-conversations).

**Show answer details** in the Actions menu is off by default and the dashboard remembers the choice per viewer. When it is on, each answer shows the time from the question to the answer and the tokens used: the agents' tokens plus the coordinator's. A figure the thread did not record is left out. To watch raw NATS subjects, use `kmctl` or the NATS tooling directly. When the dashboard cannot read its own version, the sidebar and an exported discussion say "version unknown".

The older address `/messages` redirects to `/conversations`.

### Crews

One card per crew, linking to the crew's detail page. The dashboard does not add or remove crews: create them with CrewForge or `kmctl`, and remove one with `kmctl delete crew NAME -n NAMESPACE` (the operator's finalizer and owner references remove its Agents and open discussions). The page shows that command.

### Reports

MCPServerReport quality verdicts (`use`, `caution`, `avoid`) with the evaluation history, and any verdict an administrator pinned. The page shows the `kmctl mcp report pin REPORT --verdict use|caution|avoid` command that pins one, and `kmctl mcp report unpin REPORT` when one is pinned. Both run with your credentials, so they need `patch` on `mcpserverreports`.

### Fitness

CrewFitnessSuite runs and their scores. Opening a run shows the commands that control it: `kmctl fitness pause|resume|stop SUITE -n NAMESPACE`, which set `spec.suspend` (pause and resume) or `spec.cancel` (stop) (see the [CrewFitnessSuite reference](../../reference/crewfitnesssuite/#pause-resume-and-stop)), and `kmctl delete crewfitnesssuite` to remove the run with its owned CrewFitness resources. The status badge reads `Pausing` while a paused suite's last iteration finishes and `Stopping` until the operator applies a stop.

### Downloading conversations

The Conversations page and the Moot can download the conversations of a crew as a zip. The operator builds the zip from the stores it reads, the same file `GET /conversation-export/{namespace}/{crew}` returns (see [Taking a conversation away](../data-ownership/#taking-a-conversation-away)); the dashboard relays it and keeps no copy. The file name comes from the operator: `<crew>-conversation-<id>.zip` for one conversation, `<crew>-conversations.zip` for several. The zip holds the turns, the receipts and workspace files still stored, the crew memory learned in that conversation (screened), and a manifest. The times are written in your browser's time zone.

- **One conversation.** Conversations and the Moot have **Download conversation** in the **Actions** menu of the open item. A thread from before conversations were recorded on it has no conversation ID and shows no button.
- **Several.** Conversations lists a checkbox beside each conversation when a crew is selected. Ticking some shows **Download N conversations** above the list; the answer is a zip of zips with a manifest that lists every conversation as included, refused, expired or failed. A download is for one crew, so the checkboxes are not offered with All crews selected.

The same namespace scope as the rest of the dashboard applies (a crew outside it answers 404). The dashboard asks the operator with its own service account token, which the operator checks with a TokenReview and a SubjectAccessReview that the account may `get` the Crew; the dashboard's `ClusterRole` already grants that, so the export needs no extra access. The route answers 400 for a missing or invalid ID (or more than 50), 403 when the operator refuses, 404 when the operator holds no such conversation, 503 when conversation export is not available, and 502 when the operator cannot be reached or fails. An `allowUnscreened` parameter is refused, as in the memory export.

The export decides who may take a conversation in one place in the operator. Today that rule is the access already checked for the crew. When the export checks the conversation's owner (a conversation belongs to the caller who started it on a crew whose gateway verifies callers), the dashboard's service account is not that owner, so a conversation with an owner is refused to it and listed as `refused` in the manifest, unless the account is given the operator role that may read other callers' conversations (`kubemoot-conversation-reader`, bound to no one by default). The chart does not bind it, and the dashboard stays read-only and unprivileged; to take away a conversation a person owns, that person uses their own credentials.

### Models

The Models and Model servers pages show state only. Removing a model from the catalog is `kmctl delete model NAME`; the dashboard changes nothing. See [Where changes are made](#where-changes-are-made).

## Where changes are made

| What you want to do | Where to do it |
|---------------------|----------------|
| Ask a crew a question, or follow up in a conversation | The Conversations page when you are signed in, `kmctl conversation ask CREW "question" [--conversation-id ID]`, or **Ask** in CrewForge |
| Ask a past conversation's first question again | `kmctl conversation replay CREW ID` |
| Stop, pause, resume or delete a conversation | The Conversations page when you are signed in (the **Actions** menu), or CrewForge |
| Add, edit, delete or clear a crew memory fact | `kmctl memory add\|edit\|delete\|clear CREW ...`. Read memory with `kmctl memory export CREW` |
| Remove a crew | `kmctl delete crew NAME -n NAMESPACE` |
| Pause, resume or stop a fitness suite | `kmctl fitness pause\|resume\|stop SUITE`, or the fitness controls in CrewForge |
| Delete a fitness suite run | `kmctl delete crewfitnesssuite NAME -n NAMESPACE` |
| Pin or unpin an MCP server report verdict | `kmctl mcp report pin REPORT --verdict use\|caution\|avoid` and `kmctl mcp report unpin REPORT`; read reports with `kmctl mcp report list` and `kmctl mcp report get` |
| Start a fitness suite run from a manifest | `kmctl apply -f SUITE.yaml -n NAMESPACE` |
| Delete a model from the catalog | `kmctl delete model NAME` |
| Load a model | Not offered. A model loads on demand when a crew needs it |
| Unload a model | No command yet |

Every command that changes something runs with your own credentials and accepts `--dry-run`, which has the API server check the change and your access and store nothing.

Asking a crew, and stopping, pausing, resuming or deleting its conversations, are the changes the dashboard makes for you, through the crew's gateway with your own token. The others stay in `kmctl`; offering them in the dashboard under your rights is a later step.

## Access the dashboard needs

While people sign in, the dashboard's own ServiceAccount holds one right: `create` on `tokenreviews` (authentication.k8s.io), to check a pasted token. People read with their own token, so their access is whatever their RBAC grants. The read-only grants below are created only when `auth.anonymousReadOnly` is true (the instance then reads with its ServiceAccount for visitors who have not signed in); the chart creates a `ClusterRole` (`kubemoot-dashboard-reader`) with these grants:

| Resources | Verbs | Why |
|-----------|-------|-----|
| `nodes`, `namespaces`, `pods` | get, list, watch | Node and namespace views |
| `pods/log` | get | Log access |
| `configmaps` | get, list | Agent prompt bundles for the Agent detail page |
| `deployments` (apps) | get, list, watch | Operator version in the Kubemoot components panel |
| All `kubemoot.ai` resources | get, list, watch | Every Kubemoot page |

Every grant is read-only apart from the TokenReview right: a test fails if the chart's `ClusterRole` or `Role` holds any other verb than `get`, `list` and `watch`. The server also makes no write to the Kubernetes API or NATS, apart from the short-lived read consumers it opens to read history and list objects. The dashboard never reads or displays Secret values. The container runs as a non-root user with a read-only root filesystem.

### The operator report service

The dashboard reads the component status, the fitness report download, and the crew memory export from the operator's report service (port 8082). That service is plain HTTP and any pod can reach it, so every endpoint except `/healthz` requires a bearer token. The operator checks it with a TokenReview, then asks the API server with a SubjectAccessReview whether that identity may make the access the endpoint equals:

| Endpoint | Access checked |
|----------|----------------|
| `/report/{namespace}/{suite}` (fitness report) | `get` the CrewFitnessSuite in that namespace |
| `/memory-export/{namespace}/{crew}` | `get` the Crew in that namespace |
| `/conversation-export/{namespace}/{crew}?id=...` | `get` the Crew in that namespace; see [Taking a conversation away](../data-ownership/#taking-a-conversation-away) |
| `/componentstatuses` | `list` ModelServers in all namespaces |

The component status reports the operator, NATS, and how many ModelServers are ready. The ModelServer count is the only part that comes from cluster objects and it spans namespaces, so listing ModelServers is the narrowest access that fits it. The dashboard sends the signed-in person's own token on every call, so their RBAC decides; for an anonymous read-only visitor it sends its service account token, which the `kubemoot.ai` read grant above covers. A call with no valid token answers 401 with the header to send (`Authorization: Bearer <token>`), one whose identity lacks the access answers 403, and a service started without an authorizer answers 503 rather than serving. Any other client of these endpoints, such as a script in a pod, sends its own service account token and needs the same access; `kmctl` and CrewForge do not call them.

## HTTP API

The SvelteKit server exposes the JSON and event-stream endpoints the pages use. Every one is a GET except sign-in, sign-out, asking a crew and acting on a conversation (stop, pause, resume and delete); any other method answers 405 with `Allow: GET, HEAD, OPTIONS`. Without a session they answer 401 (the sign-in page for pages), unless anonymous read-only viewing is on, which serves reads only. A Kubernetes call the signed-in person's RBAC refuses answers 403 with a plain message. List endpoints accept a `namespace` query parameter; an empty value lists across all namespaces.

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/signin`, `/signout` | POST | Sign in with a pasted token (form field `token`); sign out. Same-origin only |
| `/api/gateway/<namespace>/<crew>/ask` | POST | Ask the crew as the signed-in person: `{message, conversationId?, timeZone?, localDateTime?}`; answers `{conversationId, requestedAt}` or the gateway's refusal with its status |
| `/api/gateway/<namespace>/<crew>/conversations` | GET | The conversations the signed-in person started, listed by the gateway |
| `/api/gateway/<namespace>/<crew>/<conversationId>/stream` | GET | The answer as server-sent events, relayed from the gateway; refused for a conversation that is not theirs |
| `/api/health`, `/api/version` | GET | Health check (open, no session needed); dashboard version |
| `/api/namespaces`, `/api/nodes` | GET | Namespaces; Kubernetes nodes with GPU information |
| `/api/kubemoot/<plural>` and `/api/kubemoot/<plural>/<name>` | GET | List and detail for `agents`, `crews`, `models`, `modelservers`, `embeddingmodels`, `mcpservers`, `mcpgateways`, `mcpqualitypolicies`, `mcpcatalogs`, `mcpserverreports`, `ragsources`, `promptmodules`, `crewfitnesses` |
| `/api/kubemoot/config`, `/api/kubemoot/system-info`, `/api/kubemoot/topology` | GET | Configuration, system information, agent topology graph |
| `/api/kubemoot/crewfitnesssuites` | GET | Fitness suites; `<namespace>/<name>` is one suite; sub-paths under it serve `scores`, `iterations`, `transcript`, and `artifact` |
| `/api/kubemoot/crew-memory?crew=&namespace=` | GET | Crew memory facts |
| `/api/kubemoot/crew-memory/export?namespace=&crew=` | GET | The crew's memory as an OMPI v0.6 zip, always screened (an `allowUnscreened` parameter is refused); `report=true` returns the selection report as JSON (left-out entries by key or opaque id and reason, never text) |
| `/api/kubemoot/conversation-export?namespace=&crew=&id=` | GET | One conversation as a zip, or a zip of zips with a manifest when `id` is repeated (at most 50); `report=true` returns the manifest as JSON; `timeZone` sets the zone of the times. The operator names the file |
| `/api/kubemoot/watch/<plural>` | GET | Kubernetes resource updates as server-sent events |
| `/api/nats/subscribe?subject=` | GET | Server-sent events for a NATS subject |
| `/api/nats/history?stream=&subject=&limit=&before=` | GET | JetStream history as a JSON array: the newest `limit` sequence numbers of a pattern, the `limit` before sequence `before` to page back, or every message of one thread when `subject` names it. Answers `windowStart` (the next `before`) and `hasMore`; `Server-Timing` reports the cost |
| `/api/nats/status`, `/api/nats/crews` | GET | The NATS tile and page: server, account and every store; each crew's share of them and its last retention cleanup |
| `/api/nats/config`, `/api/nats/kv`, `/api/nats/stream` | GET | NATS connection status, the agent-state key-value bucket, and stream replay |

The browser talks to the SvelteKit server with server-sent events. Only the server holds a NATS connection (the `nats` client over TCP), so the browser never connects to NATS directly, and the server only reads from it. That connection is the dashboard's own, not yours (see [What runs as you](#what-runs-as-you)).

## Related

- [Observability](../observability/)
- [MCPServer guide](../../reference/mcpserver-guide/)
- [RAGSource guide](../../reference/ragsource-guide/)
- [Agent reference](../../reference/agent/)
