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
| `my_accounts` | none | only with connected accounts on: each outside account the person's crews could use, its state, and the link to connect it |

`ask` answers `status: "needs_connection"` (with `connections` and a link written by the liaison) and starts nothing when the crew requires an account the person has not connected.

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
- With connected accounts on: `GET|POST /oauth/connect/<account>`, `GET /oauth/accounts`, `POST /oauth/accounts/disconnect`, `POST /oauth/signout`, `POST /oauth/revoke`; and on the internal port (`LIAISON_INTERNAL_PORT`, default 8081, never routed) `POST /internal/v1/accounts/lease` and `POST /internal/v1/accounts/connected` for a crew's discussion gateway.

## Configuration (environment)

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | listen port |
| `LIAISON_TOKEN` | empty | when set, `/mcp` requires `Authorization: Bearer <token>`; leave empty when an edge (for example Cloudflare Access) authenticates |
| `LIAISON_DISPLAY_NAME` | `Kubemoot` | the MCP server title a chat app shows for this install's door |
| `LIAISON_OPERATOR_REPORT_URL` | empty | the operator's report service, which builds conversation exports; without it `export_conversation` answers that export is not configured. It serves TLS, so the URL is https and is verified against `LIAISON_CA_FILE`; the chart sets both. A URL that is not https is refused unless `LIAISON_ALLOW_INSECURE_REPORT=true` |
| `LIAISON_CA_FILE` | empty | the install CA's public certificates (the chart mounts them), which verify the report service; read again when they change |
| `LIAISON_ALLOW_INSECURE_REPORT` | `false` | accept a report URL that is not https; the chart never sets it |
| `LIAISON_ALLOW_INSECURE_GATEWAY` | `false` | call the crews' discussion gateways over plain http. Without it the liaison calls them over https and verifies each against `LIAISON_CA_FILE` (required then); the chart sets it only when `discussionGateway.tls.enabled` is false |
| `LIAISON_SIGNIN_UPSTREAM` and friends | empty | turns on the stateless OAuth sign-in front (`/.well-known/*`, `/oauth/*`); see "The sign-in front" in `docs/integrations/signing-in.md` for `LIAISON_SIGNIN_UPSTREAM_TOKEN_URL`, `_UPSTREAM_CLIENT_ID`, `_UPSTREAM_SCOPES`, `_PUBLIC_URL`, `_SEAL_KEYS`, `_SEAL_KEYS_FILE`, `_ALLOWED_REDIRECT_HOSTS` |
| `LIAISON_STATE_BACKEND` | `memory` | where the door keeps its single-use marks and the connected accounts: `memory`, `nats` (the `NATS_*` connection every component reads) or `openbao` (`LIAISON_OPENBAO_ADDR`, `_MOUNT`, `_PREFIX`, `_TOKEN_FILE`, `_CA_FILE`). `nats` and `openbao` share it across replicas |
| `LIAISON_ACCOUNTS_ENABLED` and friends | `false` | connected accounts, off by default; see "Connected accounts" in `docs/integrations/signing-in.md` for `LIAISON_ACCOUNTS_SEAL_KEYS_FILE`, `LIAISON_ACCOUNTS`, `LIAISON_INTERNAL_PORT` and `ACCESS_HASH_KEY` |
| `LIAISON_BROKER_TLS_CERT_FILE`, `LIAISON_BROKER_TLS_KEY_FILE` | none | the account broker's serving certificate and key, which the operator signs from the install CA; the port then speaks TLS only and is read again when the files change. Without them the broker refuses to start unless `LIAISON_ALLOW_INSECURE_BROKER=true` (the chart sets it only when `discussionGateway.tls.enabled` is false) |
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

Tickets live in memory; a restart forgets them and the client asks again. The token store behind connected accounts and the door's single-use marks (`internal/tokenstore`) has a NATS key-value backend, an OpenBao backend and a memory one behind one interface; its contract tests run against all three.

## Testing against a real OpenBao

The token store's OpenBao backend is checked on every test run against an in-process server that speaks KV version 2, and against a real OpenBao (or Vault compatible) server when `OPENBAO_ADDR` is set. The real-server checks cover the compare-and-swap create, a conflict on a stale revision, a version expiring by its time to live, and delete. They skip when the address is not set:

```sh
bao server -dev -dev-root-token-id=root &
OPENBAO_ADDR=http://127.0.0.1:8200 OPENBAO_TOKEN=root go test ./internal/tokenstore -run OpenBaoServer
```

`OPENBAO_MOUNT` names the KV v2 mount (default `secret`, which a dev server provides). Each run writes under a prefix of its own and deletes what it wrote. The test accepts an `http` address because a dev server has no TLS; the liaison itself never does.
