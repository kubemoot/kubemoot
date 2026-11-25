---
title: The Crew Liaison
weight: 10
description: How a crew becomes one agent for any MCP client, and why the answer comes by ticket.
---

A crew is a committee. From inside the cluster you can watch it work: agents
convening, findings arriving, signals settling. From outside, a client should not
have to. The crew liaison is the operator's door for other agents: one MCP endpoint
that fronts every crew in the cluster, takes a question, and hands back the crew's
synthesized answer.

## What the client sees

Four tools, and nothing about the discussion's substance.

| Tool | What it does |
|---|---|
| `list_crews` | The crews that can be asked: name, namespace, what each is for, whether it is ready. |
| `ask` | Starts a discussion with a crew and returns a ticket. It waits up to 45 seconds first, so a quick crew answers in the same call. |
| `get_answer` | Reports a ticket: `pending`, `answered` with the answer, or `failed` with the reason. It too waits up to 45 seconds, returning the moment the answer exists. |
| `export_conversation` | Takes a conversation away as one zip: the turns, receipts, workspace files and the memory facts learned in it, with a manifest of what had expired. |

`export_conversation` takes the crew, `conversationIds` (the `conversationId` that `ask` and `get_answer` return; several ids return a zip of zips with a manifest listing each as included, refused or expired) and an optional `timeZone` for the times in the file. The liaison asks the operator's [conversation export](../../operating/data-ownership/#taking-a-conversation-away) with the caller's own Kubernetes token when it verifies tokens (see below), so the operator's owner check applies to the caller, and otherwise with its own service account, and the zip is built for that call and never stored. The result carries the zip as an embedded resource when it is at most 48 KiB, because a model client reads a tool result as text and caps how much of it it takes in. A larger zip is not returned: the result carries the manifest and a note naming the other ways to take it (`kmctl conversation export`, or the operator endpoint). The chart sets `LIAISON_OPERATOR_REPORT_URL` to the operator's report service, which serves TLS from the install CA; the liaison verifies it against that CA (`LIAISON_CA_FILE`) before it sends the token, and refuses a URL that is not https unless `LIAISON_ALLOW_INSECURE_REPORT=true` accepts it. Without the URL the tool answers that export is not configured. See [encrypted calls that carry credentials](../../operating/encrypted-internal-calls/).

