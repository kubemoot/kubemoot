# scheduling-mcp

Stdio MCP server exposing four scheduling tools to Kubemoot crews:

| Tool | Effect |
|---|---|
| `set_reminder(when, message, source_thread_id?)` | Notify the user at `when` with `message`. Inline in the source thread if provided. |
| `schedule_followup(when, query, source_thread_id?)` | Re-ask `query` at `when`. Reopens the source thread if provided. |
| `list_scheduled()` | List the crew's pending schedules. |
| `cancel_scheduled(scheduleId)` | Cancel by id (after `list_scheduled` to look up the id). |

Records are written to the NATS KV bucket `kubemoot_scheduled` (see [`pkg/record/record.go`](pkg/record/record.go) for the schema). The Kubemoot operator's scheduler poller reads the bucket and fires due records by publishing to the discussion stream. See [`docs/architecture/scheduling.md`](../docs/architecture/scheduling.md) for the full architecture.

This component is the **MCP write path**. There's also a complementary CronJob write path for recurring schedules; both produce the same record shape, the operator does not care which path wrote any given record.

## Config

| Env var | Default | Notes |
|---|---|---|
| `NATS_URL` | `nats://nats.nats.svc.cluster.local:4222` | NATS server. |
| `KUBEMOOT_CREW` | *(required)* | Crew this MCP serves. Used to scope reads/deletes; records are written with this value. |

## Deployment

The MCPServer CR for this binary lives in `homelab-pilot/charts/homelab-pilot-crew/templates/mcpserver-scheduling.yaml`. The Kubemoot operator deploys it as a stdio MCP with the mcp-bridge sidecar; the `scheduler-advisor` Agent CR includes the four tools in its `enabledTools`.

## Local build / test

```sh
go mod tidy
go test ./...
go build ./cmd/scheduling-mcp/
```

The Dockerfile produces a static `scratch`-based image. CI tags via the existing `kubemoot/.github/workflows` semantic-version pattern.
