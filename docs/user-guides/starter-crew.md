---
title: "The Starter Crew"
weight: 5
description: "The crew kmctl create scaffolds: a read-only guide to its own namespace. Install it, ask it, run its fitness suite, and change one of its rules."
---

`kmctl create` scaffolds a small, complete crew you can install on a fresh Kubemoot
cluster and talk to within minutes. It is the Kubemoot equivalent of the nginx chart
`helm create` produces: a working example you change into your own crew.

The starter crew is a read-only guide to the Kubernetes namespace it is installed into.
Ask it what is running, what is wrong, and why. It reads the namespace with real tools and
answers from what it found. It never changes anything.

[CrewForge](../../ecosystem/crewforge/) scaffolds the same crew with **Create Crew**; see
[Develop a crew in VS Code](../../ecosystem/crewforge/develop-a-crew/).

## What it contains

| Part | Detail |
|---|---|
| Specialists | A coordinator plus 1 to 5 specialists, set by `--members`. Size 1 is `workloads`; sizes 2 to 4 add `events`, `networking`, and `config` in that order; size 5 adds a `reviewer`. |
| Toolers | `workloads` (Pods, Deployments, ReplicaSets, StatefulSets, Jobs), `events` (Warning events, restarts, recent failures), `networking` (Services, endpoints, Ingresses or HTTPRoutes, NetworkPolicies), `config` (ConfigMaps, ServiceAccounts, Secret references). |
| Analyst | `reviewer` checks the answer against the data the Toolers gathered and flags unsupported claims. |
| Tools | One Kubernetes MCP server in read-only mode, behind an MCPGateway. Each specialist enables only the tools for its slice. |
| Access | A namespaced `Role` with `get`, `list`, and `watch`, and no Secret access. |
| Prompts | ADL PromptModules, one per specialist, plus the coordinator's and the shared discussion rules. |
| Fitness | 3 scenarios at size 1, up to 7 at size 5. They pass on a fresh install and ground on the crew's own pods. |

Secrets are not readable at all: Kubernetes cannot grant a Secret's name without its data.
The `config` specialist names the Secrets that pod specs reference instead. Set
`access.clusterWide: true` in `values.yaml` to switch the `Role` to a `ClusterRole` and let
the crew read every namespace; it stays read-only and still has no Secret access.

## Walkthrough

You need a cluster with the Kubemoot operator and a model provider (see
[Installation](../../introduction/installation/)), plus `kmctl` and `helm` on your PATH.

### 1. Create it

```bash
kmctl create hello --chart --members 1 --model-family qwen --no-input
cd hello
```

`--members 5` scaffolds the full crew. The chart's `README.md` repeats these steps.

### 2. Install it

```bash
helm upgrade --install hello . --namespace crew-hello --create-namespace
kmctl crew get hello -n crew-hello      # wait for Ready
```

The prompts carry the namespace name, which Helm fills in at install. A bundle rendered for
one namespace is tied to it; install the chart again for another namespace.

### 3. Ask it something

```bash
kmctl conversation ask hello "List the pods in this namespace and their status." -n crew-hello
```

The answer names the crew's own pods. Check it with `kubectl get pods -n crew-hello`. Ask
the crew to delete something and it declines: it is read-only by its prompts and by its
`Role`.

### 4. Run its fitness suite

```bash
kmctl fitness run -f fitness/fitness.yaml -n crew-hello
kmctl fitness get hello-starter -n crew-hello
```

Each scenario asks a question whose answer the namespace itself proves. The suite sits
outside `templates/`, so installing the chart never starts a run. Delete the suite before
running it again:

```bash
kubectl delete crewfitnesssuite hello-starter -n crew-hello
```

### 5. Change one rule and ask again

The crew's behavior is the ADL in `templates/promptmodules.yaml`. In the `synthesis-prompt`
module, which shapes the coordinator's answer, add one rule:

```text
ALWAYS end with a one-line summary that starts "In short:"
```

Apply it and let the operator roll the agents:

```bash
helm upgrade --install hello . --namespace crew-hello
```

Ask the same question again. The answer now ends with that line. A rule in a specialist's
PromptModule changes what that specialist gathers and reports in the same way; see
[Write Agents and ADL](../write-agents-and-adl/).

## Known limits

- The prompts carry the namespace name, so a bundle is tied to the namespace it was
  rendered for.
- At size 5 the `reviewer` cannot see Tooler findings over 4 KB, which the Toolers post as
  artifact pointers.

## Next

- [Compose a Crew](../compose-a-crew/) - the design decisions behind the lineup.
- [Build a Crew](../build-a-crew/) - the resources a crew is made of.
- [Define Fitness Functions](../define-fitness-functions/) - write scenarios of your own.
- [kmctl reference](../../reference/kmctl/#kmctl-create) - every `kmctl create` flag.
