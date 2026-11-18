# workspace-access

The Kubemoot collaborative-artifact data-access component. It lets agents
exchange large artifacts by **reference, not value**: a producer writes bulk
tool output to the NATS Object Store and publishes a small reference on the
discussion; consumers read only what they need so bulk data never crosses the
discussion bus or enters an LLM context. See
[`docs/concepts/discussion-artifact-store.md`](../docs/concepts/discussion-artifact-store.md)
for the full design.

## Modes

- **Materializer sidecar (this slice).** Runs beside the no-network code sandbox.
  Subscribes to a crew's artifact subject and streams each referenced object to a
  local directory (a shared `emptyDir`) the sandbox reads. The sandbox stays
  no-network/no-creds; only this sidecar touches NATS.
- **Read-ops MCP service (later).** A shared service exposing bounded read-ops
  (search/filter, head/tail, select-columns, parse csv/json/xml, count) near the
  data so non-code agents can sip a slice without pulling a blob into context.

## Layout

- `internal/artifact` - the reference contract (the producer in agent-runtime
  emits this exact JSON) and the safe object-key -> local-path mapping.
- `internal/materializer` - streams an object to a file (atomic temp+rename);
  `ObjectStore` is an interface so the core is unit-tested without NATS.
- `internal/natsstore` - the NATS JetStream Object Store adapter.
- `internal/threadkey` - short keys. A call's `_meta.workspacePrefix`
  (`{namespace}/{crew}/{thread}/`) names the thread it runs in; a key written
  `{agent}/{nnn}-{tool}` is read inside that thread, a full key of the same thread
  passes, and a key of another thread is refused. Without the entry, keys are used as given.
- `cmd/workspace-access` - wires NATS + subscription + materialization.

## Config (env)

| var | default | meaning |
|-----|---------|---------|
| `NATS_URL` | `nats://localhost:4222` | NATS server |
| `WORKSPACE_MODE` | `materializer` | `materializer` or `mcp` |
| `WORKSPACE_BUCKET` | `kubemoot_workspace` | the workspace bucket, read first |
| `ARTIFACT_BUCKET` | `kubemoot_discussion_artifacts` | the bucket the workspace was stored in before it was renamed, read second; writers still use it |
| `WORKSPACE_DIR` | `/artifacts` | where objects are materialized |
| `WORKSPACE_SUBJECT` | `kubemoot.artifacts.>` | subject carrying reference envelopes |

Every `WORKSPACE_*` variable is also read under its old `ARTIFACT_*` name (`ARTIFACT_MODE`, `ARTIFACT_DIR`,
`ARTIFACT_SUBJECT`, `ARTIFACT_QUERY_ENGINE` and the other `ARTIFACT_QUERY_*`/`WORKSPACE_DUCKDB_BIN` variables)
when the new one is empty, with a log line naming the old variable. The old names are removed in the next
release. Reads try the workspace bucket first and the old bucket second, so a reader keeps working while writers
move; a bucket that does not exist yet is skipped.

## Tools

`workspace_head`, `workspace_tail` and `workspace_grep` read a stored result. `artifact_head`, `artifact_tail` and
`artifact_grep` are served beside them as the same tools under their old names, and `artifact_count`,
`artifact_rows`, `artifact_select` and `artifact_jq` are replaced by `workspace_query`. The old names keep working
for one release: their descriptions say to use the new tool, and the first call of each logs a warning.

## Query tool (off by default)

With `WORKSPACE_QUERY_ENGINE=sqlite` (recommended, in-process, pure Go) or `duckdb` the MCP service also offers `workspace_query`: one
read-only SQL `SELECT` over named artifacts, with a time limit, a row limit, no network
and no files outside the named artifacts. `internal/workspace` holds it; the engine sits
behind its `Engine` interface. For `duckdb` the program must be on `PATH` (or at
`WORKSPACE_DUCKDB_BIN`); `sqlite` needs nothing installed. The variables and the table rules are in
[`docs/concepts/discussion-artifact-store.md`](../docs/concepts/discussion-artifact-store.md#query-with-sql).

## Test

    go test ./...
