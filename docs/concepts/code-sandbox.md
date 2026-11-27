---
title: "Code sandbox"
weight: 58
description: "How a crew gets exact counts, sums, and sorts by running short programs in the code sandbox, how each run is contained, and how to run its pod under a sandboxed runtime such as gVisor."
---

Language models are unreliable at counting, summing, sorting, and filtering, especially
small local models reading a long listing. Asked how many pods are running in each
namespace, a model gives a confident, plausible, and often wrong number. Kubemoot does not
ask the model to do that work. It gives the work to a program.

The **code sandbox** is an MCP server that runs short Python or bash programs for the
compute agent and returns what they print. The models decide what to compute. The sandbox
makes the numbers real.

## Why it exists

In a crew, arithmetic goes to the compute agent. Another agent gathers the raw data. The
compute agent writes a short program that reads it and computes the answer. The sandbox runs
the program and returns the exact output, the compute agent reports that result, and the
coordinator uses it.

This is the compute contract, and the runtime enforces it: the compute agent may not state a
number that did not come from running code. A count that the model read off a list is not an
answer. A count a program printed is.

## How data reaches it

The container that runs programs has no network path to the places data lives, so the data
comes to it:

1. An agent fetches the data from the live system, and each tool result is stored as its own file in the crew's
   [workspace](../workspace/), announcing it on the bus.
2. The workspace-access materializer, a sidecar in the sandbox pod, reads the referenced
   objects from the store over the message bus and writes them to a shared volume under
   `/kubemoot/workspace`.
3. The compute agent's program reads those files and computes over them locally.

The materializer is the only part of the pod that talks to the bus. The sandbox container
that runs programs holds no bus credentials and talks to nothing itself. Data goes in the
program's code or in a file, never through stdin.

### Reading a stored result in full

`execute_code` takes an optional `inputs` list of keys of the discussion's stored results, in the short form
the discussion's list of stored results shows, `{agent}/{nnn}-{tool}`:

```json
{"language": "python", "code": "...", "inputs": ["k8s-config/001-resources_get"]}
```

Each key becomes the file `./inputs/{agent}/{nnn}-{tool}` in the program's directory, a copy of the whole stored
result. A program counts over all of it, so a count over a stored CSV that sits inside a YAML text field
is the true count and not a count over the rows an agent had pasted into the code. The program parses the
format itself: the standard library reads CSV, JSON and, line by line, YAML text.

The keys resolve inside the discussion the call belongs to. The agent runtime names that discussion in the
call's `_meta` (`workspacePrefix`, the same entry the workspace read tools use), and the key never carries it.
A key of another discussion, a key that does not exist, a pattern, and any key on a call that is not
part of a discussion are refused before the program runs, with an error that says which.

### What a program can see of the workspace

The workspace volume holds every discussion's results, and a program runs as the same user as the server
that reads them, so file permissions cannot separate them. Each program is instead started in its own user and
mount namespace:

- `/kubemoot/workspace` (and the old `/artifacts` mount) is replaced by the calling discussion's own folder,
  mounted read-only, so a program cannot plant a link or change a stored result. A call outside any discussion
  sees an empty directory.
- The temporary directory that holds the run is replaced by a private one that holds only this run's directory,
  so programs of concurrent calls cannot read each other's inputs or code.
- The program holds no capabilities, so it cannot undo any mount, and the server's own processes (their
  environment, file descriptors and working directory) are out of its reach.
- The program runs in its own pid namespace with a fresh `/proc`, so the server and other runs are not listed.
- The server opens an input once, each path component relative to the one before it and never through a
  link, so the file it checked is the file it copies; it takes regular files only and caps one input at 64 MiB
  and all inputs of a call at 256 MiB. A discussion folder that is a link shows the program an empty workspace.
- The private temporary directory is memory (`SANDBOX_TMP_SIZE`, default `256m`) and counts against the pod's
  memory limit while a run holds it, so size the pod for the concurrent runs. The run's own directory is on the
  pod's disk and is not size-capped; the pod's ephemeral-storage limit bounds it.

