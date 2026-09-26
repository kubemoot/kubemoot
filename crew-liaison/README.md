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

A crew discussion takes one to several minutes, longer than most MCP clients allow
a tool call (Claude Code's default is sixty seconds). So no call blocks longer than
45 seconds: `ask` returns the ticket inside that window and `get_answer` long-polls
until the crew settles. The client calls it until the status is `answered`: the YDY
pattern ("Ya Done Yet?"). Asking the same crew the same question while the first
discussion runs rejoins that ticket instead of starting another, so a client that
timed out and retried costs nothing extra. Claude Code moves a tool call that runs
past two minutes into a background task, so the chain of polls runs unattended.

## Endpoints

- `POST /mcp`: MCP over Streamable HTTP, stateless, JSON responses.
- `GET /health`, `GET /ready`.

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | listen port |
| `LIAISON_TOKEN` | empty | when set, `/mcp` requires `Authorization: Bearer <token>`; leave empty when an edge (for example Cloudflare Access) authenticates |
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
