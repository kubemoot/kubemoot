# agent-sidecar

The Moot Mesh sidecar. One runs in every agent pod whose Agent sets `spec.harness.mode: sidecar`. It is the
pod's side of the table: it sits on the bus, sends the agent's signals, places every model call, and hands
out the tools. The harness container (any program that serves an A2A agent card and `SendMessage`) only
reasons. The operator is the control plane that writes the sidecar's `contract.yaml`; the sidecars are the
data plane. The architecture page "The Moot Mesh" in `docs/architecture/` explains the whole picture.

The module imports the operator's bus contract (`operator/pkg/bus`) and the contract.yaml definition
(`operator/pkg/mesh`) through a `replace`, so the wire and the file have one definition.

## Packages

| Path | What it does |
|---|---|
| `cmd/agent-sidecar` | Reads `contract.yaml` (`--config`), starts the app, stops on SIGTERM |
| `internal/app` | Wires the contract, the NATS connection with the agent's own credentials, the doors, readiness (state: NATS connected and the harness card answering) and shutdown |
| `internal/a2a` | The turn door: the official `a2a-go` v2 client over the HTTP+JSON binding, the Kubemoot turn profile, the table that turns a harness reply into a signal |
| `internal/modeldoor` | The model door: OpenAI-compatible, `model` is an alias, each call placed by the placement service, streamed back unbuffered, measured, ticket released |
| `internal/toolsdoor` | The tools door: stateless MCP over streamable HTTP, allow-list enforced on list and on call, requester date and time zone in `_meta` |
| `internal/placement` | The placement service client, the bound-server floor, and the degraded fallback |
| `internal/bus` | Admission, the durable consumer binding with progress acks, the turn, signals, heartbeat, artifact spill, lifecycle |
| `internal/turn` | What the sidecar measures per turn, keyed by the per-turn key the harness sends as its API key |
| `internal/obs` | OpenTelemetry (W3C trace context through every hop) and Prometheus metrics |
| `internal/bustest` | Test kit: a real NATS server with JetStream, the operator's consumer shape, schema validation of every published message |

## Run the tests

```
cd agent-sidecar
go test -race -count=1 ./...
../.github/scripts/golangci-lint.sh
```

The tests use an embedded NATS server (no Docker). Every message the sidecar publishes in a test is validated
against the bus contract's JSON Schema files, including a check that it carries no undeclared field.

## The placement service API the sidecar codes against

The sidecar is the client; the service is built separately. Everything outside `place` and `release` is
additive and a service may answer 404 to it (the sidecar then does without).

| Call | Request | Answers |
|---|---|---|
| `POST /placement/v1/place` | `namespace, crew, agent, threadId, phase, alias, models[] (preferred first), promptTokens, maxOutputTokens, mayUnload, wait` | `200 {server, endpoint, model, reason, ticket, queued, coldLoad, evicted[]}`; `409 {code: kubemoot_no_fit, reason, kind}` with `kind` one of `model-too-large`, `gpu-busy`, `prompt-too-large` (a `reason` equal to one of them is read as the kind); `202 {waitId, reason}` when the call is held for capacity; 5xx is "unavailable" |
| `GET /placement/v1/wait/{waitId}` | none; the caller's disconnect ends the wait | `200` placement, `409` no fit, `202` still waiting (the client asks again) |
| `POST /placement/v1/release` | `ticket, ok, promptTokens, completionTokens, durationMs` | `2xx` |
| `POST /placement/v1/plans` | `namespace, crew, agent, threadId, phase, models[], mayUnload` | `2xx`; sent when the coordinator selects the agent, so a model can load early |

Traceparent is sent on every call. When the service is unreachable or fails, the call runs on the server the
operator bound for the alias (`models.aliases.<alias>.fallback` in the contract), the turn publishes `waiting`
with reason `placement-unavailable`, and `pickReason` says `degraded`. A `409` is the service's decision and is
never overridden.

## Spike findings: what each became

| # | Spike finding | In this module |
|---|---|---|
| 1 | Placement is not a stateless call | The client sends the thread and alias, follows a held call (202 then wait) and cancels it when the caller leaves, sends `plans` at selection, and reads typed no-fit kinds into capacity stand-asides |
| 2 | The 5 ms budget is not met by today's ticket protocol | Not in the sidecar: the single-writer ticket table lives in the placement service. The sidecar adds no KV work per call. The quiet-node latency measurement is still open |
| 3 | JetStream redelivery against the turn deadline | Built: the sidecar binds the operator's durable consumer by name (`bus.consumer`), reports `InProgress` every `bus.progressAckSeconds` (default 30 s, a quarter of the 120 s AckWait) for as long as the turn lives, including turns queued behind `concurrency`, and acknowledges only after the terminal signal is published; a publish failure asks for the message again. A test with an AckWait of 3 s and a 5 s turn shows no redelivery, and its control without progress acks shows one |
| 4 | The tools door must be stateless | Built and tested against a stateless `tools/list` |
| 5 | A harness's own names and defaults (tool prefix, `--no-profile`, tool traces in text) | The operator passes `KUBEMOOT_NATIVE_TOOLS`; applying it and choosing the JSON output are the goose harness image's job (next step). Tool-name prefixes are documented for prompt authors |
| 6 | agent-runtime is not a library | Not here: it concerns the placement service build |
| 7 | Port 8080 | The sidecar owns the pod-facing port; the harness gets `KUBEMOOT_TURN_PORT` (default 9000); sidecar-mode pods carry `kubemoot.ai/harness-mode: sidecar` and a separate PodMonitor scrapes `/metrics` |
| 8 | The Go module depends on the operator module | Kept as an in-tree `replace`; the CI workflow also triggers on `operator/pkg/bus` and `operator/pkg/mesh` |
| 9 | The Dapr injector is not a template | The pod patch is one pure function (`operator/internal/mesh`), ready for next release's webhook |
| 10 | Readers of the ticket bucket | Not here: the placement service decides whether the KV mirror is written before it replies |

## Deferred, with reasons

- **Direct chat** (`POST /api/chat` answers 501): the chat surface needs its own schema inventory first.
- **`/debug/turns`**: needs a decision on where a loopback-only route lives and a `kmctl` reader; traces carry the cross-container debugging for now.
- **Learned and shadow placement**: they stay with the placement service.
- **`/v1/embeddings`**: the embeddings binding is catalog work, not a harness need yet.
- **`kubemoot_scheduler_generate_seconds`**: OpenAI-compatible replies carry no engine timing; the engine contract adds it.
- **Crew memory and RAG tools**: they arrive as tools through the gateway, not as sidecar code.
- **Pause**: `thread_pause` does not cancel a running turn; stop and close do.
