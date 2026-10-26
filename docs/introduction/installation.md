---
title: "Install the Operator"
weight: 50
description: "Prerequisites and the Helm install of the Kubemoot operator; other components install separately."
---

This page installs the **Kubemoot operator**: what a cluster needs before it runs, and
how to install it on your own cluster - the accelerator-backed deployment Kubemoot is
built for. If you want to try the protocol first without an accelerator, the
[Quickstart](../quickstart/) runs a smaller CPU-only trial with its own script and its
own limits, described below. To see where the operator, the model server, and your
machines sit in a cluster before you start, read
[How the Pieces Fit Together](../how-the-pieces-fit-together/).

The operator is one part of the ecosystem. The others install separately, each from
its own page:

- **kmctl**, the command-line tool: [kmctl User Guide](../../user-guides/kmctl/).
- **Crews**, packaged as Helm charts, including the reference crew:
  [Crews](../../ecosystem/crews/) and [Pilot](../../ecosystem/pilot/).
- **CrewForge**, the VS Code extension: [CrewForge](../../ecosystem/crewforge/).
- **Integrations** that reach a crew from elsewhere, such as Claude Code:
  [Integrations](../../integrations/).

## Prerequisites

- **`amd64` (x86-64) nodes.** `amd64` is the processor family of Intel and AMD PCs;
  `arm64` is the family of the Raspberry Pi and Apple silicon. Kubemoot's release images
  are `amd64` only today, so on an `arm64` node the image pulls but the
  container does not start: the pod shows `Error` or `CrashLoopBackOff`, and its log
  (`kubectl logs`) says `exec format error`. The [Roadmap](../roadmap/#images-for-arm64) tracks `arm64` images.
- **A Kubernetes cluster** (v1.30+) and `kubectl` configured against it. Cluster-admin
  is required for the initial install, since Kubemoot installs CRDs and cluster-scoped
  RBAC.
- **Helm** (v3.8+, for OCI charts). The operator ships only as a Helm chart.
- **A model server** - at least one inference server on an accelerator that serves the
  models your crew will use. vLLM is the default engine: a moot sends
  overlapping calls to the same model, and vLLM served those faster than Ollama in the
  [measured results](../../concepts/choosing-a-model/#measured-results). Install it with
  the `kubemoot-vllm` chart, as [Install a model server](#install-a-model-server) below
  shows. [Ollama](https://ollama.com/) stays supported, and is the engine of the
  [Quickstart](../quickstart/). A crew composes capability from several small models, so a
  single modest accelerator is enough to start. The accelerator must be visible to the
  worker node, on bare metal or passed through to a VM (see
  [GPU Worker Nodes](../../operating/gpu-worker-nodes/)).
- **NATS JetStream** - the message bus crews deliberate over. It can run in-cluster;
  the operator publishes discussion and event traffic to it. Install it with the
  official [NATS Helm chart](https://docs.nats.io/running-a-nats-service/nats-kubernetes)
  with JetStream enabled.
- **A vector store (pgvector)** - only if a crew uses RAG knowledge sources. Not
  required for a discussion-only crew. See [pgvector](https://github.com/pgvector/pgvector)
  for PostgreSQL with vector search.

## CPU trial vs. accelerator deployment

Kubemoot does not require an accelerator to run. The [Quickstart](../quickstart/) runs on a
node with 2 CPUs and 8 GB free and no accelerator, on Ollama, and its
[CPU trial profile](../quickstart/#the-cpu-trial-profile) table lists what differs from an
accelerator deployment: smaller models, a smaller crew, and slower answers. The rest of
this page installs the accelerator-backed profile, on vLLM.


## Install a model server

Install the model server before the operator, because the operator is pointed at it. vLLM
is the default engine. The `kubemoot-vllm` chart installs it as plain
Kubernetes objects: one pod on one accelerator that runs a vLLM server for each model you
list, a Service with a port per model, and a volume for the weights. Describe the pod, the
volume, and the models in a values file (the full set of values, with the node and memory
requirements, is in [Run vLLM on a GPU node](../../operating/gpu-worker-nodes/#run-vllm-on-a-gpu-node)):

```bash
helm install kubemoot-vllm oci://ghcr.io/kubemoot/charts/kubemoot-vllm \
  --namespace vllm --create-namespace -f values.yaml
```

Each model's `parameters` and `capabilities` go in those values, because vLLM cannot
report them. The chart publishes them on its Service, and the operator reads them into the
cluster's model catalog, so no Model object is needed. The first start downloads the
weights and compiles each model, which takes minutes.

Ollama is the alternative, and the engine the quickstart uses. It suits CPU-only and
small setups, and hosts without the memory to park vLLM weights. Deploy it as an ordinary
workload, for example with the community
[Ollama Helm chart](https://github.com/otwld/ollama-helm), and point the operator at its
Service as shown next.

## Install the operator

The operator ships as the `kubemoot-operator` Helm chart. The chart installs the CRDs,
the controller deployment, and its RBAC.

```bash
helm upgrade --install kubemoot-operator \
  <chart-reference> \
  --namespace kubemoot --create-namespace
```

Replace `<chart-reference>` with the chart source. From a checkout of the repository
you can install the bundled chart directly:

```bash
helm upgrade --install kubemoot-operator \
  operator/chart/kubemoot-operator \
  --namespace kubemoot --create-namespace
```

The published chart is `oci://ghcr.io/kubemoot/charts/kubemoot-operator`; pass it as
the chart reference with `--version` for a specific release.

### Point the operator at your model server

The chart's `modelServers` value is empty, because a chart cannot point at a server that
does not exist. Once your model server is up, list it in a values file. For the vLLM
release installed above:

```yaml
# values.yaml for kubemoot-operator
modelServers:
  - name: vllm-gpu
    type: vllm
    sleepLevel: 1            # the same level as the kubemoot-vllm release
    serviceRef: {name: kubemoot-vllm, namespace: vllm}
```

For Ollama, name the endpoint and the models the cluster should have; Kubemoot downloads
each one the server lacks:

```yaml
modelServers:
  - name: ollama-gpu
    type: ollama
    endpoint: http://ollama.ollama:11434
    models:
      - model: qwen3:14b
```

Pass the file with `--values values.yaml` to the `helm upgrade --install` above. The chart
creates a `ModelServer` for each entry, and a `Model` in the operator's namespace for each
`models` entry. Crews ship no Models: they select from this catalog by label, so changing
the engine or adding a server changes the catalog and no crew.

### Optional: a placement service

The placement service is the one writer that decides where each model call runs. It
picks the model and the accelerator at the moment of the call and owns the call
tickets. Agents that run a harness behind a [Moot Mesh sidecar](../../architecture/moot-mesh/)
ask it where each call runs, and an agent runtime can use it in place of placing calls
in its own process. The chart ships it off by default. Turn it on with two values:

```yaml
placementService:
  enabled: true      # deploy the service: two replicas, only the leader is Ready
  agentsUse: true    # point agents at it
```

`placementService.url` overrides the address agents call; empty means the in-cluster
Service on port 8080. The value reaches each sidecar as `placement.serviceURL` in the
agent's contract. With the service off, every sidecar uses the model server its Agent
was bound to and no call is placed afresh, and an agent runtime places calls in its own
process. Agents without a harness block are not affected by sidecar placement.

### Optional: the dashboard

The operator chart carries the [dashboard](../../operating/dashboard/) as an optional
subchart. It is off by default because the dashboard has no login and can purge NATS
streams and delete models. To install it with the operator:

```bash
helm upgrade --install kubemoot-operator \
  oci://ghcr.io/kubemoot/charts/kubemoot-operator \
  --namespace kubemoot --create-namespace \
  --set dashboard.enabled=true
```

It has no route; reach it with `kubectl port-forward`, as the
[dashboard page](../../operating/dashboard/) shows. The dashboard chart is also published
by itself if you prefer a separate release.

### Optional: metrics and Grafana dashboards

Prometheus and Grafana are a recommended add-on, not a prerequisite. If your cluster
runs the Prometheus Operator, the operator chart creates PodMonitors for Kubemoot's
metrics, and it installs Grafana dashboards for them. Both are values of the same
chart, and [Observability](../../operating/observability/) covers the labels your
Prometheus selects on, how to see the live list of metrics, and how to load the
dashboards into your Grafana.

### Optional: GitOps with Flux

If you already manage your cluster with GitOps, you can skip the `helm` command above.
In a GitOps setup the operator is reconciled from the chart by a Flux `HelmRelease`
pointed at an `OCIRepository`, rather than installed imperatively. Set
`install.crds: Create` and `upgrade.crds: CreateReplace` so CRDs are managed with the
release. This is how the reference deployment runs Kubemoot.

## Verify

```bash
kubectl get pods -n kubemoot
kubectl get crds | grep kubemoot.ai
```

You should see the operator pod `Running` and the Kubemoot CRDs registered
(`crews`, `agents`, `models`, `modelservers`, `mcpservers`, `mcpgateways`,
`ragsources`, `promptmodules`, and the cluster-scoped `kubemootconfig`).

## Put a model server on your accelerator nodes

Kubemoot does not install model servers, and it does not pick accelerator nodes for you. The seam is
deliberate: placement is yours, capacity is discovered. Getting an accelerator into a worker node
in the first place, including VM passthrough, is covered in
[GPU Worker Nodes](../../operating/gpu-worker-nodes/).

1. **You run a model server per accelerator worker node.** A common topology is a worker node
   per accelerator, and a cluster scales by adding such nodes. For every accelerator node, deploy its own
   Ollama (or other server) as an ordinary workload pinned to that node by node
   affinity, requesting the accelerator resource your device plugin exposes (for
   NVIDIA GPUs, `nvidia.com/gpu` with the runtime class your cluster uses). Each such server becomes one ModelServer with its accelerator's memory, and the scheduler
   places models across the servers, loading and evicting them on demand. The CPU
   trial runs a single Ollama Deployment with no accelerator at all.
2. **You declare a `ModelServer` per model server.** One manifest for each: the
   type, that server's Service URL, a weight, and, when the host cannot be measured, a
   memory budget. Two accelerator nodes, two model servers, two ModelServers. This is the only static
   declaration the operator needs.
3. **The operator discovers the rest.** It probes the server for available and loaded
   models, follows the Service to its backing pod, records that pod's node, reads
   `OLLAMA_NUM_PARALLEL`, and, when a Prometheus endpoint is configured, queries the
   DCGM metrics for that node to learn the GPU model and its total and used VRAM (NVIDIA
   GPUs; other accelerators use the memory budget).
   Without DCGM, `spec.scheduling.memoryMiB` is the budget.
4. **Agents choose a server per call.** At each inference call the runtime fits the
   model it needs against every ready server's discovered headroom and picks one. So
   "sized to accelerator memory" in the table above means sized to the memory discovered on the server
   that serves the call, not to the largest accelerator in the cluster: a 32B model needs a
   server whose accelerator holds it, and smaller roles land on the smaller one.

The model server comes first, then the declaration. Besides the chart value shown above, a
`ModelServer` is an ordinary manifest. The example below declares two Ollama servers; for a
vLLM server, set `type: vllm` and `serviceRef` instead of `endpoint`, as
[Run vLLM on a GPU node](../../operating/gpu-worker-nodes/#run-vllm-on-a-gpu-node) shows.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: ollama-gpu-a          # the Ollama pinned to accelerator worker node A
  namespace: kubemoot
spec:
  type: ollama
  endpoint: http://ollama.ollama-a:11434
  scheduling:
    weight: 100
---
apiVersion: kubemoot.ai/v1alpha1
kind: ModelServer
metadata:
  name: ollama-gpu-b          # the Ollama pinned to accelerator worker node B
  namespace: kubemoot
spec:
  type: ollama
  endpoint: http://ollama.ollama-b:11434
  scheduling:
    weight: 100
```

The full field reference, discovery sources, and the budget rules are in
[Models](../../reference/models/).

## What you'll see before a model server is ready

The operator, a crew, and its agents can all exist as Kubernetes resources before a
`ModelServer` is reachable. Nothing crashes; nothing answers either. Knowing what
that state looks like saves you from wondering whether the install is broken:

- **`ModelServer`** reports `status.phase: Failed` and `status.ready: false` with a
  message such as `Failed to connect to Ollama: ...` until the operator can reach the
  endpoint and parse a version response. Once it can, `status.phase` becomes `Ready`.
- **`Agent`** reports `status.phase: Unschedulable` and `status.ready: false` with
  a message such as `no model in the cluster catalog ... satisfies the require of
  phase=mulling` (or a more specific scheduling reason) when the model catalog holds no
  model that satisfies its declared capabilities; an empty catalog says that no server
  reports a model. An
  unschedulable agent gets **no Deployment at all** - there is no crash-looping pod to
  find, because none was created. The operator keeps retrying on a backoff until a
  server appears.
- **`Crew`** follows its agents. It reports `status.phase: Pending` until its
  coordinator `Agent` is `Running`, and `Degraded` with `status.ready: false` when the
  coordinator is `Unschedulable` or when every specialist is, with a message naming
  the agents concerned (`coordinator coordinator is unschedulable: no feasible Model
  for mulling phase`, or `no specialist can be scheduled: k8s-advisor, ...`). A crew
  with a running coordinator and at least one schedulable specialist is `Ready`, and
  its message lists any specialists still unschedulable. `kubectl get crews -A`
  therefore tells you whether a crew can answer before you ask it.

Once a `ModelServer` becomes `Ready` and the operator reconciles, agents move to
`Running` and pick up their Deployments without you having to do anything.

## Next

- [Quickstart](../quickstart/) - deploy a crew and ask it a question.
- [Build a Crew](../../user-guides/build-a-crew/) - author your own crew.