**This needs user namespaces and mounts.** A sandboxed runtime such as gVisor allows both (the combination
of a user namespace, a bind mount, a read-only remount and a private temporary directory is exercised on the
homelab's gVisor). A plain runtime with the default `RuntimeDefault` seccomp profile blocks user namespaces: there
every `execute_code` call fails, with an error that names the cause, until you set `SANDBOX_CONFINEMENT=off`.
With confinement off a program can read every discussion's files in the volume and every other run's files, so
there is no isolation between discussions: use it only for a pod whose workspace holds one discussion's data.

At startup only, the server refuses to start when a volume whose mount path is named `workspace` or
`artifacts` is mounted at a path it does not hide (volumes are found by that name, not by content). Name that path in `SANDBOX_WORKSPACE_DIRS` (comma separated) or set `WORKSPACE_DIR`.

## How a run is contained

The sandbox runs code a model wrote, so every run is locked down.

- **No credentials and no Kubernetes access.** The pod holds no secrets and no service
  account token. It only computes over data already handed to it. The `execute_code`
  description tells the model so: no cluster API and no kubectl, so a crew that needs live
  data selects the agents that fetch it.
- **Limited egress.** With [network policies](../network-policies/) on, the crew's policy
  limits the pod's outbound traffic to the message bus, which the materializer sidecar uses
  to stage artifacts. The pod has no route to external services.
- **A fresh temporary directory.** Each run starts in a new directory, which is also its
  `HOME`, and the directory is removed afterwards.
- **No stdin.** A read returns end of input at once, so a program that expects piped data
  fails fast instead of waiting.
- **Its own process group.** At the time limit the whole group is killed, and so is anything
  a finished program left running in the background.
- **Capped output.** Stdout and stderr are each capped, and the result says when output was
  cut.
- **A small image.** Python with the standard library only, bash, and an init process that
  reaps what a killed program leaves behind. The image keeps GNU `date` and the zone database:
  time-zone conversion and date arithmetic are done with `date` in the sandbox, for example
  `TZ=Asia/Tokyo date -d "2026-11-20 15:00 EST" "+%F %R %Z"`.

A program that fails is a normal result with its exit code. Only an unknown language or a
program that cannot start is a tool error. At worst, a bad program wastes the pod's CPU until
the time limit ends it.

## Tools and settings

| Tool | Input | Result |
|------|-------|--------|
| `execute_code` | `language` (`python` or `bash`), `code`, optional `filename` | stdout, stderr, exit code, execution time |
| `validate_code` | the same | whether the syntax is valid, and the errors if not |

| Setting | Default | Meaning |
|---------|---------|---------|
| `EXECUTION_TIMEOUT` | `30` | Seconds a program may run |
| `MAX_OUTPUT_BYTES` | `65536` | Cap on each of stdout and stderr |
| `SANDBOX_TMPDIR` | the OS default | Where run directories are created |
| `MCP_TRANSPORT` | `stdio` | `stdio` or `http`. Use `http` for new installs. |
| `PORT` | `3000` | The listening port when the transport is `http`; an invalid value stops the server at start |

These are environment variables on the sandbox container. The component reference is the
[code-sandbox README](https://github.com/kubemoot/kubemoot/blob/main/code-sandbox/README.md).

## Isolation: run it under a sandboxed runtime

The containment above limits what a program can reach through the pod. It does not change the
fact that the program runs on the node's kernel. A runtime such as [gVisor](https://gvisor.dev)
puts a user-space kernel between the pod and the host. With it, model-written code no longer
reaches the host kernel directly.

The sandbox is an MCPServer, so it takes the same optional field as any MCP server,
`spec.runtimeClassName`:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: MCPServer
metadata:
  name: code-sandbox
spec:
  # image and the workspace-access sidecar as in your crew
  transport: http
  port: 3000
  healthPath: /healthz
  readinessPath: /readyz
  env:
    - name: MCP_TRANSPORT
      value: http
  runtimeClassName: gvisor
```

Under a sandboxed runtime the sandbox must use `transport: http`. In `stdio` mode the operator
adds the mcp-bridge sidecar, which talks to the server over named pipes in a volume the two
containers share. gVisor does not share those pipes between containers, so the bridged
sandbox crash-loops. With `http` the sandbox serves MCP itself over Streamable HTTP at `/mcp`,
the operator adds no bridge, and the gateway calls it directly. The same applies to any MCP
server: third-party stdio servers behind the bridge do not run under gVisor without
node-level containerd configuration, so run sandboxed only servers that speak HTTP.

Setting it is optional. Left unset, the pod carries no `runtimeClassName` and runs under the
node's default runtime, so a plain kind cluster with default settings keeps working with no
extra install. To use a sandboxed runtime, the cluster owner installs it on the nodes that
will run the sandbox and creates a `RuntimeClass` whose `handler` names it. Kubemoot installs
neither. [Isolation](../isolation/) covers the install steps and the same field for Agents.

### When the class is missing

If the MCPServer names a class that does not exist, the operator checks before it builds
anything:

| Condition | Meaning |
|-----------|---------|
| `RuntimeClassAvailable=True` | The class exists. The Deployment is built with it. |
| `RuntimeClassAvailable=False`, reason `RuntimeClassMissing` | The class does not exist. The operator holds the Deployment, sets the MCPServer phase to `Pending`, and puts the missing name in the condition message. |
| absent | The MCPServer asks for no class. |

The fix is on the cluster: install the runtime and create the `RuntimeClass`, or clear
`runtimeClassName`. The operator watches RuntimeClass objects, so the server proceeds the
moment the class appears, with no retry timer. A Deployment that already exists is left as it
is if the class is deleted while it runs.

The compute agent has no sandbox to call while the server is held, so a crew that asks for a
class its cluster does not have cannot compute until the class exists. Check
`RuntimeClassAvailable` first when the compute agent reports that its tool is unavailable.

These controls are one part of how a crew resists injected instructions. The
[Crew threat model](../crew-threat-model/) shows where each one acts and what remains
undefended.
