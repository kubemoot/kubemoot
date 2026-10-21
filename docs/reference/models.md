---
title: "Models in Kubemoot"
description: "The Kubemoot model layer: the ModelServer, Model, and EmbeddingModel CRDs, how the scheduler binds agents to them, load on demand, the catalog, and GPU fit."
weight: 3
---

This document covers the model layer end to end: the CRDs (ModelServer, Model, EmbeddingModel), how Agents reach a `(model, server)` binding via the scheduler, the load-on-demand principle, the model catalog, and GPU compatibility for a reference deployment.

For the framework behind these choices - why tool-calling fidelity outranks coding benchmarks, how to budget VRAM, and how to match model size to discussion role - see [Choosing a Model](../../concepts/choosing-a-model/).

## CRD Hierarchy

```
ModelServer (where - a model server endpoint)
    │
    ├── Model (what - labeled inference model on a server)
    │
    └── EmbeddingModel (what - embedding model for RAGSource indexing)
```

Agents declare **capabilities**; the scheduler matches them to a `Model` whose labels satisfy a `CrewSchedulingPolicy`'s `require` / `prefer` selectors. See [Scheduler](../../architecture/scheduler/) for the full algorithm.

## ModelServer

Declares an inference server. The operator discovers accelerator capacity (VRAM, loaded models, agent count) via DCGM metrics and the engine's own APIs (vLLM and Ollama). vLLM is the currently preferred engine; Ollama is the engine of the quickstart and remains supported.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | Yes | The model server's engine type: `ollama` or `vllm`. Any other value, including `openai` and `anthropic`, is rejected at admission: those types are not supported yet. The operator publishes it as `engine` in the server state, and agents use it to pick the adapter that calls the server through the OpenAI-compatible chat API. See the [measured results](../../concepts/choosing-a-model/#measured-results) for how the two engines compare. |
| `endpoint` | string | For ollama | API endpoint URL. Allowed only on an `ollama` ModelServer: a `vllm` ModelServer names its servers with `serviceRef` or `servers`. |
| `serviceRef.name` | string | One of serviceRef or servers | For a `vllm` ModelServer: the Service that fronts the vLLM servers, for example the one the `kubemoot-vllm` chart installs. The operator reads the Service's ports and asks each port's `/v1/models` which model it serves, so you write no endpoints. Allowed only on a `vllm` ModelServer. See [vLLM servers](#vllm-servers). |
| `serviceRef.namespace` | string | No | The Service's namespace. Default: the ModelServer's namespace. |
| `servers` | list | One of serviceRef or servers | For a `vllm` ModelServer: the vLLM servers, one per served model (1 to 32), for vLLM deployed some other way than behind one Service. Allowed only on a `vllm` ModelServer; set exactly one of `serviceRef` and `servers`. |
| `servers[].model` | string | Yes, within an entry | The served name of the model the vLLM server answers to: the `model` of the `Model` on it, and the `--served-model-name` the vLLM server runs with. Unique within the ModelServer. |
| `servers[].endpoint` | string | Yes, within an entry | The vLLM server's OpenAI-compatible base URL without `/v1`, for example `http://vllm-qwen3-14b.models:8000`. Must start with `http://` or `https://`. |
| `sleepLevel` | int | No | For a `vllm` ModelServer: how a vLLM server is put to sleep when another model takes the accelerator. `1` parks the weights in host memory (fast to wake), `2` drops them and reloads from disk on wake (holds no host memory). Unset means `1`. It cannot change once the ModelServer exists, so it must be the level the vLLM servers were installed with. Allowed only on a `vllm` ModelServer. |
| `secretRef` | string | No | Reserved for future engine types; not used by `ollama` or `vllm` |
| `scheduling.weight` | int | No | Preference between ModelServers for reasons other than capacity (higher is preferred); default 100 |
| `scheduling.memoryMiB` | int | No | Memory budget the fit gate spends when the operator cannot discover one; see below |
| `calibration.enabled` | bool | No | Whether this ModelServer may be calibrated (default `true`); see Calibration below |

### vLLM servers

vLLM serves one model per server process, and its sleep mode lets several processes share one accelerator: the processes that are not in use sleep and the one in use is awake. A `vllm` ModelServer therefore describes a set of vLLM servers, one OpenAI-compatible endpoint per served model, not one process. You install the vLLM servers first, the way you install Ollama, and then point a ModelServer at them. vLLM is the currently preferred engine for a model that several agents call at once; Ollama stays the engine of the quickstart.

The `kubemoot-vllm` chart installs the vLLM servers (see [Run vLLM on a GPU node](../../operating/gpu-worker-nodes/#run-vllm-on-a-gpu-node)); any other vLLM deployment works too. With the chart, the ModelServer names the chart's Service, and the operator finds the vLLM servers from its ports:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: vllm-gpu
spec:
  type: vllm
  sleepLevel: 1
  serviceRef:
    name: vllm-gpu-kubemoot-vllm
    namespace: vllm
```

`helm install` prints this ModelServer for the release, with a Model for each model, so you copy it rather than type it. The operator records what it found in `status.vllmServers`: each served name and the URL of its port. It asks the TCP ports of the Service and keeps every name a vLLM server lists, so a vLLM server started with several served names is known by all of them. A port that has never answered is left out until it does, and the ModelServer is `Pending` until a port reports a model. A port that answered before and stops answering, for example while its vLLM server restarts, keeps the names last found there, so its Models are not reported as misnamed. If the Service itself cannot be read, the last known vLLM servers stay in the status and the ModelServer reports `Failed`.

For vLLM deployed another way, list the vLLM servers yourself with `servers`, one endpoint per model:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: vllm-a
spec:
  type: vllm
  sleepLevel: 1
  servers:
    - model: qwen3:14b
      endpoint: http://vllm-qwen3-14b.models:8000
    - model: qwen3:8b
      endpoint: http://vllm-qwen3-8b.models:8000
```

In both forms you run the vLLM servers; the operator does not start, stop, or restart them. A vLLM server must run with `--enable-sleep-mode`, the environment variable `VLLM_SERVER_DEV_MODE=1` (vLLM serves its sleep endpoints only then), and `--served-model-name` set to the name its Model uses. A vLLM server without sleep mode answers 404 on the sleep endpoints and is treated as always awake: it still serves its model, but the operator cannot make it give up the accelerator, so switching to another model on the same accelerator fails. `sleepLevel` cannot be changed once the ModelServer exists, because a sleeping vLLM server wakes by the level the ModelServer names and that must be the level it slept at; delete and recreate the ModelServer to change it. When a call needs a model that is asleep, the agent runtime asks the operator to wake it, over NATS; the operator puts the awake vLLM server of the ModelServer to sleep first.

What the operator does with the vLLM servers:

- **Readiness.** The ModelServer is `Ready` while at least one vLLM server answers `/health` and lists its served name on `/v1/models`. The status message says how many answer, for example `1 of 2 vLLM servers answering`. A vLLM server that does not answer, or serves a different name than listed, is left out of the published state.
- **State.** Each vLLM server's `/is_sleeping` becomes the model's `modelStates` entry in the server state, `awake` or `asleep`. A vLLM server that answers 404 on the sleep endpoints counts as awake. The awake models are the ModelServer's `loadedModels`.
- **Load.** `/metrics` supplies `requestsRunning`, `requestsWaiting`, and `kvCacheUsage` (`vllm:num_requests_running`, `vllm:num_requests_waiting`, and the cache gauge `vllm:kv_cache_usage_perc`, or `vllm:gpu_cache_usage_perc` on older vLLM versions). The server state describes the whole ModelServer, so the request counts are summed over its vLLM servers and the cache use is the highest of them. A metric a vLLM server does not export stays absent; a reported 0 is published as 0.
- **Placement.** An agent places a call on a `vllm` ModelServer that hosts the model, awake or asleep. The ModelServer needs no accelerator memory total and no per-model footprint: each vLLM server reserved its memory when it started. It has room while it holds no waiting request and its key-value cache is not nearly full; the number of calls in flight does not limit it, because they join the engine's batch. `maxParallel` is published as the engine's ceiling on concurrent sequences rather than one slot. See [How placement treats each engine](../../architecture/scheduler/#how-placement-treats-each-engine).
- **Switching.** An agent asks the operator over NATS before a call to a model that is not awake (see [Waking and sleeping over NATS](../../architecture/scheduler/#waking-and-sleeping-over-nats)), and the operator replies when the ModelServer reports the state. The chart's launcher wakes nothing: every vLLM server starts asleep. To make a model ready, the adapter puts any other awake vLLM server of the ModelServer to sleep, then wakes the model's vLLM server. To release a model, it puts that vLLM server to sleep. It never restarts a vLLM server, because a restart takes 34 to 47 seconds where a wake takes 0.7 to 4.9. At level 2 the wake restores the weight buffers, reloads the weights from disk, then restores the key-value cache, in that order.

An agent that uses a model on a `vllm` ModelServer calls that model's own vLLM server with the served name. `ModelServer.status.capacity` is filled from the same probe (loaded models, no accelerator memory per model); the accelerator total comes from `scheduling.memoryMiB` or DCGM. Storage and the Ollama version check do not apply to a `vllm` ModelServer; it is calibrated like an Ollama one (see Calibration below). An `EmbeddingModel` cannot use one.

### Status

| Field | Source | Description |
|-------|--------|-------------|
| `ready` | Operator probe | Endpoint reachable + API responding (for `vllm`, at least one server answering) |
| `vllmServers` | Operator, from `serviceRef` | For a `vllm` ModelServer with `serviceRef`: each server found, as its served name and URL. Empty with `servers`, where the spec says it. |
| `capacity.vramTotalMiB` | DCGM Prometheus | Total GPU VRAM |
| `capacity.vramUsedMiB` | Ollama `/api/ps` | VRAM consumed by loaded models |
| `capacity.gpuModel` | DCGM metrics | e.g., "NVIDIA GeForce RTX 5090" |
| `capacity.maxParallel` | Pod env `OLLAMA_NUM_PARALLEL` | Concurrent request slots |
| `capacity.contextLength` | Pod env `OLLAMA_CONTEXT_LENGTH` | Per-request context (tokens) each slot gives a model; unknown when the engine chooses its own default |
| `capacity.loadedModels[].contextLength` | Ollama `/api/ps` | Context the loaded model actually runs with; takes precedence over `capacity.contextLength` |
| `capacity.agentCount` | Operator | Agents currently bound to this server |
| `capacity.loadedModels` | Ollama `/api/ps` | Models currently resident |
| `capacity.availableModels` | Ollama `/api/tags` | Models downloaded on this server |
| `capacity.nodeName` | Kubernetes | Node hosting the Ollama pod |
| `capacity.lastProbed` | Operator | When capacity was last discovered |
| `storage.volume` | Ollama pod | The volume mounted at the models directory (`OLLAMA_MODELS`, default `/root/.ollama/models`), for example `pvc/ollama-data` |
| `storage.totalBytes` | PersistentVolumeClaim | Declared capacity of that volume (an `emptyDir` counts only when it has a size limit) |
| `storage.modelBytes` | Ollama `/api/tags` | Sum of the sizes of the downloaded models |
| `storage.partialBytes` | Operator | Disk held by downloads that have not finished: pulls in progress and the partial files a cancelled or failed pull left behind |
| `storage.freeBytes` | Operator | `totalBytes` minus `modelBytes` and `partialBytes`, an estimate of the free disk |

`storage` is reported whether or not the scheduler is enabled. It is absent when the
volume cannot be sized (for example a `hostPath` or an `emptyDir` without a limit).
`freeBytes` is an estimate: layers shared between models are counted once per model.
A pull whose remaining download exceeds `freeBytes`
stops and the Model reports `Error` with the amount it is short, instead of the server's
raw failure; the pull is tried again once `freeBytes` changes.

Ollama lists no partial download in `/api/tags`, and it has no call that removes the
partial files of a model that never finished: `DELETE /api/delete` answers "not found"
for a model without a manifest, and the server deletes unused partial files only when it
starts. The operator therefore does not reclaim them. It counts them instead: it adds
the bytes each pull downloaded to `partialBytes` while the pull runs, and keeps that
number when a pull is cancelled (the last Model or EmbeddingModel using the tag was
deleted or retargeted) or fails. The count is dropped when a later pull of the same tag
resumes from those files, when the model lands, and when the Ollama pod or container
restarts. An operator restart forgets the count, so partial files from before the
restart are not reported until the model server restarts.

A ModelServer stored with any type other than `ollama` (one created before the API server rejected them) reports `ready: false` with reason `Unsupported` and the same message the API server gives; Models and EmbeddingModels on it report the same message.

The scheduler refuses to place a call whose prompt exceeds a server's context for the
model, because the engine would silently drop the oldest messages instead of rejecting it
(see [Scheduler](../architecture/scheduler.md#the-context-window-is-a-hard-constraint)).
On Ollama the total KV cache scales with `OLLAMA_NUM_PARALLEL` times
`OLLAMA_CONTEXT_LENGTH`, so a GPU trades parallel slots against per-request context.

Discovery needs the scheduler enabled and, for VRAM, DCGM metrics in Prometheus. A
server the operator cannot measure (a CPU Ollama, or a GPU host without DCGM)
declares its budget instead:

```yaml
spec:
  scheduling:
    memoryMiB: 6144   # VRAM on a GPU host; host RAM the server may use on a CPU host
```

A discovered VRAM total always wins over the declaration. With neither, agents refuse
cold loads on that server because the fit gate has no budget to check against.

On a server without a discovered VRAM total, a loaded model whose `/api/ps`
`size_vram` is 0 (how a CPU server reports a model held in host RAM) counts at its
whole size against `memoryMiB`.

### Calibration

A `vllm` ModelServer is calibrated through the same path. The probes are streamed `/v1/chat/completions` requests: the prompt speed is the prompt tokens the server reports over the time to the first token, the generation speed is the tokens after the first over the time from the first token to the last, and the context is the `max_model_len` the server lists. The parallel probe sends four requests together, because a vLLM server batches without a fixed slot count. A model that is asleep is woken through the operator's lifecycle request, the same one agents use, never with a direct `/wake_up`; the time that took is the model's load time, and the model is put back to sleep through the operator when the calibration ends. Nothing is loaded ahead of a call that needs it. The models measured are the served names of the Models bound to the server.

`status.calibration` records what calibrating a server measured, for each model: how
long the engine takes to load it, how much accelerator memory it takes, the context the
engine gives it, how fast it processes a long prompt and generates, the total generation
speed with the server's parallel slots in use, and whether it fits or spills to the
CPU, with a `Calibrated` condition.

**When it runs.** The operator starts a calibration on its own when a server needs one:
it was never calibrated, a reset was requested, its engine version changed, or Models
were bound to it that the calibration has not measured. It asks once per need. It
resumes a calibration that paused as `Waiting` (crew calls took the server, or the
calibration server stopped mid-run) once no call holds the server, and retries one
that `Failed` once the server has become ready again after the failure. A need that
arrives while a calibration is already running is asked once that run ends. It
starts calibrations through the `accelerator-mcp` core server once that server is ready
(see [The scheduling crew](../architecture/scheduler.md#the-scheduling-crew)); the
scheduling crew's model-server-calibrator also starts them when asked. The scheduler
does not read `status.calibration` yet. A server never calibrated has no
`status.calibration`.

**What it measures.** The `calibrate_server` tool runs a fixed recipe in the
background, with no model making decisions: for each model, load it, send two short
requests, a long prompt, and, when the server runs requests together, a parallel batch.
Each number is copied from one probe: generation speed from the second (warm) short
request, prompt speed from the long prompt. Without a list of models it measures the
downloaded models that Model resources bind to the server together with the models the
server's catalog lists as present, else the three smallest that fit. A model that does
not fit is recorded with `fits: false` and its reason.

Each model is measured on its own, and every model gets a result in
`status.calibration.models[]`, with a `status`:

- `Measured`: the numbers below. This is the default when `status` is empty.
- `Skipped`: calibration does not apply. An embedding model (the engine lists
  `embedding` and not `completion` for it) cannot be asked to generate, so it is never
  loaded or sent a request, and it is not a failure. The `reason` says why.
- `Failed`: a model that chats could not be measured; the `reason` is the error.

One model's failure never stops the others. The phase follows the results: `Calibrated`
when every model that chats was measured (also when there is nothing to measure because
the server has only embedding models), `Failed` when a model that chats could not be
measured, with the others' results kept. A `Failed` run with results stays `Failed`
until the server, its Models or the reset annotation change, or the server becomes ready
again after the failure. A `Failed` record with no per-model results and the summary "no model
could be measured" stopped before it learned anything about the models (the recipe now
records every model it tries), and the operator asks again once the server is idle.

**Waiting for the bus.** Calibration reads and writes two NATS KV buckets: the server
tickets every probe claims and the performance history. On a fresh install the NATS
bootstrap job that creates them starts together with the operator, so they can be
missing for a few seconds, and NATS itself can be briefly unreachable. That is a state to
wait out, not a result about the models: the operator does not start the calibration
(see the `Calibrated` condition below), and a run that meets it mid-way is
recorded as `Waiting` with `status.calibration.reason: WaitingForBus`, never `Failed`.
The operator checks again every 30 seconds and starts or resumes the calibration on its
own when both buckets exist and the server is idle. The 30 seconds only set how soon the
next look happens; the calibration continues because the bus became ready.

The operator is the only writer of the `Calibrated` condition. It derives it on every
reconcile from `status.calibration` (which `accelerator-mcp` writes) and its own check
of the bus, so the condition and its message always follow the current cause.

| `Calibrated` condition | When |
|-------|-------|
| `True`, reason `Calibrated` | The last run measured every model that chats (embedding models are skipped) |
| `True`, reason `RecalibrationWaitingForBus` | The server was calibrated before and a newer calibration waits for the bus; the earlier results stand and stay in use |
| `Unknown`, reason `WaitingForBus` | The server was never calibrated and NATS is unreachable or a scheduler bucket does not exist yet; nothing is wrong with the server or its models |
| `False`, reason `Calibrating` or `Waiting` | A run is under way, or paused because crew calls hold the server |
| `False`, reason `Failed` | A model that chats could not be measured; it stays `Failed` until the server, its Models or the reset annotation change, or the server becomes ready again after the failure |

| `status.calibration` field | Description |
|-------|-------------|
| `phase` | `Calibrating`, `Calibrated`, `Waiting` (paused because crew calls were using the server), or `Failed` |
| `message` | What the run measured, or why it paused or failed |
| `reason` | What a `Waiting` run waits for when it is more than the server being idle: `WaitingForBus` |
| `engineVersion` | The engine version the results were measured on |
| `resetToken` | The last reset request acted on, set when the calibration it asked for starts |
| `completedAt` | When the last calibration finished |
| `updatedAt` | When the calibration was last recorded, in any phase |
| `models[]` | Per model: `status` (`Measured`, `Skipped` or `Failed`), `fits`, `reason`, `loadMs`, `residentMiB`, `contextLength`, `promptTokensPerSecond`, `generateTokensPerSecond`, `parallelRequests`, `parallelGenerateTokensPerSecond`, `calibratedAt` |

The tools keep calibration out of a crew's way. A probe sends nothing while other calls
hold the server, claims tickets for its own requests like any call, refuses a model the
server has not reported or one larger than its memory budget, and the models it loaded
are unloaded when the calibration is recorded (or after ten idle minutes), unless a crew
call is using one. Every probe request is kept in the scheduler's performance history as
kind `calibration`.

**Asking for a fresh calibration.** Set the annotation `kubemoot.ai/calibration-reset` on
the ModelServer to a new value. The next calibration that starts clears the earlier
results and records the value in `status.calibration.resetToken`, so each value resets
once. With `kubemoot.ai/calibration-reset-scope: all`, it also forgets the server's
recorded performance history, for a first-install test. With GitOps, set the annotations
in the ModelServer manifest in git; by hand:

```bash
kubectl annotate modelserver ollama-gpu -n kubemoot --overwrite \
  kubemoot.ai/calibration-reset="$(date +%s)" kubemoot.ai/calibration-reset-scope=all
```

Set `spec.calibration.enabled: false` to refuse probes on a server whose calls are not
wanted, such as a leased accelerator billed per request.

`status.calibrationRequest` is the operator's last request: `key` names the need it
asked about (`new`, `reset/<token>`, `engine/<version>`, or `models/<digest>`), `reason`
says it in words, and `requestedAt` says when.

`status.calibrationDrift` lists the models whose crew calls, running alone on the
server, generated much slower or faster than calibration measured (see
[Drift](../architecture/scheduler.md#drift)): per model, `calibratedTokensPerSecond`,
`observedTokensPerSecond`, and the `samples` behind the median as detected, with
`detectedAt` and, once the scheduling crew was asked to judge it, `askedAt`. It is
cleared once the speeds agree again or a new calibration replaces the one they drifted
from.

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: ollama-a
spec:
  type: ollama
  endpoint: http://ollama.ollama-a:11434
---
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: ollama-b
spec:
  type: ollama
  endpoint: http://ollama.ollama-b:11434
```

`kubectl get mdls` (short name: `mdls`).

### The model catalog

The cluster's models form one catalog, kept by the cluster owner and discovered from the
model servers. Each ModelServer publishes its part of the catalog in `status.models`: every
chat model the server has (downloaded on Ollama, answering on vLLM), merged with the Model
objects in the ModelServer's namespace that name it. The cluster owner keeps those Models in the
operator's namespace, next to the servers; a crew's namespace holds none.

- A chat model the server reports joins the catalog without a Model object. A model that only
  embeds is never admitted.
- A Model with `spec.disabled: true` keeps its model out.
- Where a Model and the engine both speak, the Model wins: its `parameters`, `contextLength`,
  `capabilities` and labels replace what the engine reported.
- A Model whose model the server does not have is listed with `present: false`. For an
  Ollama server the operator downloads it.

Each entry of `status.models`:

| Field | Description |
|-------|-------------|
| `name` | The model name on the server: the Ollama tag, or the vLLM served name |
| `present` | The server has the model now |
| `source` | Where the parameter count came from: `model`, `ollama-show`, `ollama-tags`, `service`, or `none` |
| `parameterCount` | Total parameters, 0 when unknown |
| `size` | The size class, empty when unknown |
| `contextLength` | Context window in tokens |
| `capabilities` | `tool-calling`, `reasoning`, `vision` |
| `modelRef` | The Model object that contributed, as `namespace/name` |
| `labels` | The effective labels a scheduling rule matches: the derived ones above plus the Model's own |

The labels the catalog produces:

| Label | Values | Comes from |
|-------|--------|------------|
| `kubemoot.ai/size` | `small`, `medium`, `large` | the parameter count; a `kubemoot.ai/size` label on a Model overrides it |
| `kubemoot.ai/context-tokens` | integer string | the engine, or the Model's `contextLength` |
| `kubemoot.ai/family` | for example `qwen3` | Ollama's model details |
| `kubemoot.ai/engine` | `ollama`, `vllm` | the server type |
| `capability/tool-calling`, `capability/reasoning`, `capability/vision` | `"true"` | the engine, or the Model's `capabilities` |
| anything else | any | the Model's `metadata.labels` |

The size class is the same on every cluster: **small** is under 12 billion parameters,
**medium** is 12 billion up to but not including 30 billion, **large** is 30 billion and over.
It counts total parameters, so a mixture-of-experts model with few active parameters is large
and shows its speed in calibration instead. An entry whose parameter count is unknown has no
size: the server's `CatalogSizeKnown` condition is `False` and names the models, it contributes
no size score to `qualityBias`, and a rule that requires a size excludes it. Declare
`spec.parameters` on a Model to fix it. An Ollama server too old to list `capabilities` on
`/api/show` cannot say that an embedding-only model is one; keep such a model out with a
Model that sets `disabled: true`. Embedding-only models are never catalog entries; they are
listed in `status.embeddingModels` for [EmbeddingModel](#embeddingmodel) binding.

Where each fact comes from:

| Fact | Ollama | vLLM |
|------|--------|------|
| The model exists | `/api/tags` | the servers found through `serviceRef` or `servers` |
| Parameter count | `/api/show` (`model_info` `general.parameter_count`, else `details.parameter_size`), asked once per model digest | declared: the `kubemoot-vllm` chart's `parameters`, or a Model's `spec.parameters` |
| Context window | `/api/show` (`model_info`, `<architecture>.context_length`) | `max_model_len` on `/v1/models` |
| Abilities | `/api/show` `capabilities` (`tools`, `thinking`, `vision`) | declared: the chart's `capabilities`, or a Model's `spec.capabilities` |

Speed and fit are not catalog labels: calibration measures them per server (see
[Calibration](#calibration)), because the same model is fast on one accelerator and slow on
another. Calibration measures every present model in the catalog.

### From the operator chart

The operator chart can declare ModelServers from its `modelServers` values. The list is
empty by default, because a chart cannot point at a server that does not exist: install the
model server first, then list it. Each entry renders one ModelServer; `schedulingWeight` and
`memoryMiB` render under `spec.scheduling` when set. vLLM is the currently preferred engine,
so the examples lead with it.

A `vllm` ModelServer has no `endpoint`. Its `serviceRef` (the Service of a vLLM install such
as `kubemoot-vllm`) or its `servers`, and its `sleepLevel`, are copied into the spec as
they are:

```yaml
modelServers:
  - name: vllm-gpu
    type: vllm
    serviceRef: {name: kubemoot-vllm, namespace: vllm}   # a kubemoot-vllm release named kubemoot-vllm
  - name: vllm-external
    type: vllm
    sleepLevel: 1
    servers:
      - {model: "qwen3:14b", endpoint: "http://vllm-14b.models:8000"}
```

An `ollama` ModelServer names its `endpoint`. Ollama is the engine of the quickstart and
remains supported:

```yaml
modelServers:
  - name: ollama-gpu-a
    type: ollama
    endpoint: http://ollama.ollama-a:11434
    schedulingWeight: 100
  - name: ollama-cpu
    type: ollama
    endpoint: http://ollama.ollama-cpu:11434
    memoryMiB: 20480   # host RAM the CPU server may spend on models
```

`namespace` defaults to the release namespace.

The chart declares no Model for a `vllm` ModelServer: a vLLM server reports every model it
serves, and the `kubemoot-vllm` chart declares each model's parameters and abilities. The
scheduling crew the chart renders ships no Model either; like every crew it selects from the
catalog (see [The scheduling crew](../architecture/scheduler.md#the-scheduling-crew)).

A server entry may list `models`, the cluster's catalog entries for it. Each renders one
Model in the ModelServer's namespace, named for the server and the model (`ollama-gpu-qwen3-14b`).
An Ollama server downloads each model that is missing. Only `model` is needed for Ollama,
because the operator reads the size, context window and abilities from the server:

```yaml
modelServers:
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.ollama:11434
    models:
      - model: qwen3:32b
      - model: qwen3:14b
      - model: gemma3:4b
        disabled: true          # keep it out of the catalog
```

An entry may also set `parameters`, `capabilities`, `contextLength`, `vramMib` and `labels`,
for what the server cannot report or the owner wants to add (a data-residency label, for
example). A vLLM model served by the `kubemoot-vllm` chart needs no entry: the chart declares
its parameters and capabilities (see below).

#### vLLM models declared in the kubemoot-vllm chart

A vLLM model is declared once, in the `kubemoot-vllm` chart that starts its server. Each
entry of the chart's `models` takes optional `parameters` and `capabilities`:

```yaml
models:
  - servedName: qwen3:14b
    repo: Qwen/Qwen3-14B-AWQ
    revision: 31c69efc
    parameters: 14B
    capabilities: [tool-calling, reasoning]
    args: [--enable-auto-tool-choice, --tool-call-parser=hermes, --reasoning-parser=qwen3]
```

The chart publishes them on its Service as the annotation `kubemoot.ai/models`, a JSON list
of `{name, parameters, capabilities}`. A ModelServer whose `serviceRef` names that Service
reads the annotation, so the model joins the catalog with its size and abilities and no Model
object is needed. Changing them does not restart the servers. A server that lists its
`servers` has no Service to read; declare those models with a Model's `spec.parameters` and
`spec.capabilities`.

## Model

Declares a specific inference model on a server, **labeled** so the scheduler can select it. The operator pulls the model if needed and tracks its lifecycle.

### Spec

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `serverRef` | string | Yes | Name of the ModelServer CR |
| `model` | string | Yes | The served name: the name every request uses to ask for this model (e.g., `qwen3:32b`), whatever engine serves it. Where the weights come from is `source`. |
| `source` | object | No | Where the weights come from, as exactly one of `ollama` or `huggingFace`. Absent means an Ollama tag equal to `model`, so existing Models need no change. |
| `vramMib` | int32 | For GPU servers | Capacity request - used by the scheduler's filter step |
| `contextLength` | int | No | Override maximum context window |
| `quantization` | string | No | Quantization method (e.g., `q4_K_M`, `q8_0`) |
| `parameters` | string | No | The total parameter count, such as `14B`, `0.5B` or `751M`. Declare it where the engine cannot report it (vLLM does not) or to correct a wrong report. The size class in the [model catalog](#the-model-catalog) is derived from it. |
| `capabilities` | list | No | Abilities the engine cannot report: `tool-calling`, `reasoning`, `vision`. When set they replace what the engine reports. |
| `disabled` | bool | No | Keep the model out of the catalog. Its state is `Disabled`, the operator downloads nothing for it, no crew selects it, and calibration skips it. |

### Source

`source` holds exactly one of these fields; setting none or both is rejected when the Model is created.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ollama.tag` | string | Yes, within `ollama` | The Ollama tag to pull. It must equal `model`, because Ollama serves a model only under its tag; a different value is rejected, and so is changing `model` while an `ollama` source is set. |
| `huggingFace.repo` | string | Yes, within `huggingFace` | The Hugging Face repository, for example `Qwen/Qwen3-14B-AWQ` |
| `huggingFace.revision` | string | Yes, within `huggingFace` | The commit sha the weights are pinned to, 7 to 40 hex characters. A branch or tag name is rejected. |
| `huggingFace.quantization` | string | No | The quantization of the repository's weights, for example `awq` or `gptq` |

The source must fit the engine of the server: `ollama` for an ollama server, `huggingFace` for a vLLM server. A Model on a `vllm` ModelServer needs no `source`: the server decides its weights. A `huggingFace` source on one is information about what the server runs, and an `ollama` source is a mismatch. The operator never guesses or falls back. A source that does not fit is reported on the Model's `SourceCompatible` condition (reason `SourceEngineMismatch`, with a message naming the mismatch), the Model's state is `Error`, and nothing is pulled. A source that fits sets the condition to `True`. `source` cannot be changed after the Model is created, with one exception: an `ollama` source (tag equal to `model`) may be added to a Model that has none, which keeps the same meaning and does not download the weights again. To change anything else, delete and recreate the Model.

### Labels (open string set, selected by `CrewSchedulingPolicy`)

| Label | Purpose | Examples |
|-------|---------|----------|
| `family` | Model family | `qwen3`, `mistral`, `glm`, `deepseek-r1`, `phi3` |
| `params` | Parameter count | `"3B"`, `"8B"`, `"14B"`, `"24B"`, `"32B"` |
| `capability/<name>` | What the model can do | `capability/tool-calling: "true"`, `capability/reasoning: "true"` |
| `contextWindow` | Max context tokens | `"32768"`, `"131072"`, `"200000"` |
| `latencyClass` | Retired. Read only for a crew's own Models, as `low` = small, `medium` = medium, `high` = large; the catalog carries `kubemoot.ai/size` instead | `low`, `medium`, `high` |

### Status

| Field | Description |
|-------|-------------|
| `state` | `Pending`, `Pulling`, `Available`, `Loaded`, `Error`, `Disabled` |
| `pull` | While `Pulling`: `completedBytes`, `totalBytes` and `percent` of the download. `totalBytes` grows as the registry announces layers. Cleared when the model is on the server. |
| `ready` | Model ready for inference |
| `conditions` | `Ready`; `SourceCompatible`: whether `source` fits the server's engine; on a `vllm` ModelServer also `ServedNameFound`: whether the ModelServer's servers report `model` as a served name, and `ServedRepoMatches`: whether the server reports the repository a `huggingFace` source names (present only when the server says) |
| `endpoint` | Inference endpoint (from server) |
| `modelInfo` | Size, parameters, family, quantization, context length, format, digest |

A model the server does not list is downloaded in the background, so the Model
reconcile returns at once and deleting or changing other Models is never held up by a
download. Models with the same tag on the same server share one download. Deleting
the last Model that uses a tag cancels its download in progress and removes the
model from the server. A server that is not Ready (its server is scaled to zero, say) cannot be asked, so
the Model is released without removing the file; the file stays on that server's disk for its
owner to remove. A Ready server whose delete fails keeps the Model until the delete works or
the server stops being Ready. The partial files of the cancelled download stay on the
server's disk and are counted in `storage.partialBytes` (see the ModelServer status). If the operator restarts mid-download, the Model
stays `Pulling`, the pull starts again, and the server resumes the partial files.
A failed download reports `Error` with the reason and is retried.

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-32b
  labels:
    family: qwen3
    params: "32B"
    capability/tool-calling: "true"
    capability/reasoning: "true"
    contextWindow: "32768"
spec:
  model: qwen3:32b
  serverRef: ollama-a
  vramMib: 20480
  quantization: q4_K_M
---
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-8b
  labels:
    family: qwen3
    params: "8B"
    capability/tool-calling: "true"
    contextWindow: "131072"
spec:
  model: qwen3:8b
  serverRef: ollama-b
  vramMib: 5120
  quantization: q4_K_M
```

These labels are for a crew that still ships its own Models, which it uses instead of the catalog until it deletes them (see [Scheduler](../../architecture/scheduler/#crew-local-models-replace-the-catalog)). A crew that ships none selects from the catalog by the labels in [the model catalog](#the-model-catalog); the cluster owner's Models carry only what the engine cannot report and any labels of their own.

A Model for a vLLM server needs no source: the vLLM server decides its own weights, and the operator downloads and pulls nothing for it. What the Model must get right is its served name. `model` is the name the server is started with (`--served-model-name`), so crews and policies that select the Model by label do not change when the engine does. The name appears twice, in the install values of the server (`servedName` in the `kubemoot-vllm` chart) and in the Model, and the operator checks them against each other: it asks the ModelServer's servers which names they report, and a `model` no server reports is an `Error` whose `ServedNameFound` condition is `False` with the reason `ServedNameNotReported` and a message that lists the names reported. It never guesses. While the server has not found its servers yet the Model is `Pending`.

The Model is `Available` when its server answers (state `Loaded` while the server is awake, `Available` while it sleeps, since a sleeping server wakes on demand). Its `status.endpoint` is the server's URL. A `huggingFace` source is optional and informational: when the server reports where it loaded its weights from, the operator compares that repository with `source.huggingFace.repo` and sets the `ServedRepoMatches` condition (`True`, or `False` with the reason `ServedRepoDiffers`) without failing the Model. A server that does not say, or a Model with no source, has no such condition, and the revision cannot be read from the server, so it is never checked. The server is the `vllm-gpu` server from [vLLM servers](#vllm-servers):

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Model
metadata:
  name: qwen3-14b
  labels:
    family: qwen3
    params: "14B"
    capability/tool-calling: "true"
spec:
  model: qwen3:14b
  serverRef: vllm-gpu
```

`kubectl get mdl` (short name: `mdl`).

## EmbeddingModel

Declares an embedding model for RAGSource indexing. Not consumed by chat agents; RAGSources reference EmbeddingModels via `spec.embeddingModelRef`. It is a separate resource from `Model` because its lifecycle and consumers differ (see the [RAGSource design notes](../ragsource-guide/#design)).

### Spec

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `model` | string | (required) | Model name (e.g., `nomic-embed-text`). A crew names the model, never the server: stored vectors match only the model that made them. |
| `dimensions` | int32 | 768 | Vector dimension size. A server that reports a different size for the model is not bound. |
| `serverRef` | string | (none) | Name of the ModelServer CR to pin the model to. Leave it out and the cluster's catalog supplies a server (see Binding below). |
| `batchSize` | int32 | 32 | Max texts per embedding request |

### Status

| Field | Description |
|-------|-------------|
| `state` | `Pending`, `Pulling`, `Available`, `Error` |
| `pull` | While `Pulling`: `completedBytes`, `totalBytes` and `percent` of the download, as on a Model. Cleared when the model is on the server. |
| `ready` | Model ready for embedding |
| `boundServer` | The ModelServer that serves the model: `spec.serverRef` when set, otherwise the server the operator bound it to. Empty while none has it. |
| `endpoint` | Embedding API endpoint (from the bound server). RAGSource indexers, query services and the MCP gateway tool index read this, not the spec. |
| `conditions` | `Ready`, and `Bound`: `True` with the server named, or `False` with reason `NoServer` naming the model and what the cluster owner can add |
| `modelInfo` | Dimensions, max input tokens, family |

### Binding

An EmbeddingModel with no `serverRef` binds to a ready `ollama` ModelServer whose
`status.embeddingModels` lists its model. Ollama reports the `embedding` capability from
`/api/show`; the operator keeps those models out of `status.models` (the chat catalog that
scheduling rules match) and lists them, with the vector size the engine reports, in
`status.embeddingModels`. When several servers qualify, the operator chooses in this order:

1. a server that has the model now, before one that is only declared to hold it;
2. the server the model is already bound to, so a second server does not move it;
3. namespace, then name, so the choice does not depend on list order.

A server that reports a vector size other than `spec.dimensions` is skipped, and the
`NoServer` message names it. The model name stays in the crew because the same model on
any server makes the same vectors; switching the embedding server is a change to the
cluster (add the model to another ModelServer, remove it from the old one), never to the
crew. The EmbeddingModel re-binds when the bound server is removed, is not ready, or stops
listing the model, and the new endpoint reaches RAGSources (their query services and the
next indexing job) through `status.endpoint`.

If no server has the model, the `Bound` condition is `False` with reason `NoServer` and
says which model is missing. A model is downloaded only where the cluster owner asked for
it: a Model object (`kmctl model add`) in a ModelServer's namespace that names the model on
that server is the same request it is for a chat model, and the operator pulls it there and
binds to it. Without one, the condition tells the owner to pull the model onto a server or
add that Model. With `serverRef` set nothing changes: the model is looked up on that
server and downloaded there when missing, as for a crew-local Model.

An embedding model the server does not list is downloaded in the background on the
same pull a Model uses, so the reconcile returns at once, a Model and an EmbeddingModel
with the same tag on the same server share one download, and deleting the last one that
wants the tag cancels it.

### Example

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: nomic-embed
spec:
  model: nomic-embed-text
  dimensions: 768
```

Pinned to one server (crew-local, like a crew's own Models):

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: EmbeddingModel
metadata:
  name: nomic-embed
spec:
  model: nomic-embed-text
  serverRef: ollama-a
```

`kubectl get emb` (short name: `emb`).

## How Agents Reach a Model

The Agent CR does **not** name a model or a server. It declares capabilities:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Agent
metadata:
  name: kubectl-agent
spec:
  capabilities: [tool-calling, kubernetes]
  discussRole: tooler
```

The crew's `CrewSchedulingPolicy` declares which models satisfy each discussion phase via Kubernetes-style label selectors:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: CrewSchedulingPolicy
metadata:
  name: homelab-pilot-default
  namespace: crew-homelab-pilot
spec:
  crewRef: homelab-pilot
  rules:
    - phase: mulling
      require:
        matchLabels: { capability/tool-calling: "true" }
        matchExpressions:
          - { key: params, operator: In, values: ["14B", "32B"] }
      prefer:
        - weight: 100
          selector:
            matchLabels: { family: qwen3, params: "32B" }
    - phase: triage
      require:
        matchLabels: { capability/tool-calling: "true" }
      prefer:
        - weight: 100
          selector:
            matchLabels: { kubemoot.ai/size: small }
```

The scheduler computes a `(model, server, endpoint)` per agent via filter → score → bind and templates the result into the agent's Deployment as `KUBEMOOT_MODEL_MODEL`, `KUBEMOOT_MODEL_ENDPOINT`, `KUBEMOOT_TRIAGE_MODEL_MODEL_ID`, `KUBEMOOT_TRIAGE_MODEL_ENDPOINT`. See [Scheduler](../../architecture/scheduler/) for the algorithm.

### application.properties wiring

The agent-runtime is Quarkus + LangChain4j. The `quarkus.langchain4j.ollama.chat-model.model-id` property is `BUILD_AND_RUN_TIME_FIXED` and cannot be overridden via env vars at runtime. The operator writes resolved values to `application.properties` in the `{agent}-policy` ConfigMap, mounted at `/app/config/application.properties` (SmallRye ordinal 260, overrides in-jar ordinal 250 at startup).

```properties
quarkus.langchain4j.ollama.chat-model.model-id=qwen3:32b
quarkus.langchain4j.ollama.base-url=http://ollama.ollama-a:11434
quarkus.langchain4j.ollama.chat-model.temperature=0.3
quarkus.langchain4j.ollama.chat-model.num-predict=2048
```

`temperature` and `num-predict` come from `Agent.spec.temperature` and `Agent.spec.maxTokens`.

## Load on Demand - We Do Not Preload

Kubemoot loads a model into server VRAM **when an agent asks for it, not before**. There is no preload field, no warm-up CRD, no eager-loading hook. This is intentional design, not a missing feature.

### The principle

> Load a model when an agent needs it. Do not tie up a GPU with a model it's not needed.

GPU VRAM is shared capacity. Pinning model X in advance for a guess at what crew A will use starves model Y for whichever agent actually fires next. Any system that hand-declares preload intent is making predictions about future agent demand that the scheduler can - and should - make at request time.

### What handles the cold-load case

When the first inference of the session hits a cold server, Ollama loads the model from disk to VRAM. This takes **30-60s** for a 30B-class model on an RTX 5090 (and longer on the 4090). After that, the model is resident.

Two mechanisms make this cost acceptable:

1. **`OLLAMA_KEEP_ALIVE=1h`** - once loaded, the model stays in VRAM for an hour of inactivity. Every subsequent query within the session is warm.
2. **`CrewSchedulingPolicy` `prefer` weights** - steer the agent's default model toward the phase's latency profile; the runtime then prefers a copy that is already resident when it picks where each call runs.

The cold-load tax is paid once per `(model, server)` per session. If a user complains that "the first response was slow," the answer is **not** to add preload. The answer is to verify that subsequent queries are fast (they will be), and to consider session-shaping if the cold cost is unacceptable for the workload.

### When to push back on preload

Future requests in the form *"can we just preload model X on GPU Y"* should be re-framed:

1. What problem is the preload solving? (Usually: "first query is slow.")
2. Is the slowness on the *first* query only, or on every query? (If only first: `KEEP_ALIVE` handles the rest - accept the cold cost.)
3. If genuinely needed, address at the *infrastructure* layer: increase `KEEP_ALIVE`, tune the scheduler, or admit that the workload pattern is wrong for shared GPU capacity.
4. Pin VRAM only as a last resort, and never via a chart - manual `curl /api/generate keep_alive=-1` is fine for one-off debugging.

The architectural commitment: **the orchestrator decides what to load. The chart declares what's possible (Model CRs with labels), not what's resident.**

Calibration is not preload: it loads a model to measure it, never while other calls hold
the server, and unloads what it loaded when the calibration is recorded (see
Calibration above).

## Model Catalog

> Sizes below are approximate and move with each quantization release. Treat them as
> planning figures and validate the real footprint against
> `ModelServer.status.capacity` after a cold load.

### Tier 1: Frontier Models (Cloud-Routed, Not Locally Hostable)

These mixture-of-experts families are one to two orders of magnitude beyond a single consumer GPU. On local runtimes they generally exist only as cloud-routed tags, which resolve to the vendor's hosted infrastructure rather than your hardware. Reaching them is a deliberate decision to move inference off the premises.

| Model | Org | Total Params | Active | Tool Calling | Ollama Tag | Self-Host Footprint |
|-------|-----|-------------|--------|--------------|------------|---------------------|
| Kimi K2 family (K2.6, K2.7-Code, K3) | Moonshot AI | 1T MoE | ~32B | Native | `kimi-k2.6:cloud`, `kimi-k3`, `kimi-k2.7-code` | ~630 GB full weights; ~240 GB at 1.8-bit dynamic quant; roughly 4x H200 to self-host |
| GLM-5.2 | Z.ai | MoE | - | Native | `glm-5.2:cloud` | ~223 GB at 1-bit; 256 GB+ at 2-bit |
| DeepSeek V4 Pro | DeepSeek | MoE | - | Native | `deepseek-v4-pro:cloud` | Data-centre class |
| MiniMax M3 | MiniMax | MoE | - | Yes | `minimax-m3` | Data-centre class |

For scale: four H200 class accelerators run roughly $120,000 to $160,000 to buy, or about $4 to $18 per hour to rent. That is the price of admission to this tier, which is why the practical homelab question is which 20B to 35B class model calls tools best.

These tiers are not reachable from Kubemoot today: hosted providers are on the [Roadmap](../../introduction/roadmap/#cloud-and-frontier-models-at-the-table).

### Tier 2: Locally Hostable Models (20B-35B Class)

The working range for a single GPU with 24 GB to 48 GB of memory, which is roughly a $5,000 card. Comfortable on a 32 GB card at Q4; workable on a 24 GB card at Q4 with less KV cache headroom.

> **MoE caveat:** mixture-of-experts models load **all** expert weights into VRAM, so size follows *total* params. The active-param count buys **speed**, not VRAM savings. Validate the real cold-load footprint against server capacity, not the active-param number.

| Model | Org | Params | Tool Calling | Ollama Tag | Approx Q4 | Notes |
|-------|-----|--------|--------------|------------|-----------|-------|
| Qwen3.6 35B-A3B | Alibaba | 35B MoE (3B active) | Yes | `qwen3.6:35b` | ~20 GB | Strong reported agentic tool use for its class. MoE speed at 35B footprint. |
| Qwen3.6 27B | Alibaba | 27B dense | Yes | `qwen3.6:27b` | ~17 GB | Long context. Fits a 24 GB card with headroom. |
| Laguna XS 2.1 | - | 33B MoE (3B active) | Yes | `laguna-xs-2.1` | ~20 GB | Agentic-coding tuned. |
| Nemotron 3 | NVIDIA | 33B | Native (agentic) | `nemotron3:33b` | ~20 GB | Agentic-tuned. NVIDIA-Open license. |
| Gemma 4 26B / 31B | Google | 26B / 31B | Yes | `gemma4:26b`, `gemma4:31b` | ~16 / ~19 GB | Large install base, permissive. Reported tool use is weaker than the Qwen3.6 class; prefer elsewhere for Toolers. |
| Granite 4.1 30B | IBM | 30B | Yes | `granite4.1:30b` | ~18 GB | Built around function calling. Permissive license. |
| LFM2 24B | Liquid AI | 24B | Yes | `lfm2:24b` | ~14 GB | Fits both reference GPUs at Q4. |
| MiniMax M2.7 | MiniMax | - | Yes | `minimax-m2.7` | - | Validate footprint before committing a phase to it. |
| Qwen3 32B | Alibaba | 32B dense | Yes | `qwen3:32b` | ~20 GB | Long-standing default in the reference crew. Superseded by the Qwen3.6 class on tool use. |

### Tier 3: Small Models (3B-14B Class)

Suitable for Tooler agents, low-priority discussion participants, or a 24 GB card with room to spare.

| Model | Org | Params | Tool Calling | Ollama Tag | Approx Q4 | Notes |
|-------|-----|--------|--------------|------------|-----------|-------|
| Qwen3.5 9B | Alibaba | 9B | Yes | `qwen3.5:9b` | ~6 GB | Current small Qwen line. |
| Granite 4.1 8B | IBM | 8B | Yes | `granite4.1:8b` | ~5 GB | Function-calling focus at Tooler size. Permissive license. |
| Granite 4.1 3B | IBM | 3B | Yes | `granite4.1:3b` | ~2 GB | Large co-schedule headroom. |
| LFM2.5 8B | Liquid AI | 8B | Yes | `lfm2.5:8b` | ~5 GB | Low-latency Tooler candidate. |
| Gemma 4 12B | Google | 12B | Yes | `gemma4:12b` | ~8 GB | Permissive, widely available. |
| Qwen3 14B | Alibaba | 14B | Yes | `qwen3:14b` | ~9 GB | Dual-mode (thinking / non-thinking). |
| Qwen3 8B | Alibaba | 8B | Yes | `qwen3:8b` | ~5 GB | Dual-mode. Long-standing Tooler default in the reference crew. |

> A model with a toggleable thinking mode can regress badly in a Tooler slot when
> reasoning is disabled, because it stops planning which tool to call. Confirm the mode
> the runtime actually sends rather than assuming the default.

## GPU Compatibility

Measured on the reference homelab: an RTX 5090 node (`ollama-a`) and an RTX 4090 node
(`ollama-b`). The quantization, VRAM, and headroom figures below are specific to that
hardware; treat them as a worked example of the budgeting method in
[Choosing a Model](../../concepts/choosing-a-model/), not as universal numbers for a
different card.

### RTX 5090 - 32 GB VRAM (`ollama-a`)

| Model | Quantization | VRAM | KV Headroom | Fits? | Recommended Labels |
|-------|-------------|------|-------------|-------|--------------------|
| Qwen3.6:35b-a3b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=qwen3.6, params=35B, capability/tool-calling=true, capability/reasoning=true` |
| Qwen3.6:27b | Q4_K_M | ~17 GB | ~15 GB | Yes | `family=qwen3.6, params=27B, capability/tool-calling=true` |
| Qwen3:32b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=qwen3, params=32B, capability/tool-calling=true, capability/reasoning=true` |
| Laguna XS 2.1 | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=laguna, params=33B, capability/tool-calling=true` |
| Nemotron3:33b | Q4_K_M | ~20 GB | ~12 GB | Yes | `family=nemotron3, params=33B, capability/tool-calling=true` |
| Granite4.1:30b | Q4_K_M | ~18 GB | ~14 GB | Yes | `family=granite4.1, params=30B, capability/tool-calling=true` |
| Gemma4:31b | Q4_K_M | ~19 GB | ~13 GB | Yes | `family=gemma4, params=31B, capability/tool-calling=true` |
| LFM2:24b | Q4_K_M | ~14 GB | ~18 GB | Yes | `family=lfm2, params=24B, capability/tool-calling=true` |
| Qwen3:14b | Q4_K_M | ~9 GB | ~23 GB | Yes | `family=qwen3, params=14B, capability/tool-calling=true` |
| Qwen3:8b | Q4_K_M | ~5 GB | ~27 GB | Yes | `family=qwen3, params=8B, capability/tool-calling=true` |
| Any 32B class | Q8_0 | ~35 GB | n/a | No | Exceeds VRAM |

### RTX 4090 - 24 GB VRAM (`ollama-b`)

> **Constraint:** `num_parallel=1` required - `num_parallel=2` causes page cache OOM preventing model reload.

| Model | Quantization | VRAM | KV Headroom | Fits? | Recommended Labels |
|-------|-------------|------|-------------|-------|--------------------|
| Qwen3.6:27b | Q4_K_M | ~17 GB | ~7 GB | Yes | `family=qwen3.6, params=27B, capability/tool-calling=true` |
| LFM2:24b | Q4_K_M | ~14 GB | ~10 GB | Yes | `family=lfm2, params=24B, capability/tool-calling=true` |
| Gemma4:12b | Q4_K_M | ~8 GB | ~16 GB | Yes | `family=gemma4, params=12B, capability/tool-calling=true` |
| Qwen3:14b | Q4_K_M | ~9 GB | ~15 GB | Yes | `family=qwen3, params=14B, capability/tool-calling=true` |
| Qwen3.5:9b | Q4_K_M | ~6 GB | ~18 GB | Yes | `family=qwen3.5, params=9B, capability/tool-calling=true` |
| Granite4.1:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=granite4.1, params=8B, capability/tool-calling=true` |
| LFM2.5:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=lfm2.5, params=8B, capability/tool-calling=true` |
| Qwen3:8b | Q4_K_M | ~5 GB | ~19 GB | Yes | `family=qwen3, params=8B, capability/tool-calling=true` |
| Granite4.1:3b | Q4_K_M | ~2 GB | ~22 GB | Yes | `family=granite4.1, params=3B, capability/tool-calling=true` |
| Any 32B class | Q4_K_M | ~20 GB | ~4 GB | Tight | Loads, but too little KV cache for long discussions |
| Any 24B class | Q8_0 | ~25 GB | n/a | No | Exceeds VRAM |

## Choosing Among These

The selection framework lives in [Choosing a Model](../../concepts/choosing-a-model/): rank on tool-call fidelity rather than coding benchmarks, budget VRAM with KV cache headroom, size mixture-of-experts models by total parameters, match model size to discussion role, and decide by fitness measurement rather than by leaderboard.

Prefer permissive licenses (Apache-2.0, MIT, and comparable) and avoid non-commercial terms.

## Troubleshooting

### Model stuck in Pending

The controller checks if the model exists on the server. For Ollama, it calls the `/api/tags` endpoint. If the model isn't pulled yet, pull it manually:
```bash
kubectl exec -n ollama deploy/ollama -- ollama pull qwen3:32b
```

### Agent not using the expected model

Check the agent's scheduling status:
```bash
kubectl get agent <name> -o jsonpath='{.status.phase}{"\n"}{.status.message}{"\n"}'
```

If the phase is `Unschedulable`, the `CrewSchedulingPolicy` `require` selectors didn't match any feasible Model. Check Model labels:
```bash
kubectl get models --show-labels
```

Either relax the `require` selectors or add a Model that matches them.

### Temperature and other inference settings

`temperature` and `maxTokens` live on the Agent CR. They're written to `application.properties` by the operator and cannot be overridden via environment variables (Quarkus `BUILD_AND_RUN_TIME_FIXED` constraint). Update the Agent CR and wait for the operator to regenerate the ConfigMap.

## Migrating from ModelProvider

The kind `ModelProvider` is now `ModelServer`. The rename is clean: there is no alias, no conversion, and the operator does not read a `ModelProvider` object, so the old kind does nothing once the new operator runs. "Provider" no longer names a model server anywhere in the API.

| Before | After |
|--------|-------|
| `kind: ModelProvider`, `kubectl get modelproviders` | `kind: ModelServer`, `kubectl get modelservers` |
| `spec.providerRef` on `Model` and `EmbeddingModel` | `spec.serverRef` |
| Operator chart value `modelProviders` | `modelServers` |
| Agent labels `kubemoot.ai/mulling-provider` and `kubemoot.ai/triage-provider` | `kubemoot.ai/mulling-server` and `kubemoot.ai/triage-server` |
| `topologyKey: provider` in a `CrewSchedulingPolicy` | `topologyKey: server` |
| NATS buckets `kubemoot_provider_state` and `kubemoot_provider_tickets` | `kubemoot_server_state` and `kubemoot_server_tickets` |
| NATS subject `kubemoot.provider.model.<name>` | `kubemoot.server.model.<name>` |
| Bus message fields named `provider` (server state, ticket, latency samples, scheduler performance, signal and discussion metadata, lifecycle messages) | `server` |
| Metrics `kubemoot_provider_*` and the `provider` metric label | `kubemoot_server_*` and `server` |
| `accelerator-mcp` tools `list_providers`, `provider_state`, `calibrate_provider` | `list_servers`, `server_state`, `calibrate_server` |
| Dashboard pages and API under `modelproviders` | `modelservers` |

The bus contract is a new major version because the bucket, subject and field names changed; the agent runtime and the operator must run the same release. The upgrade removes the retired NATS buckets `kubemoot_provider_state` and `kubemoot_provider_tickets` by itself: the chart's NATS bootstrap job deletes those two buckets when they exist and touches no other bucket. A cluster that uses an external NATS (`nats.enabled` false) keeps them until you run `nats kv del <bucket> -f` for each.

To move a cluster:

1. In your manifests and Helm values, rename the kind, `providerRef` to `serverRef`, and `modelProviders` to `modelServers`. Apply the `ModelServer` objects before the `Model` objects that name them.
2. Upgrade the operator chart. The chart applies the new CRDs. Leave the old `modelproviders.kubemoot.ai` CRD installed until the upgrade finishes, because Helm removes the `ModelProvider` objects an earlier release rendered by kind, and that needs the CRD.
3. After the upgrade completes, delete the old CRD once, which also deletes any `ModelProvider` object still in the cluster (none carries a finalizer): `kubectl delete crd modelproviders.kubemoot.ai`.

## Related

- [Scheduler](../../architecture/scheduler/) - Filter / score / bind algorithm and CrewSchedulingPolicy reference
- [Agent CRD](agent.md) - Agent CRD spec
- [KubemootConfig Guide](kubemootconfig-guide.md) - Image versioning and operator config
