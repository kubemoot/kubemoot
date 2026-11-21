---
title: "kmctl - the CLI"
weight: 50
description: "The command-line tool for working with crews and Kubemoot."
aliases:
  - /docs/ecosystem/cf-cli/
---

**kmctl** ("kubemoot control") is the command-line tool for working with crews and
Kubemoot. It is the scriptable, terminal-native way to scaffold, apply, and exercise crews;
[CrewForge](../crewforge/) is the VS Code extension for developing crews and talking to
them from the editor.

## Status

`kmctl` is early and actively developed. It is installable from GitHub releases and
ships a working command set covering cluster inspection, crew scaffolding, resource
management, live conversation, fitness suite execution, and shell completion. Check
your installed version with `kmctl version`, and see the GitHub Releases page for the
latest. A small set of subcommands is still planned and marked as such in the
[reference page](../../reference/kmctl/).

## The role it plays

A CLI fits the parts of the workflow that suit a terminal and scripts rather than a
GUI: working with crew manifests, driving Kubemoot from automation, and slotting into
CI/CD pipelines. Because everything Kubemoot manages is a Kubernetes resource, `kmctl`
is a convenience layer over those resources, not a separate control plane. The operator
remains the single source of lifecycle truth.

`kmctl` follows the `kubectl` / `istioctl` / `helm` convention in its flag and output
style. Standard kubeconfig discovery applies: `--kubeconfig`, `--context`,
`-n/--namespace`, and `-A` all work as you would expect. The standard auth flags
(`--cluster`, `--user`, `--as`, and so on) are also supported on every command.

## What you can do with it

The shipped command surface covers eight areas:

**Cluster and operator inspection.** `kmctl status` checks that the cluster is
reachable and that the Kubemoot API group is registered. `kmctl info` reports the
resolved context, namespace, server URL, and server version. `kmctl version` prints
the client version.

**Crew and agent inspection.** `kmctl crew list`, `kmctl crew get <name>`,
`kmctl agent list`, and `kmctl agent get <name>` give a typed, readable view of what
is running, equivalent to `kubectl get` but without needing to know the CRD group and
version. Columns: Ready, Phase, Capacity, Coordinator, Agents, Age (crew); Type, Ready,
Phase, Endpoint, Age (agent). Capacity says whether a model in the cluster catalog can run the
crew's agents. `kmctl crew wait <name>` waits until the crew is reconciled and can answer;
while no model is available it says it is waiting for an accelerator and keeps waiting.

**Crew scaffolding.** `kmctl create <name>` scaffolds a complete, working crew: a
Crew manifest, a coordinator and 1 to 5 specialist Agents with PromptModules in ADL, a
read-only Kubernetes MCP server behind an MCPGateway, a CrewSchedulingPolicy, a starter
CrewFitnessSuite that passes on a fresh install, and a README. The crew is a read-only
guide to its own namespace; see [The starter crew](../../user-guides/starter-crew/). It ships
no models and names no model server: its CrewSchedulingPolicy states what the crew needs,
and the scheduler matches that against the cluster's model catalog. On a TTY it asks for
the display name and the number of specialists.
Pass `--no-input` with explicit flags to make it scriptable. Think of it like
`helm create`: the output is a starting point to customise, not a finished product.

**Prompt and model inspection.** `kmctl prompt list` and `kmctl prompt get <name>`
surface PromptModule resources. Use `-o yaml` to read the full ADL content.
`kmctl model list` shows the cluster's model catalog: every model each model server
offers, with its size, abilities, and context window. Cluster owners add a model to the
catalog with `kmctl model add`. `kmctl model server list` and `kmctl model server get <name>`
show the model servers and what calibration measured. `kmctl model footprint` lists the
Model objects and their state on their model servers.

**Manifest management.** `kmctl apply -f` and `kmctl delete` (which also removes a catalog model with `kmctl delete model NAME`) are CRD-only
server-side-apply wrappers. They accept Crew, Agent, PromptModule, and the rest of
the Kubemoot API, and explicitly refuse non-kubemoot.ai resources, directing you to
`kubectl apply` for those. The operator's finalizers handle cascading cleanup on
delete.

**Live conversation.** `kmctl conversation ask <crew> "<message>"` (alias `conv`)
starts a turn and streams the moot's deliberation in real time, including each
specialist's evaluation and the coordinator's synthesis. Pass `--quiet` to print only
the final answer. Pass `--conversation-id` to continue an existing thread.
`kmctl conversation watch` live-tails an in-flight conversation by ID or a unique
prefix of one, `kmctl conversation list` lists a crew's recent conversations, and
`kmctl conversation workspace` fetches an object from the discussion's workspace, and
`kmctl conversation replay` asks a past conversation's first question again in a new one. The command
reaches the crew's discussion gateway through a port-forward to a ready pod behind its
Service, through the kubelet as `kubectl port-forward` does, using your kubeconfig
credentials. It needs no extra networking setup and works under a default-deny network
policy.

**Fitness suite execution.** `kmctl fitness run <suite>` (`fitness` has the alias `fit`) polls an
existing `CrewFitnessSuite` until every iteration has run, printing progress as it
goes; `-f FILE` applies a suite manifest first. Quality scores land after the
deferred judge pass, and the exit code reflects timeouts and API errors only. `kmctl fitness list` and `kmctl fitness get <suite>` inspect
suite status. `kmctl fitness scenarios <suite>` lists the scenarios in a suite. `kmctl fitness pause`,
`resume` and `stop` control a suite while it runs.
Pass `--scenario` to run a single scenario in isolation as a quick smoke test.
`kmctl fitness download <suite>` retrieves the XLSX artifact for a completed suite
through a port-forward to a ready dashboard pod; use `-o FILE` to control the
output path.

**Crew memory.** `kmctl memory export <crew>` writes the crew's long-term memory
in the Open Memory Protocol Initiative (OMPI) interchange format, read-only, so you can
take a crew's memory out with a command and no operator source. `kmctl memory add`,
`edit`, `delete` and `clear` change the facts a crew keeps.

**MCP server reports.** `kmctl mcp report list` and `get` show the verdict Kubemoot
computed for each MCP server. `kmctl mcp report pin REPORT --verdict use|caution|avoid`
pins a verdict of your own, and `unpin` hands it back to the computed one.

**Shell completion.** `kmctl completion bash|zsh|fish|powershell` generates
completion scripts for all major shells. Resource names complete in `get` commands.

## Still planned

`kmctl conversation get`, to read a past turn's answer, is planned but not yet
shipped. A per-scenario score breakdown in
`kmctl fitness get` is also planned. To browse conversation history,
use the [Kubemoot dashboard](../../operating/dashboard/).

## Install

See the [kmctl User Guide](../../user-guides/kmctl/) for install steps, a
runnable quickstart, and shell-completion setup. For the full command reference and
flag details, see [kmctl Reference](../../reference/kmctl/).
