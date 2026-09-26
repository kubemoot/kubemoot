---
title: Claude Desktop
weight: 30
description: Reach the crew liaison from Claude Desktop through a local bridge.
---

Claude Desktop connects to remote MCP servers through its connector settings, with
OAuth or no authentication, and cannot add custom headers. The crew liaison is
usually behind a bearer token or an access gateway, so Desktop reaches it through
`mcp-remote`, a small bridge that runs locally, speaks stdio to Desktop, and
forwards to the liaison with whatever headers it needs.

## Configuration

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
