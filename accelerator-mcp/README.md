# accelerator-mcp

Stdio MCP server the scheduling crew uses to see and measure the cluster's model
providers (any accelerator, or a CPU server). The tools enforce the rules that keep
measuring out of a crew's way; what to measure, in what order, and when the results are
good enough is the calling agent's decision, set by its skill.

| Tool | Input | Effect |
|---|---|---|
| `list_providers` | none | Every ModelProvider: readiness, engine and version, memory (total and the budget a model must fit), accelerator or CPU, parallel slots, calibration status, and any reset a person requested. |
| `provider_state` | `provider` | One provider now: models loaded and how much of each is on the accelerator, how many other calls hold it and the models they use, downloaded models with sizes, models that Model resources bind to it. |
| `probe_model` | `provider`, `model`, `probe` (`load`, `short`, `long`, `parallel`) | Sends one probe and returns the engine's timings: load time and spill (`load`), generation speed (`short`), prompt speed (`long`), total speed with the provider's slots in use (`parallel`). |
| `record_calibration` | `provider`, `phase`, optional `message` and `models` | Writes `status.calibration` and the `Calibrated` condition on the ModelProvider. `Calibrating` marks the start; when a reset is pending it clears earlier results, records the reset as acted on, and for scope `all` forgets the provider's performance history (once, so resuming after `Waiting` keeps the new results); `Calibrated`, `Waiting`, and `Failed` record per-model results and unload what the probes loaded. |

## The rules the tools enforce

- A probe sends nothing and returns `busy: true` while other calls hold tickets on the provider. It claims its ticket first and checks again, so a call that arrives in between also stops it.
- A probe claims tickets for its own requests (bus schema `ProviderTicket`), so agents count it against the provider's slots and memory. A ticket that cannot be read is an error, never an idle provider.
- A probe refuses a provider whose `spec.calibration.enabled` is false.
- A probe refuses a model the provider has not reported among its downloaded models, and one whose size on disk is larger than the provider's memory budget (the agent runtime's fit-gate reserve: the discovered or declared total less 12%, at least 2 GiB). A model that fits on disk but not beside the models already loaded shows up as an eviction or a spill.
- On an accelerator, a model only partly in accelerator memory is reported as not fitting (it spilled to the CPU).
- Models a probe loaded are unloaded when the calibration is recorded, or after ten minutes without a probe of that model, unless a crew call is using one; one that is in use or fails to unload is kept for a later try. Probes and unloads run one at a time.
- Every probe request is recorded in `kubemoot_scheduler_performance` (bus schema `SchedulerPerformance`) as kind `calibration`; a failed request has no engine timings (-1).
- Tickets and history are written as `kubemoot/accelerator-mcp/<pod name>`, so two replicas see each other's probes as other calls and never share a history key.

## Config

| Env var | Default | Notes |
|---|---|---|
| `NATS_URL` | `nats://nats.nats.svc.cluster.local:4222` | NATS server holding the ticket and performance buckets. |

The Kubernetes client uses the pod's service account (or `KUBECONFIG`). It needs `get` and
`list` on `modelproviders` and `models`, and `update` on `modelproviders/status`, in
`kubemoot.ai`. Only Ollama providers are supported, as for ModelProvider itself.

## Local build / test

```sh
go test ./...
go build ./cmd/accelerator-mcp/
```

CI builds the static binary and Paketo buildpacks package it, as for the other Go
components. The binary is `/workspace/accelerator-mcp`, started by the buildpacks launcher
`/cnb/process/web`.

## Deployment

The operator chart runs one copy as a core MCP server (`acceleratorMcp` values, on by
default, needs NATS): an MCPServer in the operator's namespace labeled
`kubemoot.ai/core-mcp: "true"` and `kubemoot.ai/tools: accelerators`, started by
`/cnb/process/web` (mcp-bridge wraps it for stdio), with its ServiceAccount and a
ClusterRole with the access above. A crew's gateway registers it by selecting those
labels in `coreServerSelector`; the scheduling crew's gateway does. The chart pins the
image, so Publish Release ships it with the chart.
