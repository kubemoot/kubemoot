---
title: Claude Desktop
weight: 30
description: Reach the crew liaison from Claude Desktop with a custom connector and sign-in, or through a local bridge.
---

Claude Desktop connects to remote MCP servers through its connector settings, with
OAuth or no authentication, and cannot add custom headers. When the liaison runs a
sign-in front, Desktop connects to it directly and each person signs in. When the
liaison sits behind a bearer token or an access gateway, Desktop reaches it through
`mcp-remote`, a small bridge that runs locally, speaks stdio to Desktop, and
forwards to the liaison with whatever headers it needs. See
[Signing in](../signing-in/) for what the liaison expects, and
[the shared limits](/docs/integrations/#limits-that-apply-to-every-app) for what every chat app shares.

## Connect with sign-in

Use this when the liaison runs the sign-in front (see
[Signing in](../signing-in/#the-sign-in-front)). Each person signs in to the service
behind the crew once, and the crew's tools use that person's own access.

1. In Claude Desktop, open the sidebar and choose **Plugins** (some versions call it
   **Customize**), then **Connectors**, then **Add custom connector**.
2. Enter the liaison's address as the URL: `https://<liaison host>/mcp`.
3. Set **Authentication** to **Sign in now**, and **OAuth client** to **Register
   automatically**. Desktop registers itself with the liaison's front, so no client id
   or secret is entered.
4. Choose **Connect**, then sign in to the upstream service in the browser window that
   opens. A browser already signed in there approves without a login page.

The liaison must accept Claude's sign-in return address. List Claude's hosts in
`liaison.signIn.allowedRedirectHosts` in the Kubemoot chart values:

```yaml
liaison:
  signIn:
    allowedRedirectHosts:
      - claude.ai
      - claude.com
```

A return address on loopback (`http://localhost` or `http://127.0.0.1`) is always allowed
and needs no entry. After connecting, the `kubemoot` tools appear in the tools menu; ask a
crew a question, for example "Ask the homelab-pilot crew which nodes have an accelerator."

## Configuration (local bridge)

Add the liaison to `claude_desktop_config.json` (Settings, Developer, Edit Config):

```json
{
  "mcpServers": {
    "kubemoot": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "https://moot.example.org/mcp",
        "--header", "Authorization:${AUTH}"
      ],
      "env": { "AUTH": "Bearer <token>" }
    }
  }
}
```

Behind Cloudflare Access, send the service token pair instead:

```json
"args": [
  "-y", "mcp-remote", "https://moot.example.org/mcp",
  "--header", "CF-Access-Client-Id:${CF_ID}",
  "--header", "CF-Access-Client-Secret:${CF_SECRET}"
],
"env": { "CF_ID": "<id>", "CF_SECRET": "<secret>" }
```

The header values sit in `env` and the `args` reference them without a space after
the colon; some Desktop builds split arguments on spaces.

## Against a cluster you can reach with kubectl

Port-forward the Service and point the bridge at localhost, with no headers:

```bash
kubectl -n kubemoot port-forward svc/kubemoot-operator-liaison 18080:80
```

```json
"args": ["-y", "mcp-remote", "http://localhost:18080/mcp"]
```

Restart Desktop, confirm the `kubemoot` tools appear under the tools menu, and ask:

> Ask the homelab-pilot crew which nodes have a GPU.

Desktop's tool calls have their own timeout; the liaison returns within 45 seconds
on every call and the model keeps calling `get_answer` until the crew has settled.
