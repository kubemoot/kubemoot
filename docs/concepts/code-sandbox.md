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

The sandbox has no network path to the places data lives, so the data comes to it:

1. A tooler agent fetches the data from the live system and spills it to the crew's
   [artifact store](../discussion-artifact-store/), passing a reference on the bus.
2. The artifact-access materializer, a sidecar in the sandbox pod, fetches the referenced
   objects from the store and writes them to a shared volume under `/artifacts`.
3. The compute agent's program reads those files and computes over them locally.

The sandbox never talks to the bus or the store itself. Data goes in the program's code or in
a file under `/artifacts`, never through stdin.

## How a run is contained

The sandbox runs code a model wrote, so every run is locked down.

- **No credentials and no Kubernetes access.** The pod holds no secrets and no service
  account token. It only computes over data already handed to it.
- **Limited egress.** With [network policies](../network-policies/) on, the crew's policy
  limits what the pod can reach. It has no route to the bus or to external services.
- **A fresh temporary directory.** Each run starts in a new directory, which is also its
  `HOME`, and the directory is removed afterwards.
- **No stdin.** A read returns end of input at once, so a program that expects piped data
  fails fast instead of waiting.
- **Its own process group.** At the time limit the whole group is killed, and so is anything
  a finished program left running in the background.
- **Capped output.** Stdout and stderr are each capped, and the result says when output was
  cut.
- **A small image.** Python with the standard library only, bash, and an init process that
  reaps what a killed program leaves behind.

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
  # image, transport, and the artifact-access sidecar as in your crew
  runtimeClassName: gvisor
```

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
