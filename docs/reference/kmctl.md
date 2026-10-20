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
| `--namespace` | `-n` | kubeconfig context namespace, else `default` | Kubernetes namespace |
| `--cluster` | | | Kubeconfig cluster override |
| `--user` | | | Kubeconfig user override |
| `--as` | | | Username to impersonate |
| `--help` | `-h` | | Show help for the command |

`-A` (`--all-namespaces`) and `-o` (`--output`) are not global. They belong to the commands that list or print resources, and their meaning varies by command (for example, `-o` is a directory for `kmctl create` and a file for `kmctl fitness download`).

## Output formats

Commands that list or print resources take `-o`. The default is a table. `-o yaml` and `-o json` print the full object. `-o kyaml` prints KYAML, the strict YAML subset that `kubectl get -o kyaml` prints: every string is quoted, maps use `{ }`, and lists use `[ ]`. KYAML is valid YAML, so any YAML parser reads it, and it removes the ambiguities of unquoted values such as `no` or `on` and of indentation.

```bash
kmctl crew get demo -o kyaml
```

`-o kyaml` works wherever a command accepts `-o yaml`. YAML stays the default.

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
kmctl info [-o yaml|json|kyaml]
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
to review, customise, and apply with `kmctl apply -f`. Think of it like `helm create`'s
nginx chart: a small, complete example that works on a fresh install, to change into your
own crew.

The scaffolded crew is the **starter crew**: a read-only guide to the Kubernetes namespace
it is installed into. Ask it what is running, what is wrong, and why; it reads the
namespace with real tools and answers from what it found. It never changes anything. See
[The starter crew](../../user-guides/starter-crew/) for what it contains and a walkthrough.

