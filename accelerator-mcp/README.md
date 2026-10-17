# accelerator-mcp

Stdio MCP server that sees and measures the cluster's model providers (any accelerator,
or a CPU server). The operator calls `calibrate_provider` when a provider needs
calibrating, and the scheduling crew's calibrator uses the tools when asked. The tools
enforce the rules that keep measuring out of a crew's way.

| Tool | Input | Effect |
|---|---|---|
| `list_providers` | none | Every ModelProvider: readiness, engine and version, memory (total and the budget a model must fit), accelerator or CPU, parallel slots, calibration status, and any reset a person requested. |
| `provider_state` | `provider` | One provider now: models loaded and how much of each is on the accelerator, how many other calls hold it and the models they use, downloaded models with sizes, models that Model resources bind to it. |
| `probe_model` | `provider`, `model`, `probe` (`load`, `short`, `long`, `parallel`) | Sends one probe and returns the engine's timings: load time and spill (`load`), generation speed (`short`), prompt speed (`long`), total speed with the provider's slots in use (`parallel`). |
| `calibrate_provider` | `provider`, optional `models` | Runs the calibration recipe in the background and returns at once with the models it will measure (or `running: true` when one is already running on the provider). For each model: load, two short requests, a long prompt, and a parallel batch when the provider runs requests together; each number is copied from one probe. Without `models` it measures the downloaded models Model resources bind to the provider, else the three smallest that fit. It records `Calibrating`, then `Calibrated`, `Waiting` (crew calls took the provider), or `Failed` (nothing could be measured). |
| `observed_performance` | `provider` | Compares crew calls since the calibration with it, per model: the median generation speed of warm calls that ran alone and of calls alongside others, sample counts, failures, the time span, the most calls seen at once, the calls holding the provider now, and the models that drifted by the operator's rule. |
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
- One `calibrate_provider` run per provider at a time. While it runs, `probe_model` and `record_calibration` refuse that provider, so nothing unloads a model between its measurements. A run that the server stops is recorded as `Waiting` (its final record is written even while stopping), and at startup any provider left `Calibrating` is recorded as `Waiting`, since this server runs every calibration.

## Config

| Env var | Default | Notes |
|---|---|---|
| `NATS_URL` | `nats://nats.nats.svc.cluster.local:4222` | NATS server holding the ticket and performance buckets. |

The Kubernetes client uses the pod's service account (or `KUBECONFIG`). It needs `get` and
`list` on `modelproviders` and `models`, and `patch` on `modelproviders/status`, in
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
