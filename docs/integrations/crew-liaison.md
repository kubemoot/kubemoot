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

Three tools, and nothing about the discussion's substance.

| Tool | What it does |
|---|---|
| `list_crews` | The crews that can be asked: name, namespace, what each is for, whether it is ready. |
| `ask` | Starts a discussion with a crew and returns a ticket. It waits up to 45 seconds first, so a quick crew answers in the same call. |
| `get_answer` | Reports a ticket: `pending`, `answered` with the answer, or `failed` with the reason. It too waits up to 45 seconds, returning the moment the answer exists. |

The question goes in and the synthesis comes out. Agent names, consensus signals,
findings, tool output, and retrieved context stay inside the cluster. A pending
ticket does carry one number, the count of contributions gathered so far, so a
client can tell a live discussion from a stalled one. It names nobody.

## Why a ticket

A crew deliberates for one to several minutes. Most MCP clients time a tool call out
before that; Claude Code's default is sixty seconds. A tool that blocked for the
whole discussion would time out, lose the answer, and tempt the client to ask again,
spending the GPU twice.

So the liaison never blocks a call for longer than 45 seconds. `ask` returns the
ticket inside that window whatever the crew is doing, and `get_answer` is a long
poll: it waits up to 45 seconds and returns as soon as the crew settles. The client
calls it until the status is `answered`. We call this the YDY pattern, "Ya Done
Yet?", and it works with every client because it needs nothing from them but
patience.

Two details make it forgiving:

- **A retry rejoins.** If a client asks the same crew the same question while the
  first discussion is still running, it gets the same ticket back. A timed-out
  client that asks again does not start a second moot.
- **The hint tells the model what to do.** Every pending result says, in plain words,
  to call `get_answer` with the ticket and not to ask again. An LLM client reads it
  and behaves.

Clients that support long-running calls get the same behaviour for free. Claude Code
moves any tool call past two minutes into a background task, so a chain of 45-second
polls simply runs in the background until the answer is in.

## Watching the moot from the dashboard

The client sees only the answer, but the operator's dashboard shows the whole moot
as it happens: which agents the coordinator convened, each finding as it lands, the
signals, and the synthesis. Open the crew's discussions page while a client is
waiting on a ticket and the two views line up: a ticket on one side, a committee at
work on the other.

![The dashboard's discussions page during a liaison-asked moot: the question at the top, two advisors' findings with their signals, and the coordinator's synthesis](../dashboard-moot.png)

A clean run, as timed against the reference homelab crew of small models on two GPUs
(an RTX 5090 node and an RTX 4090 node):

| Time | Client call | Result |
|---|---|---|
| 0s | `ask` | returns at 44s with a `pending` ticket |
| 44s | `ask` again, same question | the same ticket: a retry rejoins |
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
  maxInflight: 4          # discussions at once; each spends GPU time
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

## Connecting a client

- [Claude Code](../claude-code/): one command.
- [Claude Desktop](../claude-desktop/): through a small local bridge.
- Anything else that speaks MCP over Streamable HTTP: point it at `/mcp`. The
  endpoint is stateless and answers with plain JSON, so a curl call works too:

```bash
curl -s -X POST http://localhost:18080/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"ask","arguments":{"crew":"hello","question":"What is the capital of France?"}}}'
```
