# artifact-access

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
- `cmd/artifact-access` - wires NATS + subscription + materialization.

## Config (env)

| var | default | meaning |
|-----|---------|---------|
| `NATS_URL` | `nats://localhost:4222` | NATS server |
| `ARTIFACT_BUCKET` | `kubemoot_discussion_artifacts` | object store bucket |
| `ARTIFACT_DIR` | `/artifacts` | where objects are materialized |
| `ARTIFACT_SUBJECT` | `kubemoot.artifacts.>` | subject carrying reference envelopes |

## Test

    go test ./...
