---
title: "kmctl User Guide"
weight: 50
description: "Install kmctl, scaffold and deploy your first crew, and set up shell completion."
---

`kmctl` is the command-line tool for Kubemoot. This guide covers installation, a
runnable quickstart that walks through the core workflow, and shell-completion setup.

For the complete command reference, see [kmctl Reference](../../reference/kmctl/).
For an overview of `kmctl`'s role in the ecosystem, see
[kmctl - the CLI](../../ecosystem/kmctl/).

---

## Command structure

`kmctl` uses a hybrid noun-verb layout, chosen deliberately as the best fit for a
domain CLI with deeply-nested resources and actions that have no clean kubectl
equivalent.

Resource commands are noun-first and scoped: `kmctl crew list`, `kmctl crew get`,
`kmctl fitness run`, `kmctl conversation ask`. This means `kmctl <noun> --help`
shows a clean, focused list of what you can do to that resource, and shell
completion scales as the surface grows.

Cross-cutting actions are global verbs that parallel kubectl conventions:
`kmctl create`, `kmctl apply -f`, `kmctl delete`, `kmctl status`, `kmctl version`.
This is intentional: kubectl muscle memory applies exactly where it helps (applying
and deleting manifests), while resource-specific actions such as `ask`, `run`, and
`scenarios` live where they are discoverable.

This hybrid layout follows the same convention as `istioctl`, `helm`, and Knative's
`kn`, which is the consensus pattern for developer-facing tools that combine manifest
management with domain-specific commands. A flat verb-first model (like `kubectl get
<type>`) would not house these domain actions cleanly and would fracture
discoverability as the surface grows.

---

## Install

### Binary releases

Pre-built static binaries for Linux, macOS, and Windows (amd64 and arm64) are
published to GitHub releases with every tagged version. Use the GitHub CLI to download:

```bash
gh release download \
  -R kubemoot/kmctl \
  --pattern 'kmctl_*_linux_amd64.tar.gz'
tar -xzf kmctl_*_linux_amd64.tar.gz
sudo install -m0755 kmctl /usr/local/bin/kmctl
kmctl version
```

Substitute `linux_amd64` for your platform (`darwin_amd64`, `darwin_arm64`,
`linux_arm64`, `windows_amd64`). Linux and macOS archives are `.tar.gz`; Windows archives are `.zip`.

With Go installed, `go install github.com/kubemoot/kmctl@latest` also works.

### Homebrew (planned)

```bash
brew install kubemoot/tap/kmctl
```

**Status: planned.** The Homebrew tap ships in a later distribution phase.

### Krew (planned)

```bash
kubectl krew install kmctl
```

**Status: planned.** The krew manifest ships in a later distribution phase.

---

## Prerequisites

- A Kubernetes cluster with the Kubemoot operator installed and running.
- A kubeconfig with access to the cluster (standard `~/.kube/config` or `KUBECONFIG`
  environment variable).
- `kubectl` is not required by `kmctl` itself, but the same kubeconfig is used.

---

## Quickstart

This walkthrough covers the core workflow end to end: confirm the cluster is ready,
scaffold a crew, apply it, inspect the result, start a conversation, and run a
fitness suite.

### 1. Confirm the cluster is ready

```bash
kmctl status
```

Expected output:

```
Cluster:  reachable (v1.31.2)
Kubemoot: installed (kubemoot.ai/v1alpha1)
```

If `Kubemoot: not installed` appears, the operator is not yet deployed. See
[Installation](../../introduction/installation/).

Check which cluster and namespace `kmctl` is pointed at:

```bash
kmctl info
```

### 2. Scaffold a crew

```bash
kmctl create demo
```

`demo` is the crew's Kubernetes name: lowercase letters, digits, and hyphens, starting and
ending with a letter or digit, at most 36 characters. To give the crew a name people read,
add `--display-name "Demo Crew"`; it defaults to the Kubernetes name.

On a TTY, `kmctl create` runs interactively: it asks for the display name, discovers
available ollama providers, lets you select them with a checkbox, and prompts for a model family. Answer the
prompts and it writes a `demo/` directory:

```
demo/
  crew.yaml
  agents.yaml
  promptmodules.yaml
  models.yaml
  tools.yaml
  access/
    rbac.yaml
    fitness-scenarios.yaml
  fitness/
    fitness.yaml
  README.md
```

The result is the [starter crew](../starter-crew/): a read-only guide to the namespace it
is installed into. `--members` (1 to 5) sets how many specialists it has.

To skip interaction in CI or scripts, supply all inputs as flags:

```bash
kmctl create demo \
  --members 2 \
  --providers ollama-gpu \
  --model-family qwen \
  --no-input
```

To scaffold the same crew as a Helm chart instead (`Chart.yaml`, `templates/`, and a
`fitness/` folder kept outside `templates/` so installing it never starts a run on its
own), add `--chart`:

```bash
kmctl create demo --chart --members 2 --model-family qwen --no-input
```

A chart deploys with `helm upgrade --install demo demo --namespace demo --create-namespace`
instead of the `kubectl` and `kmctl apply` steps below; both forms scaffold the same manifests. This is the
form [CrewForge](../../ecosystem/crewforge/) scaffolds with **New Kubemoot Crew Here**,
and the layout its Crew Sources view expects a chart source to have; see
[Develop a crew in VS Code](../../ecosystem/crewforge/develop-a-crew/) for that workflow.

### 3. Review and customise

Open the generated directory. Key files:

- `crew.yaml` - the Crew resource; adjust the name and labels.
- `agents.yaml` - the coordinator and the specialist Agents; review each agent's capabilities.
- `promptmodules.yaml` - the PromptModules in ADL; this is the main place to shape the
  crew's discussion behaviour.
