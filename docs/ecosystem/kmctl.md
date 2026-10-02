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

The shipped command surface covers seven areas:

**Cluster and operator inspection.** `kmctl status` checks that the cluster is
reachable and that the Kubemoot API group is registered. `kmctl info` reports the
resolved context, namespace, server URL, and server version. `kmctl version` prints
the client version.

**Crew and agent inspection.** `kmctl crew list`, `kmctl crew get <name>`,
`kmctl agent list`, and `kmctl agent get <name>` give a typed, readable view of what
is running, equivalent to `kubectl get` but without needing to know the CRD group and
version. Columns: Ready, Phase, Coordinator, Agents, Age (crew); Type, Ready, Phase,
Endpoint, Age (agent).

**Crew scaffolding.** `kmctl create <name>` scaffolds a complete, working crew: a
Crew manifest, a coordinator and 1 to 5 specialist Agents with PromptModules in ADL, a
read-only Kubernetes MCP server behind an MCPGateway, a CrewSchedulingPolicy, a starter
CrewFitnessSuite that passes on a fresh install, and a README. The crew is a read-only
guide to its own namespace; see [The starter crew](../../user-guides/starter-crew/). On a TTY it runs
interactively, discovering available ollama providers and letting you select them.
Pass `--no-input` with explicit flags to make it scriptable. Think of it like
`helm create`: the output is a starting point to customise, not a finished product.

**Prompt and model inspection.** `kmctl prompt list` and `kmctl prompt get <name>`
surface PromptModule resources. Use `-o yaml` to read the full ADL content.
`kmctl model list` and `kmctl model get <name>` show model provider resources.
`kmctl model footprint` shows the resident Model resources per provider, giving a
live view of the GPU footprint.

**Manifest management.** `kmctl apply -f` and `kmctl delete` are CRD-only
server-side-apply wrappers. They accept Crew, Agent, PromptModule, and the rest of
the Kubemoot API, and explicitly refuse non-kubemoot.ai resources, directing you to
`kubectl apply` for those. The operator's finalizers handle cascading cleanup on
delete.

**Live conversation.** `kmctl conversation ask <crew> "<message>"` (alias `conv`)
starts a turn and streams the moot's deliberation in real time, including each
specialist's evaluation and the coordinator's synthesis. Pass `--quiet` to print only
the final answer. Pass `--conversation-id` to continue an existing thread.
`kmctl conversation watch` live-tails an in-flight conversation by ID. The command
reaches the crew through the Kubernetes API server's service proxy using your
kubeconfig credentials; no extra networking setup is required.

**Fitness suite execution.** `kmctl fitness run <suite>` (`fitness` has the alias `fit`) polls an
existing `CrewFitnessSuite` until every iteration has run, printing progress as it
goes; `-f FILE` applies a suite manifest first. Quality scores land after the
deferred judge pass, and the exit code reflects timeouts and API errors only. `kmctl fitness list` and `kmctl fitness get <suite>` inspect
suite status. `kmctl fitness scenarios <suite>` lists the scenarios in a suite.
Pass `--scenario` to run a single scenario in isolation as a quick smoke test.
`kmctl fitness download <suite>` retrieves the XLSX artifact for a completed suite
through the Kubernetes API server's service proxy; use `-o FILE` to control the
output path.

**Shell completion.** `kmctl completion bash|zsh|fish|powershell` generates
completion scripts for all major shells. Resource names complete in `get` commands.

## Still planned

`kmctl conversation list` and `kmctl conversation get` are planned but not yet
shipped (no gateway history endpoint exists yet). A per-scenario score breakdown in
`kmctl fitness get` is also planned. To browse conversation history,
use the [Kubemoot dashboard](../../operating/dashboard/).

## Install

See the [kmctl User Guide](../../user-guides/kmctl/) for install steps, a
runnable quickstart, and shell-completion setup. For the full command reference and
flag details, see [kmctl Reference](../../reference/kmctl/).
