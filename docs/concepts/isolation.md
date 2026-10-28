---
title: "Isolation"
weight: 57
description: "How to run agent and MCP server pods under a sandboxing runtime such as gVisor with a Kubernetes RuntimeClass, why it is optional, and what the RuntimeClassAvailable condition says."
---

An agent that calls tools or runs generated code should not be able to reach the host
kernel directly. What turns a pod into a sandbox is its runtime class: the container runtime
the node uses to start it. A runtime such as [gVisor](https://gvisor.dev) puts a user-space
kernel between the agent and the host.

Kubemoot builds no sandbox. It sets the standard `runtimeClassName` field on the pods it
creates for Agents and MCP servers and leaves the runtime to the cluster.

## It is optional

Nothing changes until you ask. With no runtime class set, the pods carry no
`runtimeClassName` and run under the node's default runtime, as they always did. A plain
cluster, such as a kind cluster with default settings, needs no extra install.

## What you install

Kubemoot does not install the runtime. To use one, the cluster owner:

1. Installs the runtime on the nodes that run agents (gVisor's `runsc` and its containerd
   shim, for example).
2. Creates a `RuntimeClass` object whose `handler` names that runtime, for example one named
   `gvisor`.

## Where you set it

Set the class for every Agent in the realm in KubemootConfig:

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: KubemootConfig
metadata:
  name: default
spec:
  defaults:
    agentRuntimeClassName: gvisor
```

An Agent can override it with `spec.deployment.runtimeClassName`. A value there wins over the
realm default, and an empty string (`""`) runs that one Agent under the node's default
runtime. Both fields are described in the [Agent reference](../../reference/agent/#specdeployment)
and the [KubemootConfig guide](../../reference/kubemootconfig-guide/).

Changing the class rolls the agent's pods, like any other pod template change.

## The RuntimeClassAvailable condition

When an Agent asks for a class that does not exist, pods that name it would be rejected one
by one and the Deployment would fail with a stream of events. The operator checks first.

| Condition | Meaning |
|-----------|---------|
| `RuntimeClassAvailable=True` | The class exists. The Deployment is built with it. |
| `RuntimeClassAvailable=False`, reason `RuntimeClassMissing` | The class does not exist. The operator holds the Deployment, sets the Agent phase to `Pending`, and puts the missing name in the condition message. |
| absent | The Agent asks for no class. |

The fix is on the cluster: create the `RuntimeClass` (and install the runtime behind it), or
clear `runtimeClassName`. The operator watches RuntimeClass objects, so the Agent proceeds
the moment the class appears, with no retry timer.

Only the Deployment is held. The Agent's Service and network policies are still applied. A
Deployment that already exists is left as it is: if the class is deleted while the agent runs,
the condition turns `False` and the phase `Pending`, but the running pods keep serving and are
not replaced until the class exists again.

## What it covers

The setting applies to the pods the operator creates for Agents and for MCP servers. An MCP
server sets its own class with `spec.runtimeClassName`, described in the
[MCPServer reference](../../reference/mcpserver-guide/). There is no realm default for MCP
servers, so each server that should run sandboxed names the class. The same
`RuntimeClassAvailable` condition and the same hold apply to an MCPServer whose class is
missing, with the phase `Pending`.

The [code sandbox](../code-sandbox/) is the MCP server that benefits most, because it runs
code a model wrote. The gateways do not use the setting yet.
