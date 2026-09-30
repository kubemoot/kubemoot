---
title: "Build a Crew"
weight: 10
description: "Author a crew from agents, prompts, and models."
---

This guide builds a crew from its parts. A crew is a set of Kubernetes resources, so
"building" one means declaring those resources - usually packaged together as a Helm
chart so they version and deploy as a unit.

## What a crew is made of

| Resource | Role in the crew |
|----------|------------------|
| `Crew` | The team - groups agents by the `kubemoot.ai/crew` label, enables the discussion gateway, configures working memory. |
| `Agent` | Each participant: one coordinator, plus Toolers (and optionally Analysts). Declares capabilities, tools, RAG, and prompt refs. |
| `PromptModule` | Each agent's behavior, written in [ADL](../write-agents-and-adl/) and composed by reference. |
| `Model` / `ModelProvider` | The models (by capability label) and the GPU-backed endpoints that serve them. |
| `MCPServer` / `MCPGateway` | The tools Toolers use, reached through the gateway. |
| `RAGSource` | Knowledge sources, indexed and made queryable. |

## Steps

### 1. Define the crew

Declare a `Crew` with the discussion gateway enabled, and a label every agent will
share. See the [Crew CRD reference](../../reference/crew/) for the full spec including
the working-memory policy.

```yaml
apiVersion: kubemoot.ai/v1alpha1
kind: Crew
metadata:
  name: my-crew
  namespace: crew-my-crew
  labels:
    kubemoot.ai/crew: my-crew
spec:
  description: "What this crew is for"
  discussion:
    enabled: true
  memory:
    enabled: true
```

### 2. Add a coordinator, Toolers, and Analysts

Each agent is an `Agent` CR carrying the `kubemoot.ai/crew: my-crew` label. Give the
crew exactly one coordinator (`discussRole: coordinator`) and one or more Toolers
(`discussRole: tooler`), and optionally Analysts (`analyst` role). An agent
declares *capabilities*, not a model: the scheduler binds the model. See
[Crews & Agents](../../concepts/crews-and-agents/) for roles and the
[Agent CRD reference](../../reference/agent/) for every field.

### 3. Write the prompts in ADL

An agent's behavior lives in `PromptModule` CRs referenced by `spec.promptRefs`, never
inline. Write them in ADL (the Architecture Definition Language). See
[Write Agents & ADL](../write-agents-and-adl/).

### 4. Wire up tools and knowledge

Add the `MCPServer`s and an `MCPGateway` for the tools your Toolers need
([MCP Tools](../../concepts/mcp-tools/)), and `RAGSource`s for knowledge Analysts
should retrieve. Keep each Tooler's tool set small (~10-15 tools).

### 5. Package and deploy

Bundle the resources as a Helm chart so the crew deploys and versions as a unit, then
install it into the crew's namespace:

```bash
helm upgrade --install my-crew ./charts/my-crew \
  --namespace crew-my-crew --create-namespace
kubectl get agents,crew -n crew-my-crew
```

## Keep the crew portable

Don't bake anything cluster-specific (node names, label schemes, topology) into
prompts. A crew should discover those at runtime and keep them in working memory, so
the same crew chart runs unchanged on another cluster. See the working-memory section
of the [Crew CRD reference](../../reference/crew/).

## Next

- [Compose a Crew](../compose-a-crew/) - decide which Toolers and Analysts to include,
  write resumes the coordinator can reason over, and ground the coordinator so it
  routes questions intelligently and writes helpful advisory briefs.
- [Write Agents & ADL](../write-agents-and-adl/) - define behavior.
- [Define Fitness Functions](../define-fitness-functions/) - measure the crew.
- [Onboard MCP Tools](../onboard-mcp-tools/) - add capabilities.
- [Develop Crews in VS Code](../develop-crews-in-vscode/) - do this same workflow from
  CrewForge's Crew Sources view instead of the terminal.