`ask` takes the crew and the question, and these optional arguments: `namespace` (when the crew name is not unique), `waitSeconds`, `ticket` (a retry rejoins its discussion), and `timeZone`, the asker's IANA time zone such as `America/New_York`. The liaison forwards the zone with the question and keeps it nowhere; the crew states the asker's date in it. A zone that is not in the IANA time zone database is dropped, and the crew uses its default zone. A client that knows where its person is (a chat client with the person's locale, a script) should send it.

The question goes in and the synthesis comes out. Agent names, consensus signals,
findings, tool output, and retrieved context stay inside the cluster. A pending
ticket does carry one number, the count of contributions gathered so far, so a
client can tell a live discussion from a stalled one. It names nobody.

## Why a ticket

A crew deliberates for one to several minutes. Most MCP clients time a tool call out
before that; Claude Code's default is sixty seconds. A tool that blocked for the
whole discussion would time out, lose the answer, and tempt the client to ask again,
spending the accelerator twice.

So the liaison never blocks a call for longer than 45 seconds. `ask` returns the
ticket inside that window whatever the crew is doing, and `get_answer` is a long
poll: it waits up to 45 seconds and returns as soon as the crew settles. The client
calls it until the status is `answered`. We call this the YDY pattern, "Ya Done
Yet?", and it works with every client because it needs nothing from them but
patience.

Two details make it forgiving:

- **A retry rejoins, with its ticket.** If a client asks the same crew the same
  question again, passing the `ticket` it got from the first `ask`, while that
  discussion is still running, it gets the same ticket back. A timed-out client that
  asks again does not start a second moot. The ticket id is a 128-bit random value
  and the only proof a client holds a ticket: the same question without it always
  starts its own discussion, so one client cannot join another's by asking the same
  thing, and `get_answer` answers only for a ticket id it is given. A ticket that has
  already settled is read with `get_answer`; passing it to `ask` starts a new discussion.
- **The hint tells the model what to do.** Every pending result says, in plain words,
  to call `get_answer` with the ticket and not to ask again. An LLM client reads it
  and behaves.

Clients that support long-running calls get the same behaviour for free. Claude Code
moves any tool call past two minutes into a background task, so a chain of 45-second
polls simply runs in the background until the answer is in.

## Knowing who is asking

By default the liaison accepts every caller that presents the shared token (or any caller, when no token is set) and every ticket is readable by whoever holds its id, as before. With `liaison.auth.kubernetesTokens: true` in the chart, the liaison also checks Kubernetes bearer tokens with a TokenReview, so a caller is a principal and not just a holder of the shared secret:

- The caller's token is passed to the crew's discussion gateway on the question and on the stream, so the gateway, not the liaison, decides what that caller may see. The liaison's shared token is never passed on, and a caller who presents it is anonymous.
- A ticket belongs to the caller who asked. `get_answer` and a retry through `ask` answer only for the same principal; another caller gets "unknown or expired ticket".
- A caller with no token is refused, unless `liaison.auth.allowAnonymous` is `true`. A token that is present and not valid is always refused, and when a shared token is also configured a request that presents nothing is refused even with `allowAnonymous`.
- The caller's token is handed to the background follower only to open the discussion's stream. Once the stream is open the follower holds no token, so the liaison keeps none while the discussion runs. It is never stored, logged or put in a trace.

### A person's own token

When the liaison has service token verifiers (`ACCESS_SERVICE_TOKENS`, with the install's `ACCESS_HASH_KEY`, the same settings a crew's gateway reads), a person's own token for a service the crew trusts is accepted at `/mcp` when that service accepts it:

- The ticket belongs to the person the service named, by the same hashed id the gateway uses. Nobody else can read it.
- A Kubernetes token is checked first and is never sent to a service.
- The token goes to the crew's gateway on the question and on the stream. The gateway keeps it in memory for the turn when the crew's verifier passes tokens to tools.
- A chat app refreshes the person's token on its own, so each `get_answer` carries the current one. When it differs from the last one the liaison sent for that ticket, the liaison sends it to the gateway's credential route for that conversation, and the gateway replaces the held token after checking the caller owns the conversation. A token the gateway accepted is not sent again, renewals for one ticket go one at a time, and a ticket that already has its answer is not renewed. If a renewal fails the next poll tries again; the answer does not depend on it.
- For each ticket the liaison remembers only a keyed fingerprint of the last token sent, never the token.
- A request with no token, or a token the service refuses, gets 401 with a challenge that names the sign-in front's metadata when the front is on. A service that cannot be asked gets 503, never an allow.

### A person's own token from a service

The chart can also let the liaison accept a person's own token from a service, such as a chat app signed in to a library. It is off by default: `liaison.auth.serviceTokens` is empty.

| Chart value | Default | Meaning |
|-------------|---------|---------|
| `liaison.auth.serviceTokens` | `[]` | The services whose acceptance of a bearer token proves who the person is. The same list and keys as a Crew's `spec.discussion.access.serviceTokens` (`name`, `verifyURL`, `subjectClaim`, `audiences`, `maxCacheSeconds`, `passToTools`, `authorize`, `subjects`). It is rendered into `ACCESS_SERVICE_TOKENS` |
| `liaison.auth.hashKeySecret.name` | empty | A Secret in the release namespace that you create. Required once `serviceTokens` is set; the chart refuses to render without it |
| `liaison.auth.hashKeySecret.key` | `hash-key` | The key that holds the install's hash key. It must be the same value as the Secret each crew names in `hashKeySecretRef` |

The liaison is one process for every crew, so its list belongs to the whole liaison. A crew whose gateway does not list the same service refuses the token with 401, and the question fails with that message. Turning the list on also turns on enforcement (`ACCESS_ENFORCE`); Kubernetes token review stays as `liaison.auth.kubernetesTokens` sets it.

The liaison and the gateway are two doors to one crew and share one definition of who is asking (see [Who is asking](../../concepts/who-is-asking/)). Turn on `spec.discussion.access` on the crew as well: a crew that verifies callers refuses the liaison's questions when the liaison sends no token.

## Watching the moot from the dashboard

The client sees only the answer, but the operator's dashboard shows the whole moot
as it happens: which agents the coordinator convened, each finding as it lands, the
signals, and the synthesis. Open the crew's discussions page while a client is
waiting on a ticket and the two views line up: a ticket on one side, a committee at
work on the other.

![The dashboard's discussions page during a liaison-asked moot: the question at the top, two advisors' findings with their signals, and the coordinator's synthesis](../dashboard-moot.png)

A clean run, as timed against a Homelab Pilot crew of small models on two GPU nodes:

| Time | Client call | Result |
|---|---|---|
| 0s | `ask` | returns at 44s with a `pending` ticket |
| 44s | `ask` again, same question and the ticket | the same ticket: a retry rejoins |
| 45s, 101s, 145s | `get_answer` | `pending`, contributions 2 |
| 178s | `get_answer` | `answered`: the two GPU nodes and their models |

Four calls, none longer than 45 seconds, one discussion.

## Where it runs

The liaison ships in the operator chart and is on by default, cluster-internal, at
the Service `<release>-liaison` on port 80, path `/mcp`. It lists `Crew` resources
across the cluster with a read-only role and reaches each crew's discussion gateway
at its in-cluster Service. Tickets live in memory; a restart forgets them and the
client asks again.

```yaml
liaison:
  enabled: true
  maxInflight: 4          # discussions at once; each spends accelerator time
  auth:
    token: ""             # bearer token for /mcp; or existingSecret + key
  expose:
    enabled: false        # HTTPRoute at /mcp on the chart's gateway listener
    hostname: ""          # defaults to gateway.hostname
```

Set a token whenever the endpoint is reachable from outside the cluster, unless an
edge in front of it authenticates (a Cloudflare Access application with a service
token, for example). `maxInflight` caps concurrent discussions; a client that hits
the cap gets a tool error telling it to try again in a minute.

## The door's name and address

One liaison fronts every crew of an install, so the name a chat app shows for it names the install, never one crew. The MCP server title is `liaison.displayName` in the operator chart (`Kubemoot` by default); set it to the name of your community or organization.

```yaml
liaison:
  displayName: Example Community
  expose:
    enabled: true
    hostname: kubemoot.example.org   # defaults to gateway.hostname
  signIn:
    publicURL: https://kubemoot.example.org   # must be https:// plus the hostname above
```

The route on that hostname carries `/mcp`, and with sign-in on, `/oauth` and `/.well-known`. It carries no other path and no other backend; the health probes stay cluster-internal. With sign-in on, the chart refuses to render when `liaison.signIn.publicURL` is not `https://` plus the hostname the route serves.

Which outside services a crew uses on a person's behalf is declared on the crew, not in the liaison; see [Connected accounts](../../reference/connected-accounts/).

## Connecting a client

- [Claude Code](../claude-code/): one command.
- [Claude Desktop](../claude-desktop/): a custom connector with sign-in.
- [Open WebUI](../open-webui/) and [goose](../goose/).
- [Signing in](../signing-in/): what the liaison accepts today.
- Anything else that speaks MCP over Streamable HTTP: point it at `/mcp`. The
  endpoint is stateless and answers with plain JSON, so a curl call works too:

```bash
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"ask","arguments":{"crew":"hello","question":"What is the capital of France?"}}}'
```
