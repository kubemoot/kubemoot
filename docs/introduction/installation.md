---
title: "Install the Operator"
weight: 40
description: "Prerequisites and the Helm install of the Kubemoot operator; other components install separately."
---

This page installs the **Kubemoot operator**: what a cluster needs before it runs, and
how to install it on your own cluster - the GPU-backed deployment Kubemoot is built
for. If you want to try the protocol first without a GPU, the
[Quickstart](../quickstart/) runs a smaller CPU-only trial with its own script and its
own limits, described below.

The operator is one part of the ecosystem. The others install separately, each from
its own page:

- **kmctl**, the command-line tool: [kmctl User Guide](../../user-guides/kmctl/).
- **Crews**, packaged as Helm charts, including the reference crew:
  [Crews](../../ecosystem/crews/) and [Pilot](../../ecosystem/pilot/).
- **CrewForge**, the VS Code extension: [CrewForge](../../ecosystem/crewforge/).
- **Integrations** that reach a crew from elsewhere, such as Claude Code:
  [Integrations](../../integrations/).

## Prerequisites

- **A Kubernetes cluster** (v1.30+) and `kubectl` configured against it. Cluster-admin
  is required for the initial install, since Kubemoot installs CRDs and cluster-scoped
  RBAC.
- **Helm** (v3.8+, for OCI charts). The operator ships only as a Helm chart.
- **A model provider** - at least one GPU-backed inference endpoint that serves the
  models your crew will use. Kubemoot is built for Ollama today; one or more GPUs are
  the intended target. A crew composes capability from several small models, so a
  single modest GPU is enough to start. The GPU must be visible to the worker node,
  on bare metal or passed through to a VM; running a model server on it is covered in
  [Put a model server on your GPU nodes](#put-a-model-server-on-your-gpu-nodes) below.
  [Ollama](https://ollama.com/) can be installed in-cluster with the community
  [Ollama Helm chart](https://github.com/otwld/ollama-helm).
- **NATS JetStream** - the message bus crews deliberate over. It can run in-cluster;
  the operator publishes discussion and event traffic to it. Install it with the
  official [NATS Helm chart](https://docs.nats.io/running-a-nats-service/nats-kubernetes)
  with JetStream enabled.
- **A vector store (pgvector)** - only if a crew uses RAG knowledge sources. Not
  required for a discussion-only crew. See [pgvector](https://github.com/pgvector/pgvector)
  for PostgreSQL with vector search.

## CPU trial vs. GPU deployment

Kubemoot does not require a GPU to run - the [Quickstart](../quickstart/) proves that
end to end on a 2-CPU, 8 GB node with no accelerator at all. What changes without a GPU
is the profile: smaller models, a smaller crew, and slower answers. Know which profile
you're running before you judge Kubemoot's speed or a crew's size by it.

| | CPU trial (Quickstart) | GPU deployment (this page) |
|---|---|---|
| Model size | 1-3B parameters | 8B-32B+ per role, sized to VRAM |
| Agents per crew | Two to three (a coordinator plus one or two Toolers) | As many as the crew's domain needs |
| Answer latency | Minutes per discussion | Tens of seconds per discussion |
| Concurrent discussions | One at a time | Several, bounded by GPU capacity |
| What it's for | Trying the protocol on a laptop, no GPU required | Real crew work |

The CPU trial is a real discussion, not a mock: a coordinator convenes a Tooler,
signals are exchanged over NATS, and the answer is synthesized - it is simply slower
and smaller because CPU inference and a 1-3B model are slower and smaller than a
GPU-backed reasoning model. The rest of this page installs the GPU-backed profile.

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
(`crews`, `agents`, `models`, `modelproviders`, `mcpservers`, `mcpgateways`,
`ragsources`, `promptmodules`, and the cluster-scoped `kubemootconfig`).

## Put a model server on your GPU nodes

Kubemoot does not install Ollama, and it does not pick GPU nodes for you. The seam is
deliberate: placement is yours, capacity is discovered.

1. **You run one model server per GPU worker node, one GPU per node.** The expected
   topology is a worker node per GPU: each GPU worker node carries a single GPU, and a
   cluster scales by adding such nodes, not by stacking GPUs in one. For every GPU node,
   deploy its own Ollama (or other provider) as an ordinary workload pinned to that node
   by node affinity, requesting `nvidia.com/gpu`, with the runtime class your cluster
   uses for NVIDIA. One model server per GPU node, one GPU per node, is the shape the
   scheduler expects (see ADR 0009): each server becomes one provider with one GPU's
   VRAM, and the scheduler spreads models across the providers. The
   reference homelab has two GPU worker nodes, an RTX 5090 node and an RTX 4090 node,
   and runs one Ollama on each, each from its own infrastructure module with affinity to
   its node's hostname. The CPU trial runs a single Deployment with no GPU at all.
2. **You declare a `ModelProvider` per model server.** One manifest for each: the
   type, that server's Service URL, a weight, and, when the host cannot be measured, a
   memory budget. Two GPU nodes, two Ollamas, two ModelProviders. This is the only static
   declaration the operator needs.
3. **The operator discovers the rest.** It probes the endpoint for available and loaded
   models, follows the Service to its backing pod, records that pod's node, reads
   `OLLAMA_NUM_PARALLEL`, and, when a Prometheus endpoint is configured, queries the
   DCGM metrics for that node to learn the GPU model and its total and used VRAM.
   Without DCGM, `spec.scheduling.memoryMiB` is the budget.
4. **Agents choose a provider per call.** At each inference call the runtime fits the
   model it needs against every ready provider's discovered headroom and picks one. So
   "sized to VRAM" in the table above means sized to the VRAM discovered on the provider
   that serves the call, not to the largest GPU in the cluster: a 32B model needs a
   provider whose GPU holds it, and smaller roles land on the smaller GPU.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-gpu-a          # the Ollama pinned to GPU worker node A
  namespace: kubemoot
spec:
  type: ollama
  endpoint: http://ollama.ollama-a:11434
  weight: 100
---
apiVersion: kubemoot.ai/v1alpha1
kind: ModelProvider
metadata:
  name: ollama-gpu-b          # the Ollama pinned to GPU worker node B
  namespace: kubemoot
spec:
  type: ollama
  endpoint: http://ollama.ollama-b:11434
  weight: 100
```

The full field reference, discovery sources, and the budget rules are in
[Models](../../reference/models/).

## What you'll see before a model provider is ready

The operator, a crew, and its agents can all exist as Kubernetes resources before a
`ModelProvider` is reachable. Nothing crashes; nothing answers either. Knowing what
that state looks like saves you from wondering whether the install is broken:

- **`ModelProvider`** reports `status.phase: Failed` and `status.ready: false` with a
  message such as `Failed to connect to Ollama: ...` until the operator can reach the
  endpoint and parse a version response. Once it can, `status.phase` becomes `Ready`.
- **`Agent`** reports `status.phase: Unschedulable` and `status.ready: false` with
  message `no feasible Model for mulling phase` (or a more specific scheduling reason)
  when no `Model` on a `Ready` `ModelProvider` satisfies its declared capabilities. An
  unschedulable agent gets **no Deployment at all** - there is no crash-looping pod to
  find, because none was created. The operator keeps retrying on a backoff until a
  provider appears.
- **`Crew`** follows its agents. It reports `status.phase: Pending` until its
  coordinator `Agent` is `Running`, and `Degraded` with `status.ready: false` when the
  coordinator is `Unschedulable` or when every specialist is, with a message naming
  the agents concerned (`coordinator coordinator is unschedulable: no feasible Model
  for mulling phase`, or `no specialist can be scheduled: k8s-advisor, ...`). A crew
  with a running coordinator and at least one schedulable specialist is `Ready`, and
  its message lists any specialists still unschedulable. `kubectl get crews -A`
  therefore tells you whether a crew can answer before you ask it.

Once a `ModelProvider` becomes `Ready` and the operator reconciles, agents move to
`Running` and pick up their Deployments without you having to do anything.

## Next

- [Quickstart](../quickstart/) - deploy a crew and ask it a question.
- [Build a Crew](../../user-guides/build-a-crew/) - author your own crew.
