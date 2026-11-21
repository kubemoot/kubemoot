# Crew liaison

Kubemoot crews as single agents for any MCP client. Claude Code, or anything else that
speaks the Model Context Protocol, sees one tool set: list the crews, ask one a
question, collect the answer. The crew deliberates inside the cluster; agent names,
signals, tool output, and RAG never cross the boundary. The question goes in and the
synthesis comes out; while the crew deliberates, a pending ticket carries an anonymous
count of contributions so far, so a polling client can tell progress from a stall.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `list_crews` | none | every crew with a discussion gateway: name, namespace, description, ready |
| `ask` | `crew`, `question`, optional `namespace`, optional `waitSeconds` (default and max 45; 0 returns at once) | a ticket, plus the answer if it arrived within the wait |
| `get_answer` | `ticket`, optional `waitSeconds` (default and max 45) | `pending` (with a count of contributions so far), `answered` with the answer, or `failed` |
| `export_conversation` | `crew`, `conversationIds` (1 to 50), optional `namespace`, `timeZone` | the conversation's zip as an embedded resource when it is at most 48 KiB, with its manifest; several ids return a zip of zips |

A crew discussion takes one to several minutes, longer than most MCP clients allow
a tool call (Claude Code's default is sixty seconds). So no call blocks longer than
45 seconds: `ask` returns the ticket inside that window and `get_answer` long-polls
until the crew settles. The client calls it until the status is `answered`: the YDY
pattern ("Ya Done Yet?"). Asking the same crew the same question again with the
`ticket` from the first `ask` while that discussion runs rejoins it instead of starting
another, so a client that timed out and retried costs nothing extra. Without the ticket a
question always starts its own discussion. Claude Code moves a tool call that runs
past two minutes into a background task, so the chain of polls runs unattended.

## Endpoints

- `POST /mcp`: MCP over Streamable HTTP, stateless, JSON responses.
- `GET /health`, `GET /ready`.
- With sign-in enabled: `GET /.well-known/oauth-protected-resource/mcp`, `GET /.well-known/oauth-authorization-server`, `POST /oauth/register`, `GET /oauth/authorize`, `GET /oauth/callback`, `POST /oauth/token`.

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | listen port |
| `LIAISON_TOKEN` | empty | when set, `/mcp` requires `Authorization: Bearer <token>`; leave empty when an edge (for example Cloudflare Access) authenticates |
| `LIAISON_OPERATOR_REPORT_URL` | empty | the operator's report service, which builds conversation exports; without it `export_conversation` answers that export is not configured. The chart sets it |
| `LIAISON_SIGNIN_UPSTREAM` and friends | empty | turns on the stateless OAuth sign-in front (`/.well-known/*`, `/oauth/*`); see "The sign-in front" in `docs/integrations/signing-in.md` for `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL`, `_UPSTREAM_CLIENT_ID`, `_UPSTREAM_SCOPES`, `_PUBLIC_URL`, `_SEAL_KEYS`, `_SEAL_KEYS_FILE`, `_ALLOWED_REDIRECT_HOSTS` |
| `LIAISON_MAX_INFLIGHT` | `4` | discussions in flight at once; every question spends GPU time |
| `LIAISON_TICKET_TTL_MINUTES` | `60` | how long a settled ticket stays retrievable |

The liaison lists `Crew` resources across the cluster and reaches each crew's
discussion gateway at its in-cluster Service, `<crew>-discussion` in the crew's
namespace. The operator chart deploys it with that RBAC (see `liaison` in the chart
values); it is on by default and cluster-internal until a route is enabled.

## Claude Code

```bash
claude mcp add --transport http kubemoot https://moot.example.org/mcp \
  --header "Authorization: Bearer <token>"
```

Then: "Ask the homelab-pilot crew which namespaces exist." Claude calls `ask`, then
`get_answer` until the crew has settled.

## Development

```bash
go test -race ./...
go build .
```

Tickets live in memory; a restart forgets them and the client asks again.
