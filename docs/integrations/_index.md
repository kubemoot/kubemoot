---
title: Integrations
linkTitle: Integrations
weight: 45
description: Connect a chat app to a crew, and use a crew from outside the cluster, as one agent, through the crew liaison.
---
A Kubemoot crew can be a tool in someone else's agent. The crew liaison presents
every crew as a single agent over the Model Context Protocol (MCP); these pages cover how it
works and how to connect the chat apps people use.

{{< docs-index >}}

## Connect a chat app

Any chat app that is an MCP client can reach a crew. The app needs three things:

1. **The liaison's address.** The liaison is one MCP endpoint at `/mcp`, over
   Streamable HTTP, for every crew in the cluster. In the cluster it is the Service
   `<release>-liaison` on port 80, so the URL is
   `http://<release>-liaison.<namespace>.svc/mcp`. From outside the cluster it is the
   route you expose, such as `https://moot.example.org/mcp`, or a port-forward on
   `http://localhost:18080/mcp`. See [The Crew Liaison](crew-liaison/#where-it-runs).
2. **What the liaison expects in a request.** An `Authorization: Bearer <token>`
   header when the liaison has a token or verifies Kubernetes tokens, and no header
   when it has neither. See [Signing in](signing-in/).
3. **A model that calls tools well.** The app's model decides when to call `ask`
   and `get_answer`. A model with reliable tool calling follows the ticket hints
   without help.

| App | How it connects | Page |
|---|---|---|
| Claude Code | `claude mcp add --transport http` | [Claude Code](claude-code/) |
| Claude Desktop | a local bridge, `mcp-remote` | [Claude Desktop](claude-desktop/) |
| Open WebUI | an MCP (Streamable HTTP) connection in Settings | [Open WebUI](open-webui/) |
| goose | a remote extension | [goose](goose/) |
| LibreChat | install, `ENDPOINTS: custom,agents`, `mcpServers` in `librechat.yaml` (per-person sign-in or bearer token), then an agent | [LibreChat](librechat/) |

## What a person sees

The person types a question in the app's chat. The app's model calls `list_crews` if
it needs a crew name, then `ask` with the crew and the question. A quick crew answers
in that call. Otherwise `ask` returns a `pending` ticket, and the model calls
`get_answer` until the status is `answered`; each call returns within 45 seconds. The
person reads the crew's synthesized answer in the app's own words, with the caveats
the crew raised. They see tool calls in the app's tool display; they do not see the
agents, signals or findings. The operator's dashboard shows those.

A crew takes one to several minutes. The app stays responsive in between, because no
call blocks past 45 seconds. See [Why a ticket](crew-liaison/#why-a-ticket).

## Limits that apply to every app

- **A follow-up is a new Kubemoot conversation.** `ask` takes no conversation id, so a
  question a person asks next in the same chat starts its own conversation with the
  crew, even though every answer returns a `conversationId`. The crew has no memory of
  the earlier question. The `ask` tool says so to the model, and a model that follows
  it rewrites the follow-up as a self-contained question ("Which namespaces were
  created most recently?" instead of "Which of those?"). The chat app keeps its own
  conversation; Kubemoot holds one conversation per question.
- **The asker's time zone does not travel on its own.** `ask` accepts an optional
  `timeZone` (an IANA name such as `America/New_York`), and the liaison reads none
  from the connection. Unless the app's model passes it, the crew states dates in its
  default zone. Put the zone in the app's system prompt or instructions ("When you
  call `ask`, pass the user's IANA time zone as `timeZone`") if answers carry dates.
- **No progress stream.** The liaison sends no MCP progress notifications. The
  `contributions` count in a pending ticket is the only progress signal, and it is up
  to the app's model whether to show it.
- **No list of a person's conversations.** The liaison has no tool that lists the
  conversations a person started. `export_conversation` takes the ids the person
  already has.
- **No sign-in of a person to a crew's own services.** See [Signing in](signing-in/).
