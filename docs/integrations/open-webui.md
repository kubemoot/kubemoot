---
title: Open WebUI
weight: 50
description: Add the crew liaison to Open WebUI as an MCP (Streamable HTTP) tool server.
---

Open WebUI connects to MCP servers over Streamable HTTP. The crew liaison is one, so
an administrator adds it once and people enable it as a tool in their chats. For
the tools and the ticket flow see [The Crew Liaison](../crew-liaison/); for what the
liaison expects in a request see [Signing in](../signing-in/).

## Requirements

- An Open WebUI release with MCP support (its documentation lists the minimum
  version on its MCP page).
- An administrator account: only administrators add MCP servers.
- Tool servers enabled (`ENABLE_TOOL_SERVERS` is not `False`).

## Configuration

1. In the administrator settings, open **Settings**, **Integrations**, and under
   **External Tool Servers** choose **+ Add Connection**.
2. Set **Type** to **MCP (Streamable HTTP)**. Do not choose OpenAPI.
3. Set **URL** to the liaison's `/mcp` address. Open WebUI's backend makes the
   connection, so the address must be reachable from where it runs. Inside the same
   cluster use `http://<release>-liaison.<namespace>.svc/mcp`.
4. Set **Auth**:
   - **None** when the liaison has no token and no Kubernetes token check.
   - **Bearer**, with the token in **Key**, when it has a token. An empty **Key**
     sends an empty header, so use **None** when there is no token.
   Leave the OAuth and Session modes alone; the liaison has no OAuth flow.
5. Name the connection, then **Save**.

## Use it

In a chat, open **+**, then **Tools**, and enable the Kubemoot connection. Use a
model with reliable native tool calling, and ask:

> Ask the homelab-pilot crew which nodes have an accelerator.

The model calls `list_crews`, `ask` and `get_answer` and replies with the crew's
answer. Open WebUI's initialization timeout for an MCP server (`MCP_INITIALIZE_TIMEOUT`,
10 seconds by default) covers connecting, not a crew's answer time: each liaison call
returns within 45 seconds.

## Limits

The [limits that apply to every app](/docs/integrations/#limits-that-apply-to-every-app) apply: a
follow-up in the same Open WebUI chat starts a new Kubemoot conversation, and the
asker's time zone travels only if the model passes `timeZone`. Open WebUI does not
pass chat file attachments to MCP tools, so a crew receives text questions only.
