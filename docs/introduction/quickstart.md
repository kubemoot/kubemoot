---
title: "Quickstart"
weight: 40
description: "Try Kubemoot on a laptop, no accelerator required, in one script."
---

This is a laptop trial: one script takes an empty Kubernetes cluster to a real crew
answering a real question, on CPU, with no accelerator required. It runs the same protocol as
a production deployment - a coordinator convening a Tooler, signals exchanged over
NATS, an answer synthesized - just smaller and slower, because it runs on CPU with a
1-3B model instead of an accelerator with a reasoning-sized one. The trial uses Ollama,
which is familiar and runs anywhere; an installation defaults to vLLM. See
[Installation](../installation/) for the accelerator-backed deployment this trial is a
preview of, and the profile table below for exactly what's smaller and slower.

New to Kubernetes, or unsure where each piece runs? Read
[How the Pieces Fit Together](../how-the-pieces-fit-together/) first.

## Prerequisites

- `kubectl`, `helm`, and `python3`.
- A node with 2 CPUs and 8 GB free.
- **`amd64` (x86-64) nodes.** `amd64` is the processor family of Intel and AMD PCs;
  `arm64` is the family of the Raspberry Pi and Apple silicon. Kubemoot's release images
  are `amd64` only today, so on an `arm64` node the image pulls but the
  container does not start: the pod shows `Error` or `CrashLoopBackOff`, and its log
  (`kubectl logs`) says `exec format error`. The [Roadmap](../roadmap/#images-for-arm64) tracks `arm64` images.

## Run it

```bash
git clone https://github.com/kubemoot/kubemoot.git && cd kubemoot
git checkout "$(git describe --tags --abbrev=0 --match 'operator-chart-v[0-9]*' --exclude '*-rc.*')"
kind create cluster --name kubemoot     # or any cluster your kubectl points at
./quickstart/quickstart.sh
```

The `git checkout` selects the latest release, so the manifests next to the script are the
ones written for the chart it installs. The script installs the chart of the release it is
checked out at; if the quickstart files differ from the latest release, it stops and says
so instead of installing a chart they may not match.

The script takes several minutes on a laptop; the model pull and the first discussion
dominate. Every wait polls
a Kubernetes status field rather than sleeping a fixed time, so the script finishes as
soon as the cluster is actually ready, not on a guessed clock.

## What it installs

| Step | What | Why |
|---|---|---|
| 1 | NATS with JetStream | Every discussion is carried over NATS; the operator bootstraps its streams. |
| 2 | Ollama on CPU | The model server of the trial: familiar, and it runs anywhere, with no accelerator. |
| 3 | The operator Helm chart, minimal profile, with the dashboard on, a `ModelServer` for that Ollama, and one model (`qwen2.5:1.5b`) in the cluster's model catalog | The dashboard has no login, so it gets no route; the script prints the `kubectl port-forward` command that opens it. Admission webhooks, Grafana dashboards, the internal MCP servers, the NATS MCP server, and the verify runner are off. The `ModelServer` is the endpoint the scheduler binds agents to at inference time. |
| 4 | The operator marks the `ModelServer` ready and downloads the model onto it | The model is a catalog entry, not part of the crew: the script waits for the download to finish. |
| 5 | The `hello` crew: a coordinator plus one Tooler, with no Models of its own | The smallest crew that actually deliberates; its prompts are ADL rules you can `kubectl apply` and change. |
| 6 | One question through the crew's discussion gateway | The answer comes back as the discussion's synthesis, streamed over Server-Sent Events. |

## The CPU trial profile

This is what's different about a CPU trial versus an accelerator deployment, so you can judge
Kubemoot's speed and a crew's size by the right yardstick:

| | CPU trial (this script) | Accelerator deployment |
|---|---|---|
| Model server | Ollama | vLLM is the default; Ollama is supported |
| Model size | 1-3B parameters (`qwen2.5:1.5b` here) | 8B-32B+ per role, sized to accelerator memory |
| Agents per crew | Two (a coordinator plus one Tooler) | As many as the crew's domain needs |
| Answer latency | Slower, dominated by CPU inference | Faster, bounded by accelerator inference |
| Concurrent discussions | One at a time | Several, bounded by accelerator capacity |

## What just happened

You did three things, all declaratively: installed an operator, applied a crew, and
asked it a question. Under the hood the coordinator selected the one Tooler that fits
the question, the Tooler answered from its own knowledge, and the answer was **settled by the crew**
rather than asserted by one model - see
[consensus signal](../../concepts/signals-and-protocol/) for what that means. This is
the same protocol an accelerator deployment runs; only the model size, the crew size, and the
clock are different.

## Troubleshooting

- **The answer says no agent contributed.** The 1.5B model in the trial sometimes stands
  aside. Rerun the question; the script is idempotent.
- **The NATS pod stays `Pending`.** NATS keeps its stream on a PersistentVolumeClaim, so
  the cluster needs a default StorageClass. `kind` provides one.
- **Ollama restarts or is killed.** The node needs 8 GB free; on a smaller node the
  kernel kills Ollama while it loads the model.
- **A pod shows `ImagePullBackOff`.** Check that the node can reach `ghcr.io`, and that
  it runs on amd64.

## Then

```bash
kubectl get modelservers,models -n kubemoot
kubectl get agents,crew -n hello
kubectl edit promptmodule hello-facts-system -n hello   # change a rule, ask again
```

Ask another question by rerunning with `QUESTION="..." EXPECT=""` set, or with
`kmctl conversation ask hello "..." -n hello`. The script is idempotent: rerunning it
against the same cluster upgrades in place rather than failing.

CI runs this exact script on a fresh `kind` cluster against every release's images, so
if it breaks, the build is red before you see it. Full details, environment variables,
and what the script does not cover (RAG sources, MCP tool servers, accelerator scheduling
across servers, a published dashboard) live in
[quickstart/README.md](https://github.com/kubemoot/kubemoot/blob/main/quickstart/README.md).

## Next

- [Installation](../installation/) - install the operator on your own cluster with a
  accelerator-backed model server, for real crew work rather than a trial.
- [Crews & Agents](../../concepts/crews-and-agents/) - the model you just ran.
- [The starter crew](../../user-guides/starter-crew/) - scaffold a read-only guide to a
  namespace with `kmctl create`, install it, ask it, and change one of its rules.
- [Build a Crew](../../user-guides/build-a-crew/) - author your own, from scratch or
  from the packaged [Homelab Pilot](../../ecosystem/pilot/) chart.
- [CrewForge](../../ecosystem/crewforge/) - develop the crew from VS Code: create, deploy,
  ask, and test it in the editor.
- [Define Fitness Functions](../../user-guides/define-fitness-functions/) - measure
  whether the crew actually does its job.