`<name>` is the crew's technical name and becomes the Kubernetes name of its objects. It
must be a DNS-1123 label: lowercase letters, digits, and hyphens, starting and ending with a
letter or digit, at most 36 characters. The operator names objects after the crew, and the
longest is the tool index Service `<crew>-kubernetes-mcp-tools-query`, which must fit a
63-character DNS label (and the name stays under Helm's 53-character release-name limit).
`--display-name` sets the name people read.

On a TTY, `kmctl create` runs interactively: it asks for the display name first (default:
`<name>`), then a member count, offers a
checkbox selection of discovered ollama servers, and lets you choose a model family
(with a custom-entry and skip option). Pass `--no-input` with explicit flags to make
it scriptable.

Output is written to `<output>/<name>/` and includes:

- A `Crew` manifest, a `CrewSchedulingPolicy`, and the `Model` resources the crew selects from
- A coordinator `Agent` and N specialist `Agent` resources (see `--members`)
- `PromptModule` resources in ADL for the coordinator and each specialist
- One Kubernetes `MCPServer` in read-only mode and the `MCPGateway` the agents reach it through
- A namespaced `Role` and `RoleBinding` that allow get, list, and watch (see **Access** below)
- A starter `CrewFitnessSuite` of 3 to 7 scenarios, all of which pass on a fresh install
- A ConfigMap `<name>-fitness` holding the scenario files, so the deployed crew carries its
  scenarios (see **Fitness scenarios** below)
- A `README.md` with a "first five minutes" guide, titled with the display name and noting
  "Its Kubernetes name is `<name>`"

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
    fitness-scenarios.yaml
  fitness/
    fitness.yaml
  README.md
```

The fitness suite is kept in `fitness/`, outside `templates/`, so `helm install` never
starts a run on its own; apply it yourself when you want one. This is the layout
[CrewForge](../../ecosystem/crewforge/) scaffolds when you use **New Kubemoot Crew Here**,
and the layout its Crew Sources view expects a chart source to have.

| Flag | Short | Description |
|---|---|---|
| `--display-name "..."` | | The name people read: any one line of text, no line breaks, tabs, or control characters, at most 100 characters (default: `<name>`). Stored as the `kubemoot.ai/display-name` annotation |
| `--members N` | | Specialists beside the coordinator, 1 to 5 (default: prompted interactively). Size 1 is `workloads`; each larger size adds the next of `events`, `networking`, `config`, and `reviewer` |
| `--servers a,b` | | Comma-separated list of ollama server names to target |
| `--model-family` | | Model family hint, e.g. `qwen` |
| `--no-input` | | Never prompt; inputs not given as flags take their defaults |
| `--output DIR` | `-o` | Directory to write scaffold output (default: `.`) |
| `--context` | | Kubeconfig context used to discover model servers (a global `kmctl` flag) |
| `--chart` | | Lay the crew out as a Helm chart (`Chart.yaml`, `templates/`, `fitness/`) instead of loose manifests |

**Display name.** `--display-name` is stored as the annotation `kubemoot.ai/display-name` on
the `Crew` (in `crew.yaml`, or `templates/crew.yaml` for a chart) and, with `--chart`, in the
`annotations` of `Chart.yaml`. The `README.md` title is the display name. Text containing
`{{` is written safely for Helm: it is rendered verbatim and never executed. Tools such as
[CrewForge](../../ecosystem/crewforge/) show the display name and fall back to the
Kubernetes name when the annotation is absent.

**Fitness scenarios.** The scaffold deploys the crew's scenarios as a ConfigMap, so a
running crew carries them. In chart mode, `templates/fitness-scenarios.yaml` builds the
ConfigMap `<name>-fitness` from the files in `fitness/` (`.Files.Glob "fitness/*"`), labeled
`kubemoot.ai/crew: <name>` and `kubemoot.ai/fitness-kind: scenarios`. In bundle mode, the
same ConfigMap is written to `access/fitness-scenarios.yaml`; apply it with
`kubectl apply -n <ns> -f access/` beside the RBAC, because `kmctl apply` takes only
`kubemoot.ai` kinds. A ConfigMap starts nothing: installing the crew still never starts a
run. CrewForge lists and runs a live crew's scenarios from this ConfigMap.

**Scope.** The `synthesis-prompt` module has a `scope` component switched by the chart value
`access.clusterWide`. By default (`false`) the crew reads only its own namespace; answers
name it ("in namespace X") and never describe "your cluster". When `true`, it reads the
whole cluster read-only without Secrets and names the namespace of each resource. Asked
about other namespaces or the whole cluster, a namespace-scoped crew says its access is
limited to its namespace, that setting `access.clusterWide: true` in `values.yaml` and
redeploying widens it, and answers only what its namespace shows. For a bundle, scaffold as
a chart with `--chart` and set the value. The scaffolded `README.md` has a "What it can
read" section.

**Sizes.** `--members` counts specialists, so the crew has that many plus the coordinator:

| `--members` | Specialists | Fitness scenarios |
|---|---|---|
| 1 | `workloads` | 3 |
| 2 | `workloads`, `events` | 4 |
| 3 | `workloads`, `events`, `networking` | 5 |
| 4 | `workloads`, `events`, `networking`, `config` | 6 |
| 5 | the four above (Toolers) plus `reviewer` (Analyst) | 7 |

Each Tooler reads one slice of the namespace: `workloads` (Pods, Deployments, ReplicaSets,
StatefulSets, Jobs), `events` (Warning events, restarts, recent failures), `networking`
(Services, endpoints, Ingresses or HTTPRoutes, NetworkPolicies), and `config` (ConfigMaps,
ServiceAccounts, and the Secrets that pod specs reference). The `reviewer` checks the answer
against the data the Toolers gathered and flags unsupported claims.

**Access.** The tool server runs with `--read-only` and a namespaced `Role` that grants only
`get`, `list`, and `watch`. The Role has no Secret access: Kubernetes cannot grant a Secret's
name without its data, so the `config` specialist names Secrets from the references in pod
specs. Set `access.clusterWide: true` in the chart's `values.yaml` to use a `ClusterRole` and
let the crew read every namespace; it stays read-only and still has no Secret access.

**Limits.** The prompts carry the namespace name, which Helm fills in at install, so a
rendered bundle is tied to its namespace. The `reviewer` cannot see Tooler findings over 4 KB,
which the Toolers post as artifact pointers.

**Example (interactive):**

```bash
kmctl create demo
# Discovering servers...
# Display name [demo]: Demo Crew
# Members [2]: 3
# ModelServers: [x] ollama-gpu  [ ] ollama-gpu-b
# Model family [qwen]: qwen
# Writing demo/ ...done
```

**Example (scripted):**

```bash
kmctl create demo \
  --members 3 \
  --servers ollama-gpu,ollama-gpu-b \
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
#   demo/templates/fitness-scenarios.yaml
#   demo/fitness/fitness.yaml
#   demo/README.md
#
# Next: helm upgrade --install demo demo --namespace demo --create-namespace
```

**Example (with a display name):**

```bash
kmctl create homelab-health-guide --display-name "Homelab Health Guide"
# Scaffolded crew "Homelab Health Guide" (homelab-health-guide) ...
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

Inspect model server resources and live GPU state.

### kmctl model list

```
kmctl model list [-o <format>]
```

List all `ModelServer` resources with connection status and basic metadata.

**Example:**

```bash
kmctl model list
```

### kmctl model get

```
kmctl model get <name> [-o <format>]
```

Show details of a single model server.

**Example:**

```bash
kmctl model get ollama-gpu -o yaml
```

### kmctl model footprint

```
kmctl model footprint [-o <format>]
```

Show the resident `Model` resources per server. This is the live GPU footprint
view: which models are loaded, on which server, and how much VRAM they occupy.

**Example:**

```bash
kmctl model footprint
# SERVER      MODEL            VRAM
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
Model, ModelServer, MCPServer, MCPGateway, RAGSource, KubemootConfig,
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
kmctl conversation ask <crew> "<message>" [-n <namespace>] [-o full|plain|json|quiet] [-q] [--conversation-id <id>]
kmctl conversation watch <crew> <conversation-id> [-n <namespace>] [-o full|plain|json|quiet] [-q]
kmctl conversation list <crew> [-n <namespace>] [-o yaml|json|kyaml]
kmctl conversation artifact <crew> <key> [-n <namespace>] [-f <file>]
```

Start a conversation turn with a crew and stream the moot's deliberation in real
time. `kmctl conversation` reaches the crew's discussion gateway through the
Kubernetes API server's service proxy using your kubeconfig credentials; no extra
port-forwarding or networking setup is required.

### kmctl conversation ask

```
kmctl conversation ask <crew> "<message>" [-n <namespace>] [-o full|plain|json|quiet] [-q] [--conversation-id <id>]
```

Starts a turn and streams the discussion until the crew answers, then exits 0.

| Flag | Default | Description |
|---|---|---|
| `-n` | context namespace | Namespace of the crew |
| `-o`, `--output` | `full` | `full`, `plain`, `json`, or `quiet` (see [Output modes](#output-modes)) |
| `-q`, `--quiet` | | The same as `-o quiet`: print only the answer. `-q` with a different `-o` is an error |
| `--conversation-id` | | Continue an existing conversation: its ID or a unique prefix of it |
| `--idle-timeout` | `5m` | Stop waiting when the crew shows no activity for this long; `0` waits while the stream is open |
| `--timeout` | `0` | Overall cap on the wait, for scripts; `0` means no cap |

**Example:**

```bash
kmctl conversation ask homelab-pilot "Describe the proxmox nodes." -n crew-homelab-pilot -o plain
```

```
conversation 6733c361-aa0c-4bde-85c6-e143af23b251
[   2.0s] thread t-42
[   3.0s] subcommittee: pilot-metrics, pilot-proxmox, pilot-storage; brief: List each Proxmox node with its role...
[   8.0s] pilot-gpu              stood aside (qwen3:8b, 419 tokens): not in the subcommittee
[  10.0s] pilot-proxmox          agree (evaluation 10.4s, qwen3:14b, 5,850 tokens): The cluster has three Proxmox nodes, all online.
[  11.0s] pilot-metrics          agree (evaluation 12.0s, qwen3:14b, 8,230 tokens): attached artifact (8.3 KB); fetch command below
[  14.0s] done

ANSWER:
## Proxmox nodes
...

**How this answer was made**

- Total time (as received by kmctl): 14.0s; first signal after 8.0s
- Crew time (thread start to answer, as the crew published): 31.0s
- Subcommittee: 3 convened (pilot-metrics (node metrics), pilot-proxmox (proxmox inventory), pilot-storage)
- Agents: 4 took part (pilot-proxmox, pilot-metrics, pilot-gpu, pilot-storage); 1 stood aside (pilot-gpu)
- Signals: 2 agree, 1 failure, 1 stand_aside
- Tokens: 15,740 in, 2,059 out
- Models: qwen3:14b, qwen3:8b
- Tool errors: pilot-storage: zfs_pool_status failed 3 times: connection refused
- Artifact from pilot-metrics (8.3 KB): `kmctl conversation artifact homelab-pilot crew-homelab-pilot/homelab-pilot/t-42/pilot-metrics/agree-1b2c -n crew-homelab-pilot`

Next: kmctl conversation ask homelab-pilot "<next question>" -n crew-homelab-pilot --conversation-id 6733c361-aa0c-4bde-85c6-e143af23b251
```

Continue the conversation with a short ID prefix:

```bash
kmctl conversation ask homelab-pilot "and their memory?" -n crew-homelab-pilot --conversation-id 6733c
```

### Output modes

| Mode | What it shows |
|---|---|
| `full` (default) | A live view: one row per agent with a spinner while it works, its step, the accelerator and model it runs on, its step durations and tokens, and the subcommittee under the header. Each row settles to a colored signal. The answer is rendered as markdown. |
| `plain` | One line per event with the time since the turn began, no color or animation, for logs, CI, and screen readers. The answer is raw markdown. |
| `json` | Newline-delimited JSON: a `conversation` line, one line per gateway event (with `elapsedMs` and any step duration added), and a final `result` line with `answer`, `summary`, and `next`. Every line carries `schemaVersion` (1). |
| `quiet` | Only the answer. `-q` is the same. |

`full` falls back to `plain` when stdout is not a terminal, `NO_COLOR` is set, or
`TERM=dumb`.

Every mode except `quiet` ends with a summary right after the answer's own notes,
then the follow-up command. The summary has the total time and the time to the
first signal (as kmctl received them), the crew's own time from the thread's start
to the answer, the subcommittee the coordinator convened with each agent's reason
when given, the agents that took part and stood aside, signals by kind, tokens in
and out, accelerators, models, agents that waited for GPU capacity, tool errors,
and the command that fetches each attached artifact. Step durations come from the
timestamps the crew published (`summary.timeSource` is `published`); with a
discussion gateway that does not send timestamps they are measured on receipt
(`received`), and the fields that gateway does not send (tokens, models, the
subcommittee, tool errors) are left out rather than estimated. Retries are not
published by the crew, so the summary does not show them. The stream's events and
fields are described in
[Conversations, Turns, and Threads](../../concepts/conversations-turns-and-threads/#timestamps-and-signal-detail).

### How long kmctl waits

kmctl waits as long as the crew is working. While anyone on the crew is busy, the
gateway sends an `activity` event every 15 seconds: `plain` prints it as a `busy:`
line, `json` as a line of type `activity`, and `full` names who is busy in the
header. The only default clock is `--idle-timeout` (default `5m`), which runs only
while the crew shows no activity and starts again with every event from the crew.
`--timeout` (default `0`, no cap) is an overall limit for scripts. When either one
stops the wait, kmctl prints the conversation and thread IDs and the command that
reattaches, because the crew may still answer:

```
Error: the crew showed no activity for 5m0s; the discussion may still finish.
Conversation 6733c361-aa0c-4bde-85c6-e143af23b251, thread 8f0c2a1e
Reattach: kmctl conversation watch homelab-pilot 6733c361-aa0c-4bde-85c6-e143af23b251 -n crew-homelab-pilot
```

### kmctl conversation watch

```
kmctl conversation watch <crew> <conversation-id> [-n <namespace>] [-o full|plain|json|quiet] [-q]
```

Follow the discussion of a conversation's current turn until the crew answers, for
example one started by the dashboard, a CI job, or another terminal. The
conversation ID may be a unique prefix. The output modes and the wait flags
(`--idle-timeout`, `--timeout`) are the same as for `ask`.

```bash
kmctl conversation watch homelab-pilot 6733c -n crew-homelab-pilot
```

### kmctl conversation list

```
kmctl conversation list <crew> [-n <namespace>] [-o yaml|json|kyaml]
```

List the crew's recent conversations, newest first, with each one's first question.
The gateway reads them from the discussion stream, which keeps about a day.

```bash
kmctl conversation list homelab-pilot -n crew-homelab-pilot
# CONVERSATION ID                        LAST TURN              TURNS   FIRST QUESTION
# 6733c361-aa0c-4bde-85c6-e143af23b251   2026-10-15T09:12:40Z   2       Describe the proxmox nodes.
```

### Short conversation IDs and completion

Wherever kmctl takes a conversation ID, a unique prefix works, as with git hashes.
An ambiguous prefix fails and lists the matching conversations with their first
questions; a prefix that matches no recent conversation says so. A full ID is used
as given. Shell completion of `--conversation-id` and of `watch`'s conversation
argument offers the crew's recent conversations, each with its first question as
the description. A discussion gateway that cannot list conversations needs the full
ID, and kmctl says so.

### kmctl conversation artifact

```
kmctl conversation artifact <crew> <key> [-n <namespace>] [-f <file>]
```

Write an artifact an agent attached to a discussion (a contribution too large for
the stream) to stdout, or to a file with `-f`. The conversation output prints this
command for each artifact. The coordinator deletes a thread's artifacts soon after
the thread closes; after that kmctl reports the artifact as not found.

### Still planned in kmctl conversation

`kmctl conversation get`, to read a past turn's answer, is planned but not yet
shipped. To browse conversation history, use the
[Kubemoot dashboard](../../operating/dashboard/).

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
`phase=Completed`, which the operator sets when every iteration has run. Phase
reflects execution, not test outcome: a suite whose iterations failed their
assertions still reaches `Completed`. The deferred judge pass runs after that;
`status.judge` reports its progress and scores, and `kmctl fitness get` shows them.

### kmctl fitness list

```
kmctl fitness list [-n <namespace>] [-A]
```

List all fitness suites. Default table columns: Name, Namespace, Phase, Done,
Total, Passed, Failed, Judge (the judge phase), Quality (the mean judge score,
0 to 100), Age.

**Example:**

```bash
kmctl fitness list -A
# NAMESPACE   NAME           PHASE      DONE  TOTAL  PASSED  FAILED  JUDGE     QUALITY  AGE
# kubemoot    demo-starter   Completed  10    10     9       1       Complete  82       3h
# kubemoot    nightly        Running    4     20     4       <none>  Pending   <none>   12m
```

### kmctl fitness get

```
kmctl fitness get <suite> [-n <namespace>] [-o <format>]
```

Show a suite's results, read from its status through the Kubernetes API: the
summary row, the judge state, and one row per scenario with its iteration
outcomes, mean duration, judge score, and the judge's reason (one line, up to
200 characters). Use `-o yaml` for the full object. See
[Results in status](../crewfitnesssuite/#results-in-status) for the fields.

**Example:**

```bash
kmctl fitness get demo-starter
# NAME           PHASE      DONE  TOTAL  PASSED  FAILED  JUDGE     QUALITY  AGE
# demo-starter   Completed  10    10     9       1       Complete  82       3h
#
# Judge: Complete, 2 of 2 scenarios judged, mean 82, completed 2026-10-02T14:31:07Z
#
# SCENARIO      ITERATIONS  PASSED  FAILED  ERRORED  MEAN DURATION  SCORE  REASON
# smoke-hello   5           5       0       0        42s            90     Names both nodes and their roles.
# gotcha-dns    5           4       1       0        1m3s           74     Correct resolver; omits the search domain.

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
proceeds. It returns once the iterations have finished (or the suite is
cancelled) and exits 0 whatever their outcome, which `kmctl fitness get` shows; it
exits non-zero on a timeout or an API error. When the judge has
not finished scoring the run by then, it says so, and `kmctl fitness get` shows the
judge's progress and scores.

Until the judge pass finishes, `kmctl fitness download` serves a provisional
workbook whose quality scores are 0; once judging completes, the operator rewrites
the workbook with the final scores. An iteration Job that fails is retried twice
before the iteration is recorded as failed.

To run a single scenario in isolation, pass `--scenario`. This creates a single
`CrewFitness` for that scenario rather than running the full suite.

| Flag | Description |
|---|---|
| `--scenario` | Run only this named scenario as a single CrewFitness |
| `-f`, `--filename` | Apply a CrewFitnessSuite manifest, then run it |
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

The suite must have reached `phase=Completed` before the artifact is available. A workbook downloaded before the judge pass finishes is provisional and carries quality 0.

| Flag | Short | Default | Description |
|---|---|---|---|
| `-o FILE` | `-o` | `<suite>.xlsx` | File path to write the downloaded XLSX |
| `--dashboard-namespace` | | `kubemoot` | Namespace where the Kubemoot dashboard is running |
| `--dashboard-service` | | empty | Service name of the Kubemoot dashboard; when empty, the Service is discovered by label in `--dashboard-namespace` |

**Example:**

```bash
kmctl fitness download demo-starter -n kubemoot
# Downloads to demo-starter.xlsx

kmctl fitness download demo-starter -o results.xlsx -n kubemoot
# Downloads to results.xlsx
```

### Re-judging a run

`kmctl` has no re-judge command. A `CrewFitnessSuite` with a `spec.rejudge` block scores an
earlier run's saved answers against the suite's current scenarios without asking the crew;
apply it with `kmctl fitness run -f FILE` or `kubectl apply`, then use `kmctl fitness get` and
`kmctl fitness download` as for any suite. See
[Re-judge an earlier run](../crewfitnesssuite/#re-judge-an-earlier-run).

### Still planned in kmctl fitness

`kmctl fitness download` still fetches the XLSX through the dashboard. Fetching it
through an operator-owned API instead is planned.

---

## Related pages

- [kmctl - the CLI](../../ecosystem/kmctl/) - overview and role in the ecosystem
- [kmctl User Guide](../../user-guides/kmctl/) - install, quickstart, and shell completion
- [Build a Crew](../../user-guides/build-a-crew/) - the resource-level workflow `kmctl` streamlines
- [CrewForge](../../ecosystem/crewforge/) - the VS Code extension that runs `kmctl create --chart` for its Create Crew command
