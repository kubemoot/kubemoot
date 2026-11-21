---
title: LibreChat
weight: 40
description: Add the crew liaison to LibreChat as an MCP server and use it from an agent.
---

LibreChat is an open-source chat app that speaks MCP. It reaches the crew liaison
through `mcpServers` in `librechat.yaml`, using the `streamable-http` transport. For
the tools and the ticket flow see [The Crew Liaison](../crew-liaison/); for what the
liaison expects in a request see [Signing in](../signing-in/).

## Configuration

Add the liaison to `librechat.yaml`:

```yaml
mcpServers:
  kubemoot:
    type: streamable-http
    url: https://moot.example.org/mcp
    title: Kubemoot crews
    description: Ask a Kubemoot crew a question
    headers:
      Authorization: "Bearer ${LIAISON_TOKEN}"
    requiresOAuth: false
    timeout: 60000
    chatMenu: true
```

- `url` is the liaison's `/mcp` address. LibreChat's own server makes the connection,
  so the address must be reachable from where LibreChat runs. For a LibreChat in the
  same cluster, use `http://<release>-liaison.<namespace>.svc/mcp`.
- `${LIAISON_TOKEN}` is replaced from LibreChat's environment (its `.env` file or
  container environment). Leave out `headers` when the liaison has no token.
- `requiresOAuth: false` stops LibreChat from treating the 401 of a wrong token as
  a sign-in to start. The liaison has no OAuth flow.
- The default `timeout` of 60000 milliseconds is above the liaison's 45-second cap on
  each call, so it needs no change.

Restart LibreChat after editing the file.

## Use it

LibreChat offers MCP servers in the **MCP Servers** section of the chat composer, and
an agent can select the whole server or single tools from it in the Agent Builder.
Select `kubemoot`, choose a model with reliable tool calling, and ask:

> Ask the homelab-pilot crew which nodes have an accelerator.

The model calls `list_crews` if it needs the name, then `ask`, then `get_answer` until the
crew has settled, and replies with the answer.

## Limits

The [limits that apply to every app](/docs/integrations/#limits-that-apply-to-every-app) apply: a
follow-up in the same LibreChat chat starts a new Kubemoot conversation, and the asker's
time zone travels only if the model passes `timeZone`. To have the model pass it, put
that instruction in the agent's instructions.
