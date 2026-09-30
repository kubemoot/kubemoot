---
title: "Quickstart"
weight: 50
description: "Try Kubemoot on a laptop, no GPU required, in one script."
---

This is a laptop trial: one script takes an empty Kubernetes cluster to a real crew
answering a real question, on CPU, with no GPU required. It runs the same protocol as
a production deployment - a coordinator convening a Tooler, signals exchanged over
NATS, an answer synthesized - just smaller and slower, because it runs on CPU with a
1-3B model instead of a GPU with a reasoning-sized one. See
[Installation](../installation/) for the GPU-backed deployment this trial is a preview
of, and the profile table below for exactly what's smaller and slower.

## Run it

```bash
git clone https://github.com/kubemoot/kubemoot.git && cd kubemoot
kind create cluster --name kubemoot     # or any cluster your kubectl points at
./quickstart/quickstart.sh
```

Needs `kubectl`, `helm`, and `python3`. The script needs a node with 2 CPUs and 8 GB
free and takes several minutes on a laptop; the model pull and the first discussion
dominate. Release images are amd64 only today (see the [Roadmap](../roadmap/)), so on
an arm64 machine such as Apple Silicon the images run under emulation or fail to
start with an exec format error. Every wait polls
a Kubernetes status field rather than sleeping a fixed time, so the script finishes as
soon as the cluster is actually ready, not on a guessed clock.

## What it installs

| Step | What | Why |
|---|---|---|
| 1 | NATS with JetStream | Every discussion is carried over NATS; the operator bootstraps its streams. |
| 2 | Ollama on CPU, pulling `qwen2.5:1.5b` | A small model that runs anywhere, with no GPU. |
| 3 | The operator Helm chart, minimal profile | Admission webhooks, Grafana, and the platform MCP servers are off. |
| 4 | A `ModelProvider` pointing at that Ollama | The endpoint the scheduler binds agents to at inference time. |
| 5 | The `hello` crew: a coordinator plus one Tooler | The smallest crew that actually deliberates; its prompts are ADL rules you can `kubectl apply` and change. |
| 6 | One question through the crew's discussion gateway | The answer comes back as the discussion's synthesis, streamed over Server-Sent Events. |

## The CPU trial profile

This is what's different about a CPU trial versus a GPU deployment, so you can judge
Kubemoot's speed and a crew's size by the right yardstick:

| | CPU trial (this script) | GPU deployment |
|---|---|---|
| Model size | 1-3B parameters (`qwen2.5:1.5b` here) | 8B-32B+ per role, sized to VRAM |
| Agents per crew | Two (a coordinator plus one Tooler) | As many as the crew's domain needs |
| Answer latency | Slower, dominated by CPU inference | Faster, bounded by GPU inference |
| Concurrent discussions | One at a time | Several, bounded by GPU capacity |

## What just happened

You did three things, all declaratively: installed an operator, applied a crew, and
asked it a question. Under the hood the coordinator selected the one Tooler that fits
the question, the Tooler answered from its own knowledge, and the answer was **settled by the crew**
rather than asserted by one model - see
[consensus signal](../../concepts/signals-and-protocol/) for what that means. This is
the same protocol a GPU deployment runs; only the model size, the crew size, and the
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
kubectl get agents,models,crew -n hello
kubectl edit promptmodule hello-facts-system -n hello   # change a rule, ask again
```

Ask another question by rerunning with `QUESTION="..." EXPECT=""` set, or with
`kmctl conversation ask hello "..." -n hello`. The script is idempotent: rerunning it
against the same cluster upgrades in place rather than failing.

CI runs this exact script on a fresh `kind` cluster against every release's images, so
if it breaks, the build is red before you see it. Full details, environment variables,
and what the script does not cover (RAG sources, MCP tool servers, GPU scheduling
across providers, the dashboard) live in
[quickstart/README.md](https://github.com/kubemoot/kubemoot/blob/main/quickstart/README.md).

## Next

- [Installation](../installation/) - install the operator on your own cluster with a
  GPU-backed model provider, for real crew work rather than a trial.
- [Crews & Agents](../../concepts/crews-and-agents/) - the model you just ran.
- [Build a Crew](../../user-guides/build-a-crew/) - author your own, from scratch or
  from the packaged [Homelab Pilot](../../ecosystem/pilot/) chart.
- [Define Fitness Functions](../../user-guides/define-fitness-functions/) - measure
  whether the crew actually does its job.