- `models.yaml` - the Models the crew's phases select from.
- `tools.yaml` - the read-only Kubernetes MCP server and the MCPGateway in front of it.
- `access/rbac.yaml` - the Role the tool server runs with; apply it first with
  `kubectl apply -n <namespace> -f access/`.
- `fitness/fitness.yaml` - a starter CrewFitnessSuite of scenarios that ground on the
  crew's own pods.

See [Build a Crew](../build-a-crew/) and [Write Agents and ADL](../write-agents-and-adl/)
for guidance on what to customise and how.

### 4. Apply to the cluster

`kmctl create` prints the command that deploys what it scaffolded. For the loose-manifest
form, with the default namespace `crew-demo`:

```bash
kubectl create namespace crew-demo && \
  kubectl apply -n crew-demo -f ./demo/access && \
  kmctl apply -n crew-demo -f ./demo
```

The `access/` folder holds the Role the tool server runs with, so it applies first, with
`kubectl`. `kmctl apply` uses server-side apply, accepts only Kubemoot CRD resources, and
reads the files directly in the directory it is given, not subdirectories. That is why
`access/` and `fitness/` are separate steps. Add `--dry-run` to preview what `kmctl apply`
would write.

### 5. Watch the crew come up

```bash
kmctl crew get demo -n crew-demo
kmctl agent list -n crew-demo
```

### 6. Ask the crew

```bash
kmctl conversation ask demo "List the pods in this namespace and their status." -n crew-demo
```

`kmctl conversation` reaches the crew through the Kubernetes API server's service proxy,
so no extra port-forwarding is required. Add `--quiet` to print only the final answer,
and `--conversation-id <id>` (the ID the first `ask` prints) to continue the same
conversation.

### 7. Run the fitness suite

```bash
kmctl fitness run -f ./demo/fitness/fitness.yaml -n crew-demo
kmctl fitness scenarios demo-starter -n crew-demo
kmctl fitness get demo-starter -n crew-demo
```

`kmctl fitness run` returns when every iteration has run (`phase=Completed`). Quality
scores land after the judge pass; `kmctl fitness get` shows them once they are in. Then
download the workbook:

```bash
kmctl fitness download demo-starter -n crew-demo
```

[The starter crew](../starter-crew/) walks through this flow end to end, including how to
change one rule and ask again.

---

## Shell completion

`kmctl completion` generates completion scripts for bash, zsh, fish, and PowerShell.

### bash

```bash
# Add to ~/.bashrc or ~/.bash_profile:
source <(kmctl completion bash)
```

To install system-wide for all users:

```bash
kmctl completion bash | sudo tee /etc/bash_completion.d/kmctl
```

### zsh

```zsh
# Add to ~/.zshrc:
source <(kmctl completion zsh)
```

If you use Oh My Zsh, save the completion file to the custom completions directory:

```zsh
kmctl completion zsh > "${fpath[1]}/_kmctl"
```

### fish

```fish
kmctl completion fish | source
# For persistence across sessions:
kmctl completion fish > ~/.config/fish/completions/kmctl.fish
```

### PowerShell

```powershell
# Add to $PROFILE:
kmctl completion powershell | Out-String | Invoke-Expression
```

---

## Common patterns

### Use a specific namespace

```bash
kmctl crew list -n my-namespace
kmctl agent list -n my-namespace
```

### Use a specific kubeconfig context

```bash
kmctl --context staging crew list
kmctl --context staging info
```

### Non-interactive scaffolding for CI

```bash
kmctl create my-crew \
  --members 3 \
  --providers ollama-gpu,ollama-gpu-b \
  --model-family qwen \
  --no-input \
  -o /workspace/crews
```

### Scaffold as a Helm chart

```bash
kmctl create my-crew --chart --display-name "My Crew" --members 2 --model-family qwen --no-input
helm upgrade --install my-crew my-crew --namespace my-crew --create-namespace
```

### Preview what apply would do

```bash
kmctl apply -f my-crew/ --dry-run
```

### Inspect the GPU footprint

```bash
kmctl model footprint
```

### Read a PromptModule's ADL content

```bash
kmctl prompt get my-coordinator-prompt -o yaml
```

### Ask a crew a question

```bash
kmctl conversation ask homelab-pilot "what is the cluster status?" \
  -n crew-homelab-pilot
```

### Continue a conversation thread

```bash
kmctl conversation ask homelab-pilot "can you be more specific?" \
  -n crew-homelab-pilot \
  --conversation-id <id>
```

### Run a fitness suite and wait for completion

```bash
kmctl fitness run my-suite -n kubemoot
```

### Smoke-test a single scenario

```bash
kmctl fitness run my-suite --scenario smoke-hello -n kubemoot
```

### Download a fitness suite XLSX artifact

```bash
kmctl fitness download my-suite -n kubemoot
# Downloads to my-suite.xlsx

kmctl fitness download my-suite -o results.xlsx -n kubemoot
```

### List fitness suites across all namespaces

```bash
kmctl fitness list -A
```

### Inline help

Every command has a `--help` flag that mirrors this reference:

```bash
kmctl --help
kmctl crew --help
kmctl create --help
kmctl conversation --help
kmctl fitness --help
```

---

## Related pages

- [kmctl Reference](../../reference/kmctl/) - full command and flag reference
- [kmctl - the CLI](../../ecosystem/kmctl/) - overview and ecosystem role
- [Build a Crew](../build-a-crew/) - resource-level crew authoring
- [Define Fitness Functions](../define-fitness-functions/) - fitness suite authoring
- [Develop a crew in VS Code](../../ecosystem/crewforge/develop-a-crew/) - scaffolding,
  deploying, testing, and redeploying a `--chart` crew from CrewForge instead of the
  terminal
