---
title: "kmctl Reference"
weight: 80
description: "Command reference for the kmctl command-line tool."
aliases:
  - /docs/reference/cf-cli/
---

`kmctl` is the command-line tool for Kubemoot. It is a convenience layer over
Kubemoot's Kubernetes resources, consistent in flag and output style with `kubectl`,
`istioctl`, and `helm`. The command layout (noun-first resource commands + global
manifest verbs) is a deliberate design choice; see
[Command structure](../../user-guides/kmctl/#command-structure) in the user guide.

This reference covers the shipped command surface. Commands marked **planned** are
designed but not yet implemented; all others work today. Check your installed version
with `kmctl version`. Every command has runnable `--help` examples, and `get` commands
offer shell completion of resource names. Run `kmctl <command> --help` for the same
information inline.

## Global flags

These flags apply to every command:

| Flag | Short | Default | Description |
|---|---|---|---|
| `--kubeconfig` | | `~/.kube/config` | Path to the kubeconfig file |
| `--context` | | current context | Kubeconfig context to use |
| `--namespace` | `-n` | `default` | Kubernetes namespace |
| `--all-namespaces` | `-A` | false | List resources across all namespaces |
| `--output` | `-o` | `table` | Output format: `table`, `yaml`, `json` |
| `--cluster` | | | Kubeconfig cluster override |
| `--user` | | | Kubeconfig user override |
| `--as` | | | Username to impersonate |
| `--help` | `-h` | | Show help for the command |

---

## kmctl version

```
kmctl version [--short]
```

Print the `kmctl` client version. With `--short`, prints only the version string.

| Flag | Description |
|---|---|
| `--short` | Print version string only |

**Example:**

```bash
kmctl version
# kmctl version v<version> (linux/amd64)

kmctl version --short
# v<version>
```

---

## kmctl info

```
kmctl info [-o yaml|json]
```

Print the resolved kubeconfig context, namespace, server URL, and server version.
Useful for confirming which cluster and namespace `kmctl` is pointed at before
running commands.

**Example:**

```bash
kmctl info
# Context:    homelab
# Namespace:  kubemoot
# Server:     https://k8s.example.com:6443
# Version:    v1.31.2

kmctl info -o yaml
```

---

## kmctl status

```
kmctl status
```

Check cluster reachability and confirm that the Kubemoot API group
(`kubemoot.ai`) is installed. Exits non-zero if the cluster is unreachable or
Kubemoot is not registered.

**Example:**

```bash
kmctl status
# Cluster:  reachable (v1.31.2)
# Kubemoot: installed (kubemoot.ai/v1alpha1)
```

---

## kmctl create

```
kmctl create <name> [flags]
```

Scaffold a working crew directory. The output is a set of Kubemoot manifests ready
to review, customise, and apply with `kmctl apply -f`. Think of it like `helm create`:
the scaffold is a starting point, not a finished product.

On a TTY, `kmctl create` runs interactively: it asks for a member count, offers a
checkbox selection of discovered ollama providers, and lets you choose a model family
(with a custom-entry and skip option). Pass `--no-input` with explicit flags to make
it scriptable.

Output is written to `<output-dir>/<name>/` and includes:

- A `Crew` manifest and `CrewSchedulingPolicy`
- A coordinator `Agent` and N specialist `Agent` resources
- `PromptModule` resources in ADL for coordinator and specialists
- A starter `CrewFitnessSuite` with a health-check scenario
- A `README.md` with next-step instructions

With `--chart`, the same manifests are laid out as a Helm chart instead of loose YAML:

```
demo/
  Chart.yaml
  values.yaml
  templates/
    crew.yaml
    agents.yaml
    promptmodules.yaml
    models.yaml
  fitness/
    fitness.yaml
  README.md
```

The fitness suite is kept in `fitness/`, outside `templates/`, so `helm install` never
starts a run on its own; apply it yourself when you want one. This is the layout
[CrewForge](../../ecosystem/crewforge/) scaffolds when you use its **Create Crew**
command, and the layout its Crew Sources view expects a chart source to have.

| Flag | Short | Description |
|---|---|---|
| `--members N` | | Number of specialist agents (default: prompted interactively) |
| `--providers a,b` | | Comma-separated list of ollama provider names to target |
| `--model-family` | | Model family hint, e.g. `qwen` |
| `--no-input` | | Disable interactive prompts; all required inputs must come from flags |
| `--output-dir DIR` | `-o` | Directory to write scaffold output (default: `.`) |
| `--chart` | | Lay the crew out as a Helm chart (`Chart.yaml`, `templates/`, `fitness/`) instead of loose manifests |

**Example (interactive):**

```bash
kmctl create demo
# Discovering providers...
# Members [2]: 3
# Providers: [x] ollama-gpu  [ ] ollama-gpu-b
# Model family [qwen]: qwen
# Writing demo/ ...done
```

**Example (scripted):**

```bash
kmctl create demo \
  --members 3 \
  --providers ollama-gpu,ollama-gpu-b \
  --model-family qwen \
  --no-input \
  -o /tmp/crews
# Wrote /tmp/crews/demo/
```

**Example (as a Helm chart):**

```bash
kmctl create demo --chart --members 2 --model-family qwen --no-input
# Scaffolded crew "demo" (2 tooler(s)) in ./demo:
#   demo/Chart.yaml
#   demo/values.yaml
#   demo/templates/crew.yaml
#   demo/templates/agents.yaml
#   demo/templates/promptmodules.yaml
#   demo/templates/models.yaml
#   demo/fitness/fitness.yaml
#   demo/README.md
#
# Next: helm upgrade --install demo demo --namespace demo --create-namespace
```

---

## kmctl crew

```
kmctl crew <subcommand> [flags]
```

Inspect Crew resources.

### kmctl crew list

```
kmctl crew list [-n <namespace>] [-A] [-o <format>]
```

List all `Crew` resources. Default table columns: Name, Namespace, Ready, Phase,
Coordinator, Agents, Age.

**Example:**

```bash
kmctl crew list -A
# NAME   NAMESPACE  READY  PHASE    COORDINATOR           AGENTS  AGE
# demo   kubemoot   true   Running  demo-coordinator      3       2d
```

### kmctl crew get

```
kmctl crew get <name> [-n <namespace>] [-o <format>]
```

Show details of a single Crew. With `-o yaml`, prints the full CR including status.

**Example:**

```bash
kmctl crew get demo
kmctl crew get demo -o yaml
```

---

## kmctl agent

```
kmctl agent <subcommand> [flags]
```

Inspect Agent resources.

### kmctl agent list

```
kmctl agent list [-n <namespace>] [-A] [-o <format>]
```

List all `Agent` resources. Default table columns: Name, Namespace, Type, Ready,
Phase, Endpoint, Age.

**Example:**

```bash
kmctl agent list -n kubemoot
# NAME                  NAMESPACE  TYPE        READY  PHASE    ENDPOINT  AGE
# demo-coordinator      kubemoot   coordinator true   Running  ...       2d
# demo-specialist-0     kubemoot   specialist  true   Running  ...       2d
```

### kmctl agent get

```
kmctl agent get <name> [-n <namespace>] [-o <format>]
```

Show details of a single Agent, including scheduling status and model assignment.

**Example:**

```bash
kmctl agent get demo-specialist-0
kmctl agent get demo-specialist-0 -o yaml
```

---

## kmctl prompt

```
kmctl prompt <subcommand> [flags]
```

Inspect `PromptModule` resources.

### kmctl prompt list

```
kmctl prompt list [-n <namespace>] [-A] [-o <format>]
```

List all PromptModules. Default table columns: Name, Namespace, Order, Age.

**Example:**

```bash
kmctl prompt list -n kubemoot
```

### kmctl prompt get

```
kmctl prompt get <name> [-n <namespace>] [-o <format>]
```

Show a PromptModule. Use `-o yaml` to read the full ADL content of the module.

**Example:**

```bash
kmctl prompt get demo-coordinator-prompt -o yaml
```

---

## kmctl model

```
kmctl model <subcommand> [flags]
```

Inspect model provider resources and live GPU state.

### kmctl model list

```
kmctl model list [-o <format>]
```

List all `ModelProvider` resources with connection status and basic metadata.

**Example:**

```bash
kmctl model list
```

### kmctl model get

```
kmctl model get <name> [-o <format>]
```

Show details of a single model provider.

**Example:**

```bash
kmctl model get ollama-gpu -o yaml
```

### kmctl model footprint

```
kmctl model footprint [-o <format>]
```

Show the resident `Model` resources per provider. This is the live GPU footprint
view: which models are loaded, on which provider, and how much VRAM they occupy.

**Example:**

```bash
kmctl model footprint
# PROVIDER      MODEL            VRAM
# ollama-gpu    qwen3:32b        20.1 GiB
# ollama-gpu-b  qwen2.5:14b      8.7 GiB
```

---

## kmctl apply

```
kmctl apply -f <file|dir|-> [flags]
```

Apply Kubemoot CRD manifests using server-side apply. Accepts a file, a directory,
or `-` for stdin. Accepts only kubemoot.ai resources (Crew, Agent, PromptModule,
Model, ModelProvider, MCPServer, MCPGateway, RAGSource, KubemootConfig,
CrewSchedulingPolicy, CrewFitnessSuite). Explicitly refuses non-kubemoot.ai kinds
and directs you to `kubectl apply` for those.

| Flag | Description |
|---|---|
| `-f` | File, directory, or `-` to read from stdin (required) |
| `--dry-run` | Validate and print what would be applied without writing to the cluster |
| `--force` | Force apply even if field manager conflicts exist |

**Examples:**

```bash
kmctl apply -f demo/
kmctl apply -f demo/ --dry-run
kmctl apply -f - < crew.yaml
```

---

## kmctl delete

```
kmctl delete <kind> <name> [-n <namespace>]
kmctl delete -f <file|dir>
```

Delete a Kubemoot CRD resource. Accepts only kubemoot.ai kinds. The operator's
finalizers handle cascading cleanup; `kmctl delete` does not implement its own
lifecycle logic.

**Examples:**

```bash
kmctl delete crew demo -n kubemoot
kmctl delete -f demo/
```

---

## kmctl completion

```
kmctl completion <shell>
```

Generate shell completion scripts. Supported shells: `bash`, `zsh`, `fish`,
`powershell`.

**Examples:**

```bash
# bash - add to ~/.bashrc
source <(kmctl completion bash)

# zsh - add to ~/.zshrc
source <(kmctl completion zsh)

# fish
kmctl completion fish > ~/.config/fish/completions/kmctl.fish

# PowerShell - add to $PROFILE
kmctl completion powershell | Out-String | Invoke-Expression
```

See the [kmctl User Guide](../../user-guides/kmctl/#shell-completion) for
full setup instructions per shell.

---

## kmctl conversation

Alias: `conv`

```
kmctl conversation ask <crew> "<message>" [-n <namespace>] [--quiet] [--conversation-id <id>]
kmctl conversation watch <crew> <conversation-id> [-n <namespace>]
```

Start a new conversation turn with a crew and stream the moot's deliberation in
real time. `kmctl conversation` reaches the crew's discussion gateway through the
Kubernetes API server's service proxy using your kubeconfig credentials; no extra
port-forwarding or networking setup is required.

### kmctl conversation ask

```
kmctl conversation ask <crew> "<message>" [-n <namespace>] [--quiet] [--conversation-id <id>]
```

Starts a turn and streams discussion signals (agent evaluations, agreements, concerns)
followed by the coordinator's synthesis. When the turn completes, exits 0.

| Flag | Description |
|---|---|
| `-n` | Namespace of the crew (required when the crew is not in the default namespace) |
| `--quiet` | Suppress streaming signals; print only the final coordinator answer |
| `--conversation-id` | Continue an existing conversation thread instead of starting a new one |

**Example:**

```bash
kmctl conversation ask homelab-pilot "what is failing?" -n crew-homelab-pilot
```

Sample output (streaming):

```
[evaluating] homelab-pilot-k8s-specialist
[evaluating] homelab-pilot-network-specialist
[agree]      homelab-pilot-k8s-specialist   "Three pods in CrashLoopBackOff..."
[agree]      homelab-pilot-network-specialist "DNS resolution failing for..."
[synthesis]  Two issues detected: (1) Pod homelab-pilot-backend is crash-looping
             due to a missing ConfigMap. (2) CoreDNS is returning SERVFAIL for
             in-cluster names in the homelab-pilot namespace.
```

With `--quiet`:

```bash
kmctl conversation ask homelab-pilot "what is failing?" -n crew-homelab-pilot --quiet
# Two issues detected: (1) Pod homelab-pilot-backend is crash-looping...
```

Continue an existing conversation:

```bash
kmctl conversation ask homelab-pilot "which ConfigMap is missing?" \
  -n crew-homelab-pilot \
  --conversation-id 7f3a1b9c
```

### kmctl conversation watch

```
kmctl conversation watch <crew> <conversation-id> [-n <namespace>]
```

Live-tail an in-flight conversation thread. Prints each signal as it arrives and
exits when the thread closes. Useful for watching a conversation that was started
by another process (the dashboard, a CI job, or another terminal session).

| Flag | Description |
|---|---|
| `-n` | Namespace of the crew |

**Example:**

```bash
kmctl conversation watch homelab-pilot 7f3a1b9c -n crew-homelab-pilot
```

### Still planned in kmctl conversation

`kmctl conversation list` and `kmctl conversation get` are planned but not yet
shipped. There is no gateway history endpoint yet. To browse conversation history,
use the [Kubemoot dashboard](../../operating/dashboard/).

---

## kmctl fitness

Alias: `fit`

```
kmctl fitness list [-n <namespace>] [-A]
kmctl fitness get <suite> [-n <namespace>]
kmctl fitness scenarios <suite> [-n <namespace>]
kmctl fitness run <suite> [-n <namespace>] [--scenario <name>] [--timeout <duration>]
kmctl fitness download <suite> [-n <namespace>] [-o FILE]
```

Inspect and run `CrewFitnessSuite` resources. `kmctl fitness run` polls to
`phase=Completed`, which the operator sets only after the deferred judge pass
completes. That phase is the single completion gate.

### kmctl fitness list

```
kmctl fitness list [-n <namespace>] [-A]
```

List all fitness suites. Default table columns: Name, Namespace, Phase, Done,
Total, Passed, Failed, Age.

**Example:**

```bash
kmctl fitness list -A
# NAME           NAMESPACE   PHASE      DONE  TOTAL  PASSED  FAILED  AGE
# demo-starter   kubemoot    Completed  10    10     9       1       3h
# nightly        kubemoot    Running    4     20     4       0       12m
```

### kmctl fitness get

```
kmctl fitness get <suite> [-n <namespace>] [-o <format>]
```

Show a suite's status summary. Includes phase, pass/fail counts, and any error
message. Use `-o yaml` for the full CR.

**Example:**

```bash
kmctl fitness get demo-starter
kmctl fitness get demo-starter -o yaml
```

### kmctl fitness scenarios

```
kmctl fitness scenarios <suite> [-n <namespace>]
```

List the scenarios (testRefs) defined in a suite, showing each scenario name and
its current result if the suite has run.

**Example:**

```bash
kmctl fitness scenarios demo-starter
# SCENARIO          RESULT
# smoke-hello       passed
# concept-routing   passed
# gotcha-dns        failed
```

### kmctl fitness run

```
kmctl fitness run <suite> [-n <namespace>] [--scenario <name>] [--timeout <duration>]
```

Run a suite and wait for completion. Polls progress and prints it as the run
proceeds. Exits 0 when the run passes, non-zero on failure or timeout.

To run a single scenario in isolation, pass `--scenario`. This creates a single
`CrewFitness` for that scenario rather than running the full suite.

| Flag | Description |
|---|---|
| `--scenario` | Run only this named scenario as a single CrewFitness |
| `--timeout` | How long to wait for completion (default: 30m) |

**Example (full suite):**

```bash
kmctl fitness run demo-starter -n kubemoot
# Running demo-starter (10 scenarios)...
# [1/10] smoke-hello        passed (42s)
# [2/10] concept-routing    passed (1m 3s)
# ...
# Result: 9 passed, 1 failed
```

**Example (single scenario smoke test):**

```bash
kmctl fitness run demo-starter --scenario smoke-hello -n kubemoot
# Running smoke-hello...
# Result: passed (38s)
```

### kmctl fitness download

```
kmctl fitness download <suite> [-n <namespace>] [-o FILE] [--dashboard-namespace NS] [--dashboard-service SVC]
```

Download the XLSX artifact for a completed suite. The command fetches the artifact
from the dashboard's artifact endpoint through the Kubernetes API server's service
proxy, using your kubeconfig credentials. No extra port-forwarding is required.

The suite must have reached `phase=Completed` before the artifact is available.

| Flag | Short | Default | Description |
|---|---|---|---|
| `-o FILE` | `-o` | `<suite>.xlsx` | File path to write the downloaded XLSX |
| `--dashboard-namespace` | | `kubemoot` | Namespace where the Kubemoot dashboard is running |
| `--dashboard-service` | | `kubemoot-dashboard` | Service name of the Kubemoot dashboard |

**Example:**

```bash
kmctl fitness download demo-starter -n kubemoot
# Downloads to demo-starter.xlsx

kmctl fitness download demo-starter -o results.xlsx -n kubemoot
# Downloads to results.xlsx
```

### Still planned in kmctl fitness

`kmctl fitness run -f FILE` (run from a manifest file without a pre-existing suite)
is planned but not yet shipped. A per-scenario score breakdown in `kmctl fitness get`
is also planned.

---

## Related pages

- [kmctl - the CLI](../../ecosystem/kmctl/) - overview and role in the ecosystem
- [kmctl User Guide](../../user-guides/kmctl/) - install, quickstart, and shell completion
- [Build a Crew](../../user-guides/build-a-crew/) - the resource-level workflow `kmctl` streamlines
- [CrewForge](../../ecosystem/crewforge/) - the VS Code extension that runs `kmctl create --chart` for its Create Crew command
