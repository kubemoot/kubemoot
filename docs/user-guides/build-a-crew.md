---
title: "Build a Crew"
weight: 10
description: "Author a crew from agents, prompts, and models."
---

This guide builds a crew from its parts. A crew is a set of Kubernetes resources, so
"building" one means declaring those resources - usually packaged together as a Helm
chart so they version and deploy as a unit.

To see a complete small crew before writing your own, scaffold the
[starter crew](../starter-crew/) with `kmctl create`.

## What a crew is made of

| Resource | Role in the crew |
|----------|------------------|
| `Crew` | The team - groups agents by the `kubemoot.ai/crew` label, enables the discussion gateway, configures working memory. |
| `Agent` | Each participant: one coordinator, plus Toolers (and optionally Analysts). Declares capabilities, tools, RAG, and prompt refs. |
| `PromptModule` | Each agent's behavior, written in [ADL](../write-agents-and-adl/) and composed by reference. |
| `Model` / `ModelServer` | The models (by capability label) and the GPU-backed endpoints that serve them. |
| `MCPServer` / `MCPGateway` | The tools Toolers use, reached through the gateway. |
| `RAGSource` | Knowledge sources, indexed and made queryable. |
| `EmbeddingModel` | The embedding model a RAGSource indexes with. It names the model and its dimensions, never a server. |

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
(set `discussRole: tooler` explicitly; there is no default role), and optionally Analysts (`analyst` role). An agent
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
should retrieve. A RAGSource names an `EmbeddingModel`, and an `EmbeddingModel` names the
model (`nomic-embed-text`) and its `dimensions`, with no `serverRef`: stored vectors match
only the model that made them, so the crew pins the model, and the cluster supplies a server
that has it ([binding](../../reference/models/#binding)). Keep each Tooler's tool set small (~10-15 tools). For a server Kubemoot
builds, such as the artifact read-ops server, set `kubemootImage` instead of an image so the
crew never names a version ([example](../../reference/mcpserver-guide/#run-a-kubemoot-built-server-without-naming-a-version)).

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

## Choosing a harness mode

An agent's reasoning runs in a **harness**. Each `Agent` picks one of two modes in
`spec.harness.mode`:

| Mode | What runs | When to pick it |
|---|---|---|
| `legacy` (default) | The agent-runtime that ships with Kubemoot, alone in the agent container. It talks to NATS, the model servers, and the gateway itself. | Every agent in the guides above. It is today's default and needs no `harness` block. |
| `sidecar` | Your harness image beside a [Moot Mesh sidecar](../../architecture/moot-mesh/). The sidecar owns the bus, model placement, and tool access; the harness serves one A2A endpoint and calls two loopback URLs. | You want a different harness at the table: an agent framework, a command-line coding agent behind a thin shim, or code you wrote. |

Sidecar mode ships opt-in. Legacy stays the default, and a crew can mix both modes.
Set the fields under [`spec.harness`](../../reference/agent/#specharness).

### What a harness must do

A harness joins the table at one of three levels. Each adds to the one before it.

| Level | The harness |
|---|---|
| **Voice** | Serves its agent card at `/.well-known/agent-card.json` and answers A2A `SendMessage` on `KUBEMOOT_TURN_PORT`. Calls the model at `KUBEMOOT_MODEL_URL`, naming an alias (`default` or `triage`) as the model and sending the per-turn key from the turn as the API key. |
| **Worker** | Also uses the tools at `KUBEMOOT_TOOLS_URL` and returns the structured reply: a data part with a `signal` (`agree`, `concern`, `block`, or `stand_aside`), `content`, `reason`, and `evidence`. A plain text answer counts as agree. |
| **Good guest** | Also streams status while it works, cancels the work when the turn is canceled (the thread closed or a person pressed Stop), and propagates the W3C trace context it receives onto the model and tool calls it makes. |

A voice-level harness is enough to take part. The higher levels give the crew
better evidence, a faster stop, and traces that follow the question through the
harness.

If the harness fails, times out, or returns nothing, the sidecar publishes a
`failure` signal for it. A harness never needs to catch its own errors to keep the
crew informed.

### Native tools

Many harnesses ship their own tools: a shell, file access, a browser. The Moot Mesh
cannot see what a harness does with them, so `spec.harness.nativeTools` states the
choice:

- `none` (default): the harness uses only the tools at the tools door, where the
  agent's `enabledTools` and `disabledTools` apply and every call is counted.
- `declared`: the built-ins you reviewed and declared stay on.
- `all`: every built-in stays on.

Anything but `none` is a deliberate decision and is shown on the dashboard. The
sidecar can limit and count the tools it serves. It cannot limit a shell the harness
runs itself, so choose `declared` or `all` only for a harness you trust, and consider
running an untrusted one under a sandbox runtime class.

### Tool names and ADL

A harness may add its own prefix to the tool names it shows its model. Write ADL
prompts so they tolerate it: refer to a tool by what it does, or match on the end of
the name, rather than requiring an exact string. See
[Write Agents & ADL](../write-agents-and-adl/).

## Next

- [Compose a Crew](../compose-a-crew/) - decide which Toolers and Analysts to include,
  write resumes the coordinator can reason over, and ground the coordinator so it
  routes questions intelligently and writes helpful advisory briefs.
- [Write Agents & ADL](../write-agents-and-adl/) - define behavior.
- [The Moot Mesh](../../architecture/moot-mesh/) - how a sidecar-mode harness joins the table.
- [Define Fitness Functions](../define-fitness-functions/) - measure the crew.
- [Onboard MCP Tools](../onboard-mcp-tools/) - add capabilities.
- [Develop a crew in VS Code](../../ecosystem/crewforge/develop-a-crew/) - do this same
  workflow from CrewForge instead of the terminal.
