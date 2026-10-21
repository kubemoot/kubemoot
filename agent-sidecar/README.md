# agent-sidecar (spike)

Draft code from the two-day Moot Mesh sidecar spike. It answers questions with measurements; it is not
for merge and is not wired into any chart, workflow or image build. The findings, the numbers and the
build plan are in the umbrella note `tasks/notes/Agent Sidecar for the Kubemoot Contract.md`, section
"Spike findings (2026-10-20)".

## What is here

| Path | What it shows |
|---|---|
| `internal/a2a` | The turn door: A2A 1.0 over the HTTP+JSON binding, hand-written client (agent card, SendMessage), the Kubemoot turn profile, and the table that turns a harness reply into a signal |
| `internal/bus` | Reads an `advisory_ready` that selects this agent, runs the turn, publishes `evaluating` and the terminal signal on today's subjects with the generated `operator/pkg/bus` types; every published message is validated against `discussion-message.json` in the tests |
| `internal/modeldoor` | The model door: an OpenAI-compatible endpoint; `model` is an alias; each call is placed by the placement service, streamed back unbuffered, measured, and its ticket released |
| `internal/toolsdoor` | The tools door: MCP over streamable HTTP (stateless), per-agent allow-list enforced on list and call, calls the gateway's REST routes |
| `internal/placement` | Client of the placement service API (`POST /placement/v1/place`, `/release`) and the `bound` placer |
| `internal/turn` | The facts the sidecar measures per turn (tokens, models, tools, failures), keyed by the per-turn key a harness sends as its OpenAI API key |
| `cmd/agent-sidecar` | Wires the doors and the bus; `/healthz` and `/readyz` (ready means NATS connected and the harness card answered) |
| `spike/cmd/*` | Throwaway helpers: `hello-harness` (conformance level 1 harness), `cli-shim` (harness-agnostic shim for command-line agents such as goose), `placement-go` (Go prototype of the service API), `fakemodel`, `fakegateway`, `bench`, `daprapp` |
| `spike/a2a-interop` | Separate module: the hand-written client against the official `a2a-go` v2 server |

The Java side of the spike is `agent-runtime/.../modelserver/PlacementResource.java`: the existing placement
code behind the same service API.

## Run the tests

```
cd agent-sidecar
go test -race -count=1 ./internal/...
golangci-lint run --config ../.golangci.yml ./internal/... ./cmd/...
```

The Go toolchain is the operator's (`go 1.27.2`); the module imports `operator/pkg/bus` through a `replace`.

## Not gate-clean on purpose

The helpers under `spike/` are throwaway and carry line-length findings. They are not part of any release path.
